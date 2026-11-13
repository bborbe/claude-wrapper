// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	libtime "github.com/bborbe/time"
)

// The session-exit store records how a session ended, so a dead session can be
// attributed after the fact.
//
// Before it, a session that died left no trace: the task file kept its
// claude_session_id, the registry kept a pid, and both still read as "running"
// long after the process was gone — a killed session and one that was never
// started looked identical from every surface the fleet reads.
//
// ⚠️ **Keyed by session, not by pane.** `session-exit-record.py` writes
// `pane-<pane_id>.log`, which it can do because it runs inside a pane shell. The
// daemon spawns headless children that have no pane and it retains no pane id,
// so the only key it holds is the session id. Both families live in the same
// directory and a reader globs it.
const (
	// DefaultSessionExitDir is where the per-session exit files live.
	DefaultSessionExitDir = "~/.claude/state/session-exit"
	// SessionExitWriterWrapper marks a record the daemon wrote, beside the
	// script's `trap` and `observer`.
	SessionExitWriterWrapper = "wrapper"
	// SessionExitJobName is the name this job writes in its `job=` lines.
	SessionExitJobName = "session-exit-daily"
)

// SessionExitRecorder records how one session ended.
//
// It takes no context: it is called from the child's exit path, which holds the
// child's own lock and has no request to cancel against. A recorder that blocks
// delays settling a session, so an implementation must be a bounded local write.
type SessionExitRecorder interface {
	Record(session Session, pid int, exitCode *int)
}

// SessionExitFileRecorder appends one line per ended session to
// `<dir>/session-<sid>.log`.
//
// It replaces the `append`/`observe` writers of `session-exit-record.py` for the
// children the daemon owns: the daemon is the parent, so it observes the exit
// directly and needs neither an EXIT trap nor a detached kqueue observer.
type SessionExitFileRecorder struct {
	// Dir holds the per-session files. Empty means DefaultSessionExitDir.
	Dir string
	// Now supplies the clock. Nil means the process clock.
	Now libtime.CurrentDateTimeGetter
	// Logf writes the job's lines. Nil means log.Printf.
	Logf func(line string)
}

// Record appends the session's exit line. A failed write is logged, never
// returned: the child is already gone, and refusing to settle it because a
// diagnostic file could not be appended would be the worse failure.
func (r *SessionExitFileRecorder) Record(session Session, pid int, exitCode *int) {
	path := r.path(session.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		r.logf(JobLine(SessionExitJobName, "write-failed",
			"sid", session.ID, "error", err.Error()))
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		r.logf(JobLine(SessionExitJobName, "write-failed",
			"sid", session.ID, "error", err.Error()))
		return
	}
	defer func() { _ = file.Close() }()
	if _, writeErr := file.WriteString(r.line(session, pid, exitCode)); writeErr != nil {
		r.logf(JobLine(SessionExitJobName, "write-failed",
			"sid", session.ID, "error", writeErr.Error()))
		return
	}
	r.logf(JobLine(SessionExitJobName, "recorded",
		"sid", session.ID, "kind", exitKind(exitCode)))
}

// line is one tab-separated record, the shape the existing reader parses.
//
// `pane=-` is deliberate rather than omitted: a reader that splits on tabs sees
// a stable field count, and `-` is already the store's spelling for "not
// applicable".
func (r *SessionExitFileRecorder) line(session Session, pid int, exitCode *int) string {
	return r.now().UTC().Format(time.RFC3339) +
		"\tkind=" + exitKind(exitCode) +
		"\tpane=-" +
		"\tsession=" + session.ID +
		"\tpid=" + strconv.Itoa(pid) +
		"\trc=" + exitCodeField(exitCode) +
		"\twriter=" + SessionExitWriterWrapper + "\n"
}

// exitKind names how the child ended.
//
// An adopted child is `unknown`, never `exited`: this process never held its
// Wait, so it has no exit status to report and inventing one is the defect the
// store exists to remove.
func exitKind(exitCode *int) string {
	if exitCode == nil {
		return "unknown"
	}
	if *exitCode < 0 {
		return "signalled"
	}
	return "exited"
}

func exitCodeField(exitCode *int) string {
	if exitCode == nil {
		return "-"
	}
	return strconv.Itoa(*exitCode)
}

func (r *SessionExitFileRecorder) path(sid string) string {
	dir := r.Dir
	if dir == "" {
		dir = DefaultSessionExitDir
	}
	return filepath.Join(dir, "session-"+sid+".log")
}

func (r *SessionExitFileRecorder) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now.Now().Time()
}

func (r *SessionExitFileRecorder) logf(line string) {
	if r.Logf != nil {
		r.Logf(line)
		return
	}
	log.Printf("%s", line)
}

// SetSessionExitRecorder installs the exit store on the supervisor.
//
// It is a setter rather than a constructor parameter because the supervisor
// constructor has call sites — tests among them — that do not care about the
// store, and a nil recorder is a no-op rather than a nil dereference. It must be
// called before any child is spawned or adopted, since the field is copied into
// each child at construction.
func (s *supervisor) SetSessionExitRecorder(recorder SessionExitRecorder) {
	s.exitRecorder = recorder
}
