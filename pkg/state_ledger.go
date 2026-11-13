// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// The state ledger records every live session's state, since and waits_on, so
// time in state can be measured.
//
// The blind spot it exists for, measured 2026-11-05: 44 live sessions counted,
// 25 with zero output tokens in 15 minutes, and no instrument could say why.
// The instruments count stocks (sessions live, tasks ready) and never time in
// state, so a stalled fleet reads exactly like a busy one — a session in
// `decision` for four hours and one in `external` for four hours need opposite
// fixes and both read as "idle".
//
// ⚠️ This is NOT the attention-controller's `/api/1.0/session-ledger`. That is
// spawn-lifecycle rows (status, spawned_at, result) with no `since` and no
// waits_on; it cannot answer how long anything waited. This job writes the
// local append-only events file and nothing else.
const (
	// DefaultStateLedgerDir is where the append-only events files live. The
	// Python writer used the same directory, so a port keeps one store rather
	// than two.
	DefaultStateLedgerDir = "~/.claude/state/state-ledger"
	// DefaultStateLedgerInterval matches the heartbeat poster's cadence: both
	// observe the same sessions, and a slower ledger would make "time in state"
	// coarser than the liveness it is meant to explain.
	DefaultStateLedgerInterval = 30 * time.Second
	// StateLedgerJobName is the name this job writes in its `job=` lines.
	StateLedgerJobName = "state-ledger"
)

// stateLedgerLine is one line of the events file.
//
// Two kinds share the file and the split is load-bearing: a `transition` line
// is written only when a session's state CHANGES, a `heartbeat` line once per
// poll per live session. Without the heartbeat there is no evidence a session
// stayed in a state rather than the poller dying; without the transition the
// file would have to be scanned to find the boundaries.
type stateLedgerLine struct {
	Kind string `json:"kind"`
	SID  string `json:"sid"`
	// State is the service's own four-state vocabulary, read from the child's
	// event stream — never inferred from an age.
	State string `json:"state"`
	// Since is when this state began, derived from the reported silence: the
	// service does not retain a per-state start time, so it is computed as
	// now minus the last event's age and is only as precise as that report.
	Since      string `json:"since,omitempty"`
	Role       string `json:"role,omitempty"`
	LastSeen   string `json:"last_seen,omitempty"`
	RecordedAt string `json:"recorded_at"`
}

// StateLedgerJob appends every live session's state to a daily events file.
//
// It replaces `~/.claude/scripts/state-ledger.py`, which polled a registry from
// outside for state this process already holds.
type StateLedgerJob struct {
	Supervisor Supervisor
	// Dir is the directory holding `events-<date>.jsonl`. Empty means
	// DefaultStateLedgerDir.
	Dir string
	// Interval is how often the file is appended to. Zero means
	// DefaultStateLedgerInterval.
	Interval time.Duration
	// Now supplies the clock. Nil means the process clock.
	Now libtime.CurrentDateTimeGetter
	// Logf writes the job's lines. Nil means log.Printf.
	Logf func(line string)
	// seen is the last state written per session id, so a transition is written
	// once rather than on every poll. It is only touched from Tick, which Run
	// calls serially.
	seen map[string]string
}

// Run ticks until ctx is done.
func (j *StateLedgerJob) Run(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultStateLedgerInterval
	}
	return RunTick(ctx, interval, j.TickAndLog)
}

// TickAndLog runs one poll and writes its line.
func (j *StateLedgerJob) TickAndLog(ctx context.Context) {
	written, err := j.Tick(ctx)
	if err != nil {
		j.logf(JobLine(StateLedgerJobName, "failed", "error", err.Error()))
		return
	}
	j.logf(JobLine(StateLedgerJobName, "appended",
		"file", j.path(j.now().Format("2006-01-02")),
		"lines", strconv.Itoa(written),
	))
}

// Tick appends one heartbeat line per live session and a transition line for
// each session whose state changed since the previous poll. It returns how many
// lines it wrote.
func (j *StateLedgerJob) Tick(ctx context.Context) (int, error) {
	sessions, err := j.Supervisor.List(ctx)
	if err != nil {
		return 0, errors.Wrap(ctx, err, "list sessions failed")
	}
	now := j.now()
	lines := j.buildLines(sessions, now)
	if len(lines) == 0 {
		return 0, nil
	}
	if writeErr := j.appendLines(ctx, lines, now.Format("2006-01-02")); writeErr != nil {
		return 0, errors.Wrap(ctx, writeErr, "append events failed")
	}
	return len(lines), nil
}

// buildLines decides what to write and records the state it saw, so the next
// poll can tell a transition from a repeat.
func (j *StateLedgerJob) buildLines(sessions []Session, now time.Time) []stateLedgerLine {
	if j.seen == nil {
		j.seen = map[string]string{}
	}
	lines := make([]stateLedgerLine, 0, 2*len(sessions))
	live := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		// A session with no state yet has not emitted its init event; writing a
		// transition for it would record a state the child never claimed.
		if session.State == "" {
			continue
		}
		live[session.ID] = struct{}{}
		line := stateLedgerLine{
			SID:        session.ID,
			State:      string(session.State),
			Role:       session.Role,
			RecordedAt: now.UTC().Format(time.RFC3339Nano),
		}
		if previous, ok := j.seen[session.ID]; !ok || previous != line.State {
			transition := line
			transition.Kind = "transition"
			transition.Since = sinceOf(session, now)
			lines = append(lines, transition)
			j.seen[session.ID] = line.State
		}
		heartbeat := line
		heartbeat.Kind = "heartbeat"
		heartbeat.LastSeen = line.RecordedAt
		lines = append(lines, heartbeat)
	}
	// A session that is gone is forgotten, or the map grows for the life of the
	// process and a later session reusing an id would never see its transition.
	for id := range j.seen {
		if _, ok := live[id]; !ok {
			delete(j.seen, id)
		}
	}
	return lines
}

// sinceOf is when the session's current state began, from the reported silence.
func sinceOf(session Session, now time.Time) string {
	if session.LastEventAgeSeconds == nil {
		return ""
	}
	began := now.Add(-time.Duration(*session.LastEventAgeSeconds) * time.Second)
	return began.UTC().Format(time.RFC3339Nano)
}

// appendLines writes the lines as one append, so a poll cannot interleave with
// another writer's.
func (j *StateLedgerJob) appendLines(
	ctx context.Context, lines []stateLedgerLine, date string,
) error {
	path := j.path(date)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create %s failed", filepath.Dir(path))
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrapf(ctx, err, "open %s failed", path)
	}
	defer func() { _ = file.Close() }()
	for _, line := range lines {
		encoded, marshalErr := json.Marshal(line)
		if marshalErr != nil {
			return errors.Wrapf(ctx, marshalErr, "marshal ledger line failed")
		}
		if _, writeErr := file.Write(append(encoded, '\n')); writeErr != nil {
			return errors.Wrapf(ctx, writeErr, "write %s failed", path)
		}
	}
	return nil
}

// path is the daily events file. The date boundary is the file's only rotation.
func (j *StateLedgerJob) path(date string) string {
	dir := j.Dir
	if dir == "" {
		dir = DefaultStateLedgerDir
	}
	return filepath.Join(expandedDir(dir), "events-"+date+".jsonl")
}

func (j *StateLedgerJob) now() time.Time {
	if j.Now == nil {
		return time.Now()
	}
	return j.Now.Now().Time()
}

func (j *StateLedgerJob) logf(line string) {
	if j.Logf != nil {
		j.Logf(line)
		return
	}
	log.Printf("%s", line)
}
