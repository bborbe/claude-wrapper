// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// ExitStatus is how a child left: the code it exited with, the signal that
// killed it, and the daemon job that sent that signal.
//
// One type rather than three arguments because the three are only meaningful
// together: a signalled exit has a signal and, when the daemon sent it, a
// sender; an adopted child has none of them; a clean exit has only a code.
type ExitStatus struct {
	// Code is the exit code, nil when this process never Wait'ed the child and
	// so has no status to report.
	Code *int
	// Signal is the signal that killed the child. Zero means it exited on its
	// own, or the status is unknown.
	Signal syscall.Signal
	// Sender names the daemon job that signalled the child. Empty means the
	// signal came from outside this process, so it cannot be named here.
	Sender string
}

// SessionExitRecorder records how one session ended.
//
// It takes no context: it is called from the child's exit path, which holds the
// child's own lock and has no request to cancel against. A recorder that blocks
// delays settling a session, so an implementation must be a bounded local write.
type SessionExitRecorder interface {
	Record(session Session, pid int, status ExitStatus)
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
func (r *SessionExitFileRecorder) Record(session Session, pid int, status ExitStatus) {
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
	if _, writeErr := file.WriteString(r.line(session, pid, status)); writeErr != nil {
		r.logf(JobLine(SessionExitJobName, "write-failed",
			"sid", session.ID, "error", writeErr.Error()))
		return
	}
	r.logf(JobLine(SessionExitJobName, "recorded",
		"sid", session.ID, "kind", exitKind(status)))
}

// line is one tab-separated record, the shape the existing reader parses.
//
// `pane=-` is deliberate rather than omitted: a reader that splits on tabs sees
// a stable field count, and `-` is already the store's spelling for "not
// applicable".
func (r *SessionExitFileRecorder) line(session Session, pid int, status ExitStatus) string {
	fields := make([]string, 0, 12)
	fields = append(fields,
		r.now().UTC().Format(time.RFC3339),
		"kind="+exitKind(status),
		"pane=-",
		"session="+session.ID,
		"pid="+strconv.Itoa(pid),
		"rc="+exitCodeField(status.Code),
		"signal="+signalField(status),
		"sender="+senderField(status),
		"writer="+SessionExitWriterWrapper,
	)
	fields = append(fields, stallFields(session.Stall)...)
	return strings.Join(fields, "\t") + "\n"
}

// stallFields reports where a child that never left the starting state was
// waiting when it was killed.
//
// They are written only for a session carrying a stall point — one that never
// initialised — so their presence in the record is itself the finding. An
// initialised session's line stays byte-identical to what it was before this
// field existed, which is what keeps every existing reader working: the daily
// job parses the fields as a name→value map, and an absent name reads as absent
// rather than as a shifted column.
func stallFields(stall *Stall) []string {
	if stall == nil {
		return nil
	}
	return []string{
		"stall_phase=" + valueOrDash(stallPhase(stall)),
		"stall_last_event=" + valueOrDash(stall.LastEvent),
		"stall_pending_hooks=" + valueOrDash(strings.Join(stall.PendingHooks, ",")),
	}
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// exitKind names how the child ended.
//
// An adopted child is `unknown`, never `exited`: this process never held its
// Wait, so it has no exit status to report and inventing one is the defect the
// store exists to remove.
func exitKind(status ExitStatus) string {
	if status.Code == nil {
		return "unknown"
	}
	if *status.Code < 0 {
		return "signalled"
	}
	return "exited"
}

// signalField names the signal that killed the child, or explains why it cannot
// be named.
//
// ⚠️ `unavailable(...)` is reserved for an exit this process never Wait'ed: an
// adopted child, or a signalled code whose WaitStatus could not be read. A
// reaped child always carries a concrete signal when one killed it, and `none`
// when it exited on its own — inventing a value would be the defect this store
// exists to remove.
func signalField(status ExitStatus) string {
	if status.Signal != 0 {
		return signalName(status.Signal)
	}
	if status.Code == nil || *status.Code < 0 {
		return "unavailable(no-wait-status)"
	}
	return "none"
}

// senderField names the daemon job that signalled the child.
//
// A signal delivered from outside this process cannot be named here — the
// daemon observes the death, not the sender — so it says so rather than
// substituting a plausible-looking process.
func senderField(status ExitStatus) string {
	if status.Sender != "" {
		return status.Sender
	}
	if status.Signal != 0 {
		return "unavailable(external-signal)"
	}
	return "none"
}

// signalName renders a signal in the store's vocabulary: the two the daemon
// itself sends by name, and the number for anything else.
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return "SIG" + strconv.Itoa(int(sig))
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
	return filepath.Join(expandedDir(dir), "session-"+sid+".log")
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
