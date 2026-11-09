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
//
// onLine's `replayed` reports that the line was already in the file when it was
// opened. It is what lets a successor distinguish history it is re-reading from a
// turn that just closed, which is the difference between filing a verdict and
// re-filing one the row already carries.
func tailFile(
	ctx context.Context,
	path string,
	onLine func(seq int, line []byte, replayed bool),
	gone func() bool,
) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()

	seq, ok := replayStart(file)
	if !ok {
		return
	}
	// Everything below this offset existed when the file was opened; everything at
	// or beyond it was appended after. Taken AFTER the seek, so a large file's
	// skipped head is excluded from the replay range rather than counted as live.
	var replayEnd int64
	sizeKnown := false
	if info, statErr := file.Stat(); statErr == nil {
		replayEnd = info.Size()
		sizeKnown = true
	}
	// The fallback is the sequence number, which is only right for an unskipped
	// file; the seek is what actually knows where the read will start.
	offset := seekOffset(file, int64(seq))

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
			var consumed int64
			pending, seq, consumed = emitCompleteLines(pending, seq, offset, replayEnd, onLine)
			offset += consumed
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return
		}
		// An unreadable size must NOT mean "nothing was replayed": replayEnd would
		// stay 0, every historical line would be classified live, and a successor
		// would re-file a verdict for a turn that ended before it started. The first
		// EOF is the boundary instead — everything read up to it was already on disk.
		if !sizeKnown {
			replayEnd = offset
			sizeKnown = true
		}
		if gone() {
			return
		}
		time.Sleep(eventTailPoll)
	}
}

// seekOffset reports the file offset the tailer is positioned at.
//
// The seek already happened in replayStart, so this reads it back rather than
// recomputing it: the two must agree, and asking the file is the only way to know
// where the read will actually start.
func seekOffset(file *os.File, fallback int64) int64 {
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fallback
	}
	return offset
}

// replayStart seeks to where the replay begins and reports the sequence number the
// first replayed line carries.
//
// ok is false only when neither the window seek nor a reset to zero succeeded,
// which leaves nothing readable to replay.
func replayStart(file *os.File) (int, bool) {
	start, skipped := backfillWindow(file)
	if start <= 0 {
		return 0, true
	}
	if _, seekErr := file.Seek(start, io.SeekStart); seekErr != nil {
		// A seek that fails leaves the offset where Stat left it; restarting from
		// zero is always correct, just slower.
		if _, resetErr := file.Seek(0, io.SeekStart); resetErr != nil {
			return 0, false
		}
		return 0, true
	}
	return skipped, true
}

// emitCompleteLines hands every newline-terminated line in pending to onLine,
// starting at seq, and returns the unconsumed tail, the next sequence number and
// how many bytes it consumed.
//
// A partial line stays in the tail: the child may be mid-write, and emitting half
// a JSON object would be worse than emitting it one poll later.
//
// `offset` is the file offset of pending[0] and `replayEnd` the size the file had
// when it was opened; a line that starts before replayEnd was already there. The
// consumed count is what keeps `offset` honest for the next call — without it
// every line in a later read would be classified against the wrong position.
func emitCompleteLines(
	pending []byte,
	seq int,
	offset int64,
	replayEnd int64,
	onLine func(seq int, line []byte, replayed bool),
) ([]byte, int, int64) {
	var consumed int64
	for {
		index := bytes.IndexByte(pending, '\n')
		if index < 0 {
			return pending, seq, consumed
		}
		onLine(seq, pending[:index], offset+consumed < replayEnd)
		consumed += int64(index) + 1
		pending = pending[index+1:]
		seq++
	}
}

// backfillWindow reports where a large file's replay should start and how many
// lines that start skips, so sequence numbers stay absolute.
//
// It returns (0, 0) for a file small enough to replay whole. Otherwise it seeks
// to the last maxBackfillBytes, discards the partial line the window opens on,
// and counts the newlines before the line it will actually replay — the count is
// what keeps a successor's sequence numbers identical to the ones the spawning
// daemon used.
//
// The count is taken to the START of the replay, not to the window edge. The
// line break that terminates the discarded partial line sits inside the window,
// so counting only the bytes before the window undercounts by exactly one and
// every replayed line is numbered one lower than its own index. That shift is
// not cosmetic: a client resuming at Last-Event-ID+1 would then skip one event —
// possibly a `result` — on the first reconnect after any restart, and the drift
// would grow by one per restart.
func backfillWindow(file *os.File) (int64, int) {
	info, err := file.Stat()
	if err != nil || info.Size() <= maxBackfillBytes {
		return 0, 0
	}
	windowStart := info.Size() - maxBackfillBytes
	// Discard the partial line the window opens on: its head is outside the
	// window, so parsing it would either fail or — worse — succeed on a truncated
	// object that happens to parse.
	buf := make([]byte, 4096)
	offset := windowStart
	for offset < info.Size() {
		if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
			return 0, 0
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if index := bytes.IndexByte(buf[:n], '\n'); index >= 0 {
				start := offset + int64(index) + 1
				return start, countNewlines(file, start)
			}
			offset += int64(n)
		}
		if readErr != nil {
			return 0, 0
		}
	}
	// No line break anywhere in the window: the whole tail is one unterminated
	// line, so replay starts at EOF and every complete line precedes the window.
	// Counting to windowStart is both correct and cheaper than reading the file.
	return info.Size(), countNewlines(file, windowStart)
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
