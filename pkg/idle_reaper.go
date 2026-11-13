// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/bborbe/errors"
)

// The idle floor is the operator's rule (2026-11-07, restated 2026-11-10): a
// headless worker idle for 15 minutes exits.
//
// It lived in `idle-reaper-tick.py`, run by `com.bborbe.idle-reaper` every
// 900 s. The cost of running it outside was measured on 2026-11-13: completed
// workers stayed live 8.5, 10.5 and 14 minutes past their last turn, because
// the floor could only ever be evaluated on a 15-minute tick — a session that
// went idle one second after a tick waited the whole interval before anything
// looked at it.
//
// ⚠️ The interval is a floor-resolution bound, not the trigger. The service
// already knows the exact moment a child goes idle (state is derived from the
// event stream, never from a timestamp — see CLAUDE.md), so the tick exists
// only to ask "has it now been idle long enough", never to decide *whether* it
// is idle.
const (
	// DefaultIdleFloor is how long a headless worker may rest idle before it is
	// closed.
	DefaultIdleFloor = 15 * time.Minute
	// DefaultIdleReaperInterval is how often the floor is evaluated. It is the
	// worst-case lag past the floor, so it stays at the value launchd used.
	DefaultIdleReaperInterval = 900 * time.Second
	// IdleReaperJobName is the name the reaper writes in its `job=` lines.
	IdleReaperJobName = "idle-reaper"
)

// IdleReaperResult is what one tick did, so the caller can log and a test can
// assert without reading the log.
type IdleReaperResult struct {
	// Closed names the session ids this tick stopped, in list order.
	Closed []string
	// Skipped counts the sessions the floor did not apply to, by reason.
	Skipped map[string]int
}

// IdleReaperJob closes headless children that have rested idle past the floor.
//
// It replaces `idle-reaper-tick.py`. The Python tick enumerated the registry
// for `entrypoint: sdk-cli` + `status: idle` and ran two verbs against each
// candidate; this job reads the same facts from the sessions the service holds
// and calls Stop directly, so there is no registry to read and no subprocess
// to run.
type IdleReaperJob struct {
	Supervisor Supervisor
	// Floor is how long a child may rest idle. Zero means DefaultIdleFloor.
	Floor time.Duration
	// Interval is how often the floor is evaluated. Zero means
	// DefaultIdleReaperInterval.
	Interval time.Duration
	// Logf writes the job's lines. Nil means log.Printf.
	Logf func(format string, args ...any)
}

// Run ticks until ctx is done.
func (j *IdleReaperJob) Run(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultIdleReaperInterval
	}
	return RunTick(ctx, interval, j.TickAndLog)
}

// TickAndLog runs one tick and writes its lines.
func (j *IdleReaperJob) TickAndLog(ctx context.Context) {
	result, err := j.Tick(ctx)
	if err != nil {
		j.logf(JobLine(IdleReaperJobName, "failed", "error", err.Error()))
		return
	}
	j.logf(JobLine(IdleReaperJobName, "tick",
		"closed", strconv.Itoa(len(result.Closed)),
		"skipped", strconv.Itoa(result.total()),
	))
}

// Tick runs one pass and reports what it did.
//
// One line per closed session, naming the id and the age that justified it: the
// id is what the acceptance criterion joins on, and the age is what proves the
// floor was applied rather than guessed at.
func (j *IdleReaperJob) Tick(ctx context.Context) (IdleReaperResult, error) {
	result := IdleReaperResult{Skipped: map[string]int{}}
	sessions, err := j.Supervisor.List(ctx)
	if err != nil {
		return result, errors.Wrap(ctx, err, "list sessions failed")
	}
	floor := j.floor()
	for _, session := range sessions {
		reason := idleReapSkipReason(session, floor)
		if reason != "" {
			result.Skipped[reason]++
			continue
		}
		if stopErr := j.Supervisor.Stop(ctx, session.ID); stopErr != nil {
			j.logf(JobLine(IdleReaperJobName, "stop-failed",
				"sid", session.ID, "error", stopErr.Error()))
			result.Skipped["stop-failed"]++
			continue
		}
		age := idleAge(session)
		j.logf(JobLine(IdleReaperJobName, "closed",
			"sid", session.ID,
			"idle", age.Round(time.Second).String(),
		))
		result.Closed = append(result.Closed, session.ID)
	}
	return result, nil
}

// idleReapSkipReason names why a session is not this job's population, or ""
// when it is.
//
// Every branch is named rather than collapsed into a bool, because the summary
// line is the only evidence a quiet tick has: `skipped=7` with no reasons would
// read exactly like a tick that was broken.
func idleReapSkipReason(session Session, floor time.Duration) string {
	// Only a headless child is reaped. An interactive child is a tab a person
	// is looking at; closing it on an idle timer would take the tab away, and
	// the operator's rule is about workers.
	if session.Mode != SpawnModeHeadless {
		return "not-headless"
	}
	// State is the trigger and is never derived from an age. A child inside a
	// long tool call is silent too, and it stays generating — reading its
	// silence as idle is the inversion this service exists to remove.
	if session.State != StateIdle {
		return "not-idle"
	}
	// A turn has been written but the child has not yet emitted message_start,
	// so it still reads idle while a turn is on its way. Closing here would cut
	// a turn that has not started.
	if session.PendingTurns > 0 {
		return "turn-pending"
	}
	if session.ExitCode != nil {
		return "exited"
	}
	// A record with no age cannot be aged. Guessing would close a healthy
	// child, so it is skipped instead.
	if session.LastEventAgeSeconds == nil {
		return "no-age"
	}
	if idleAge(session) < floor {
		return "under-floor"
	}
	return ""
}

// idleAge is how long the child's stream has been silent. It is only ever read
// for a session already known to be StateIdle — never used to decide the state.
func idleAge(session Session) time.Duration {
	if session.LastEventAgeSeconds == nil {
		return 0
	}
	return time.Duration(*session.LastEventAgeSeconds) * time.Second
}

func (j *IdleReaperJob) floor() time.Duration {
	if j.Floor > 0 {
		return j.Floor
	}
	return DefaultIdleFloor
}

// logf writes one rendered job line. It takes the finished string rather than a
// format and arguments because every call site passes a JobLine: a variadic
// signature there would be a format string with no format.
func (j *IdleReaperJob) logf(line string) {
	if j.Logf != nil {
		j.Logf("%s", line)
		return
	}
	log.Printf("%s", line)
}

func (r IdleReaperResult) total() int {
	total := len(r.Closed)
	for _, n := range r.Skipped {
		total += n
	}
	return total
}
