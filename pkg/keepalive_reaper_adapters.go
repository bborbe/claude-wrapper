// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// IdleCloseReaper closes headless children that have rested idle past the floor —
// the existing `IdleReaperJob.Tick`, adapted UNCHANGED. The merge is an adapter,
// not a rewrite: the idle logic (the floor, the skip reasons, the Stop) stays in
// IdleReaperJob; this only re-labels its result as a `ReaperResult`.
type IdleCloseReaper struct {
	Job *IdleReaperJob
}

// Kind is the `action=` value.
func (r *IdleCloseReaper) Kind() string { return "idle-close" }

// Reap runs the idle tick and reports one closed result per stopped session.
func (r *IdleCloseReaper) Reap(ctx context.Context) ([]ReaperResult, error) {
	result, err := r.Job.Tick(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]ReaperResult, 0, len(result.Closed))
	for _, id := range result.Closed {
		results = append(results, ReaperResult{Kind: r.Kind(), SessionID: id, Closed: true})
	}
	return results, nil
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
