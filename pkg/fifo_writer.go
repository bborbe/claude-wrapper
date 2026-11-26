// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	stderrors "errors"
	"os"
	"sync"
	"syscall"
	"time"
)

// fifoRetryInterval is how long a full FIFO is left before the write is retried.
//
// It only costs anything while a child is not draining its stdin; a healthy
// child takes each line immediately and the loop never sleeps.
const fifoRetryInterval = 5 * time.Millisecond

// fifoWriter is the daemon's write end of a child's stdin FIFO: a non-blocking
// descriptor whose writes honour a deadline.
//
// *os.File cannot carry a deadline here. On darwin the Go runtime does not put a
// FIFO in its poller at all (golang/go#24164), so SetWriteDeadline answers
// "file type does not support deadline" and a write blocks for as long as the
// child does not read — measured 2026-11-26 as 67 minutes. The descriptor is
// therefore opened O_NONBLOCK and a full FIFO (EAGAIN) is retried until the
// deadline passes.
//
// O_NONBLOCK belongs to this open file description only. The child is handed a
// separate open of the same FIFO, so its own reads stay blocking.
type fifoWriter struct {
	mu       sync.Mutex
	fd       int
	deadline time.Time
	closed   bool
}

// openFIFOWriter opens path read-write and non-blocking.
//
// O_RDWR for the same reason as the child-side open: opening a FIFO for writing
// only would fail (non-blocking) or block until a reader exists, and the reader
// may not be started yet.
func openFIFOWriter(path string) (*fifoWriter, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &fifoWriter{fd: fd}, nil
}

// SetWriteDeadline bounds every later Write. The zero time removes the bound.
func (w *fifoWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}

// Write writes all of p, retrying while the FIFO is full.
//
// It returns os.ErrDeadlineExceeded when the deadline passes first; n then says
// how much of p already went into the FIFO, so the caller knows the stream may
// hold a partial line.
func (w *fifoWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	fd, deadline, closed := w.fd, w.deadline, w.closed
	w.mu.Unlock()
	if closed {
		return 0, os.ErrClosed
	}
	written := 0
	for written < len(p) {
		n, err := syscall.Write(fd, p[written:])
		if n > 0 {
			written += n
		}
		switch {
		case err == nil:
			continue
		case stderrors.Is(err, syscall.EINTR):
			continue
		case stderrors.Is(err, syscall.EAGAIN):
			// Wall clock on purpose: the deadline is a real-time bound, set and
			// checked the way os.File's is. Write keeps io.Writer's signature, so
			// it takes no ctx; the caller folds its ctx deadline into this one.
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return written, os.ErrDeadlineExceeded
			}
			time.Sleep(fifoRetryInterval)
		default:
			return written, &os.PathError{Op: "write", Path: "stdin fifo", Err: err}
		}
	}
	return written, nil
}

// Close releases the descriptor. A second Close is a no-op.
func (w *fifoWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return syscall.Close(w.fd)
}
