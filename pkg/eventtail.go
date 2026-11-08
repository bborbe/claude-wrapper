// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"io"
	"os"
	"syscall"
	"time"
)

// eventTailPoll is how often a drained event file is re-read.
//
// The file is read by offset, so a poll costs one read syscall that returns
// nothing until the child speaks. 100 ms is well under the latency a supervisor
// notices on a supervision stream and far cheaper than a watch descriptor per
// session.
const eventTailPoll = 100 * time.Millisecond

// maxBackfillBytes bounds how much of an event file is replayed on adoption.
//
// A session that has run for hours has a large file, and a restart must not
// spend seconds parsing it before the daemon can serve. The cap is on the replay
// only: the file itself is never truncated, because truncating a file another
// process holds an O_APPEND descriptor for leaves its offset past the new end and
// the next write lands in a sparse hole.
const maxBackfillBytes = 8 << 20

// tailFile reads one session's event file as it grows, handing every complete
// line to onLine with its sequence number.
//
// The sequence number is the line's index in the file, which is what makes a
// resume point stable across a daemon restart: the file is append-only, so a
// given line's index never changes, and a successor that re-reads the same file
// assigns the same numbers.
//
// gone reports that the child's process is no longer running. Once it does and
// the file has been read to its end, the tailer returns — a dead child will
// never append again, so continuing to poll would only leak a goroutine per
// exited session.
func tailFile(
	ctx context.Context,
	path string,
	onLine func(seq int, line []byte),
	gone func() bool,
) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()

	seq := 0
	if start, skipped := backfillWindow(file); start > 0 {
		if _, seekErr := file.Seek(start, io.SeekStart); seekErr != nil {
			// A seek that fails leaves the offset where Stat left it; restarting
			// from zero is always correct, just slower.
			if _, resetErr := file.Seek(0, io.SeekStart); resetErr != nil {
				return
			}
			seq = 0
		} else {
			seq = skipped
		}
	}

	pending := make([]byte, 0, 64*1024)
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, readErr := file.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				index := bytes.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				line := pending[:index]
				pending = pending[index+1:]
				onLine(seq, line)
				seq++
			}
		}
		if n == 0 {
			if readErr != nil && readErr != io.EOF {
				return
			}
			if gone() {
				return
			}
			time.Sleep(eventTailPoll)
		}
	}
}

// backfillWindow reports where a large file's replay should start and how many
// lines that start skips, so sequence numbers stay absolute.
//
// It returns (0, 0) for a file small enough to replay whole. Otherwise it seeks
// to the last maxBackfillBytes, discards the partial line the window opens on,
// and counts the newlines in the skipped prefix — the count is what keeps a
// successor's sequence numbers identical to the ones the spawning daemon used.
func backfillWindow(file *os.File) (int64, int) {
	info, err := file.Stat()
	if err != nil || info.Size() <= maxBackfillBytes {
		return 0, 0
	}
	skipped := countNewlines(file, info.Size()-maxBackfillBytes)
	start := info.Size() - maxBackfillBytes
	// Discard the partial line the window opens on: its head is outside the
	// window, so parsing it would either fail or — worse — succeed on a truncated
	// object that happens to parse.
	buf := make([]byte, 4096)
	offset := start
	for offset < info.Size() {
		if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
			return 0, 0
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if index := bytes.IndexByte(buf[:n], '\n'); index >= 0 {
				return offset + int64(index) + 1, skipped
			}
			offset += int64(n)
		}
		if readErr != nil {
			return 0, 0
		}
	}
	return info.Size(), skipped
}

// countNewlines counts the line breaks in the first limit bytes of file.
func countNewlines(file *os.File, limit int64) int {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0
	}
	buf := make([]byte, 64*1024)
	total := 0
	var offset int64
	for offset < limit {
		want := int64(len(buf))
		if remaining := limit - offset; remaining < want {
			want = remaining
		}
		n, err := file.Read(buf[:want])
		if n > 0 {
			total += bytes.Count(buf[:n], []byte{'\n'})
			offset += int64(n)
		}
		if err != nil {
			break
		}
	}
	return total
}

// processAlive reports whether pid names a running process.
//
// Signal 0 performs the permission and existence checks without delivering
// anything. EPERM is a live process this user may not signal, which is still a
// live process — reading it as dead would abandon a child that is running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// pollForProcessExit blocks until pid is no longer running, checking every
// eventTailPoll.
//
// It is the fallback for a platform without kqueue and for a kqueue
// registration that failed — never the primary path on macOS, where
// waitForProcessExit delivers the exit as an event instead.
func pollForProcessExit(pid int) {
	for processAlive(pid) {
		time.Sleep(eventTailPoll)
	}
}
