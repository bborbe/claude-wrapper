// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
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
	// Now is the clock the gap check reads. Nil means time.Now.
	Now NowFunc
}

// Run ticks until ctx is done.
func (j *IdleReaperJob) Run(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultIdleReaperInterval
	}
	return RunTickWithGap(ctx, interval, j.Now, j.reportLate, j.TickAndLog)
}

// reportLate names a tick that began later than its interval.
//
// The gap is what was measured and `since` is the tick it is measured from, so
// a reader can tell a suspended daemon from a tick that blocked on its own
// work without cross-referencing the log's timestamps.
func (j *IdleReaperJob) reportLate(gap time.Duration, since time.Time) {
	j.logf(JobLine(IdleReaperJobName, "tick-late",
		"gap", gap.Round(time.Second).String(),
		"since", since.UTC().Format(time.RFC3339),
	))
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
		"skipped", strconv.Itoa(result.skippedCount()),
		"reasons", result.skippedBreakdown(),
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
		// ⚠️ The list above is a snapshot. Between it and Stop the child can start a
		// turn, and Stop is unconditional, so re-reading here is what keeps the
		// window from being the whole tick. It is narrowed, not closed: a turn that
		// begins between this read and Stop is still cut, because the Supervisor
		// interface offers no conditional stop.
		if !j.stillReapable(ctx, session.ID, floor) {
			result.Skipped["changed-since-list"]++
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

// stillReapable re-reads one session and re-applies the floor.
//
// An error reads as "not reapable": failing to read a session is not evidence
// that closing it is safe.
func (j *IdleReaperJob) stillReapable(
	ctx context.Context, id string, floor time.Duration,
) bool {
	session, err := j.Supervisor.Get(ctx, id)
	if err != nil || session == nil {
		return false
	}
	return idleReapSkipReason(*session, floor) == ""
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
	// Defensive, and kept deliberately. reap() sets State and ExitCode together, so
	// today this is unreachable — but it is the guard that stops a future path
	// setting an exit code without the state from making a dead child look
	// reapable, and Stop on a live-looking session is the expensive mistake.
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

// skippedBreakdown renders the per-reason counts in a stable order.
//
// ⚠️ A total alone is not legible: `skipped=7` reads exactly like a tick that was
// broken, which is the reading this job exists to make impossible. The reasons are
// sorted so two ticks with the same state produce the same line.
func (r IdleReaperResult) skippedBreakdown() string {
	if len(r.Skipped) == 0 {
		return "none"
	}
	reasons := make([]string, 0, len(r.Skipped))
	for reason := range r.Skipped {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, reason+":"+strconv.Itoa(r.Skipped[reason]))
	}
	return strings.Join(parts, ",")
}

// skippedCount is the sessions this tick did NOT close. It deliberately excludes
// Closed: a summary that counted closed sessions as skipped read as though the
// tick had ignored work it had actually done.
func (r IdleReaperResult) skippedCount() int {
	total := 0
	for _, n := range r.Skipped {
		total += n
	}
	return total
}
