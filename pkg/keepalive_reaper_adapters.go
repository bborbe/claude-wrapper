// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"strconv"

	"github.com/bborbe/errors"
)

// IdleTicker is the one method IdleCloseReaper needs from IdleReaperJob. Taking
// the interface (which *IdleReaperJob satisfies unchanged) is what makes the
// per-kind `idle-close` fixture possible without a fully-populated Session fake —
// the fixture stubs the tick instead of driving the idle decision.
type IdleTicker interface {
	Tick(ctx context.Context) (IdleReaperResult, error)
}

// IdleCloseReaper closes headless children that have rested idle past the floor —
// the existing `IdleReaperJob.Tick`, adapted UNCHANGED. The merge is an adapter,
// not a rewrite: the idle logic (the floor, the skip reasons, the Stop) stays in
// IdleReaperJob; this only re-labels its result as a `ReaperResult`.
//
// ⚠️ The line shapes are documented here because this adapter is the only place
// that produces them, and a quiet tick has no other evidence:
//
//	job=idle-reaper action=tick closed=<n> skipped=<n> reasons=<reason>:<n>,…
//	job=reaper action=idle-close result=closed id=<sid>
//	job=reaper action=idle-close result=kept id=<sid> reason=<reason>
//
// The `action=tick` summary is written by this adapter (not by ReaperJob) because
// it is the idle job's own shape and the other four reapers have no equivalent.
// A per-session `kept` line is written only for a NOTABLE skip — headless, still
// alive, at or past the floor — i.e. a child that should have been closed and was
// not; every other skip appears in the summary counts only.
type IdleCloseReaper struct {
	Job IdleTicker
	// Logf writes the tick summary line. Nil means log.Printf.
	Logf func(line string)
}

// Kind is the `action=` value.
func (r *IdleCloseReaper) Kind() string { return "idle-close" }

// Reap runs the idle tick, writes its summary line, and reports one result per
// closed session plus one `kept` result per notable skipped session.
func (r *IdleCloseReaper) Reap(ctx context.Context) ([]ReaperResult, error) {
	result, err := r.Job.Tick(ctx)
	if err != nil {
		return nil, err
	}
	r.logf(JobLine(IdleReaperJobName, "tick",
		"closed", strconv.Itoa(len(result.Closed)),
		"skipped", strconv.Itoa(result.skippedCount()),
		"reasons", result.skippedBreakdown(),
	))
	results := make([]ReaperResult, 0, len(result.Closed)+len(result.SkippedSessions))
	for _, id := range result.Closed {
		results = append(results, ReaperResult{Kind: r.Kind(), SessionID: id, Closed: true})
	}
	for _, skip := range result.SkippedSessions {
		if !skip.Notable {
			continue
		}
		results = append(results, ReaperResult{
			Kind: r.Kind(), SessionID: skip.SessionID, Reason: skip.Reason,
		})
	}
	return results, nil
}

// logf writes one rendered line. Nil Logf means log.Printf, like every other job.
func (r *IdleCloseReaper) logf(line string) {
	if r.Logf != nil {
		r.Logf(line)
		return
	}
	log.Printf("%s", line)
}

// TerminalTaskReaper closes a session whose anchored task is completed or aborted
// — the existing `terminalTaskDecision`, adapted. It runs from the TICK (scans
// live sessions), not a turn-end hook.
type TerminalTaskReaper struct {
	Supervisor Supervisor
	Resolver   AnchoredTaskResolver
}

// Kind is the `action=` value.
func (r *TerminalTaskReaper) Kind() string { return "terminal-task" }

// Reap scans the sessions, resolves each anchored task, and closes on a terminal
// decision. A `notable` non-close is reported `kept` with its reason.
func (r *TerminalTaskReaper) Reap(ctx context.Context) ([]ReaperResult, error) {
	sessions, err := r.Supervisor.List(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list sessions failed")
	}
	results := []ReaperResult{}
	for _, session := range sessions {
		status, resolveErr := r.Resolver.Resolve(ctx, session.ID)
		if resolveErr != nil {
			continue
		}
		closeChild, reason, notable := terminalTaskDecision(status)
		if !notable {
			continue
		}
		if closeChild {
			if stopErr := r.Supervisor.Stop(ctx, session.ID); stopErr != nil {
				continue
			}
			results = append(results, ReaperResult{Kind: r.Kind(), SessionID: session.ID, Closed: true})
			continue
		}
		results = append(results, ReaperResult{Kind: r.Kind(), SessionID: session.ID, Reason: reason})
	}
	return results, nil
}

// StoodDownCloseReaper closes a session whose stood-down marker exists and whose
// recorded session still reads live — the existing `StoodDownSessionsToClose`
// join, adapted. It RE-READS the marker store itself (the keepalive tick decides,
// this closes; no in-process handoff).
type StoodDownCloseReaper struct {
	Supervisor Supervisor
	Candidates func(ctx context.Context) ([]StoodDownCandidate, error)
	SessionFor func(ctx context.Context, base string) (string, error)
	Live       func(ctx context.Context, sessionID string) (bool, error)
}

// Kind is the `action=` value.
func (r *StoodDownCloseReaper) Kind() string { return "stood-down-close" }

// Reap resolves the stood-down candidates and closes each live one.
func (r *StoodDownCloseReaper) Reap(ctx context.Context) ([]ReaperResult, error) {
	candidates, err := r.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	toClose, err := StoodDownSessionsToClose(ctx, candidates, r.SessionFor, r.Live)
	if err != nil {
		return nil, err
	}
	results := make([]ReaperResult, 0, len(toClose))
	for _, sessionID := range toClose {
		if stopErr := r.Supervisor.Stop(ctx, sessionID); stopErr != nil {
			continue
		}
		results = append(results, ReaperResult{Kind: r.Kind(), SessionID: sessionID, Closed: true})
	}
	return results, nil
}

// RoleFor resolves the role (`manager` / `worker`) a session ran under — the split
// that keeps a session under exactly ONE reap kind (manager → manager-reap, worker
// → drive-reap).
type RoleFor func(ctx context.Context, sessionID string) (string, error)

// livenessReaper closes a not-live child-table session whose role matches.
// manager-reap and drive-reap are the same shape, differing only in the role
// filter — so this one implementation serves both.
type livenessReaper struct {
	kind       string
	want       func(role string) bool
	verdicts   func(ctx context.Context) ([]LivenessVerdict, error)
	role       RoleFor
	supervisor Supervisor
}

// Kind is the `action=` value.
func (r *livenessReaper) Kind() string { return r.kind }

// Reap closes each `not-live` child-table session whose role matches. A `live`
// row is NEVER closed however old; a non-child-table not-live is not a close
// candidate (it may be a session this daemon cannot reach).
func (r *livenessReaper) Reap(ctx context.Context) ([]ReaperResult, error) {
	verdicts, err := r.verdicts(ctx)
	if err != nil {
		return nil, err
	}
	results := []ReaperResult{}
	for _, verdict := range verdicts {
		if verdict.Liveness == LivenessLive || verdict.Source != "child-table" {
			continue
		}
		role, roleErr := r.role(ctx, verdict.SessionID)
		if roleErr != nil || !r.want(role) {
			continue
		}
		if stopErr := r.supervisor.Stop(ctx, verdict.SessionID); stopErr != nil {
			continue
		}
		results = append(results, ReaperResult{Kind: r.kind, SessionID: verdict.SessionID, Closed: true})
	}
	return results, nil
}

// NewManagerReapReaper reaps a not-live child-table session whose role is
// `manager` (a topic or goal manager).
func NewManagerReapReaper(
	verdicts func(ctx context.Context) ([]LivenessVerdict, error),
	role RoleFor,
	supervisor Supervisor,
) Reaper {
	return &livenessReaper{
		kind:       "manager-reap",
		want:       func(r string) bool { return r == "manager" },
		verdicts:   verdicts,
		role:       role,
		supervisor: supervisor,
	}
}

// NewDriveReapReaper reaps a not-live child-table session whose role is `worker`
// (a drive worker). The nudge (FleetPusher) fires first; this closes on a later tick.
func NewDriveReapReaper(
	verdicts func(ctx context.Context) ([]LivenessVerdict, error),
	role RoleFor,
	supervisor Supervisor,
) Reaper {
	return &livenessReaper{
		kind:       "drive-reap",
		want:       func(r string) bool { return r == "worker" },
		verdicts:   verdicts,
		role:       role,
		supervisor: supervisor,
	}
}
