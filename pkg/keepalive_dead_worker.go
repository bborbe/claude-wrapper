// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// DeadWorkerDecision is one `worker-keepalive.py` verdict for one task page.
// The script decides per TASK (`--vault <vault> --task <task>`), not per session
// on a liveness list: its first stdout line is one verdict word — `jump` /
// `resume` / `crash-loop` / `cannot-tell` — and the session to resume is the
// page's own `claude_session_id`, which the caller reads.
type DeadWorkerDecision struct {
	// Vault is the task page's vault.
	Vault string
	// Task is the task page's name — the resume act opens the worker BY TASK.
	Task string
	// SessionID is the page's `claude_session_id`. Empty when the page records
	// none — there is then no session to resume and no `id=<sid>` to log.
	SessionID string
	// Verdict is the script's word for this task.
	Verdict string
}

// DeadWorkerProcedure resumes a worker whose session is gone.
//
// ⚠️ **The decision is the SCRIPT's, not a liveness re-derivation.**
// `worker-keepalive.py --vault <vault> --task <task>` carries both signals the
// act needs — is the session there, and has its resume budget been spent — and
// prints the verdict word. Reading the liveness list alone (the superseded shape)
// could not arm the script's cycle cap, so a worker would be resumed past its
// budget forever.
type DeadWorkerProcedure struct {
	// Decisions runs `worker-keepalive.py` per task page and returns one decision
	// each. The population and the script run live in the caller; this procedure
	// only maps the words and acts.
	Decisions func(ctx context.Context) ([]DeadWorkerDecision, error)
	// Claim takes the resume claim for a session before this procedure acts.
	//
	// ⚠️ It is NOT optional and it is NOT a Go-side lock. While the model round
	// still runs its own keep-alive leg — every tick between this job going live
	// and the round's rows being removed — both actors see the same population,
	// and the claim is the only thing that stops a double resume. It reports
	// `false` when another writer holds it. The worker tier's claim comes from
	// `topic-keepalive.py --claim-resume` keyed on the WORKER session id (the
	// script has no `--claim-resume` of its own); whether that is valid for a
	// worker id is the parked `# Open Questions` exposure, not a reason to drop it.
	Claim func(ctx context.Context, sessionID string) (bool, error)
	// Hold is the hold check, run BEFORE the claim (hold → claim → act). A held
	// candidate reports `skipped reason=hold`, an unknown one `skipped
	// reason=hold-unknown`; either is skipped and the scan continues to the next.
	// nil means no check; the wiring MUST set it for this procedure.
	Hold KeepaliveHold
	// Pending is where a successful `resume` enqueues its later-tick record (the
	// worker tier records via `worker-keepalive.py --vault --task --record-resume`,
	// so the record carries Vault and Task). nil means no enqueue; the wiring MUST
	// set it for this procedure.
	Pending PendingRecordStore
	// Now supplies the PendingRecord's CreatedAt. Required when Pending is set.
	Now libtime.CurrentDateTimeGetter
	// Act performs the verdict's act. A `resume` opens the worker by task through
	// the open surface (see NewKeepaliveAct).
	Act func(ctx context.Context, verdict string, decision LapsedManagerDecision) error
}

// Name is the `action=` value this procedure writes.
func (p *DeadWorkerProcedure) Name() string { return "dead-worker" }

// Run acts on at most one dead worker per tick.
//
// ⚠️ **One act per tick is deliberate.** A tick that resumed every dead session
// it found would fire the whole backlog at once after a daemon restart — dozens
// of spawns in one second, each loading a session, which is the load spike the
// 3-resumes-in-30-minutes cap exists to prevent. The next tick takes the next
// one, so the fleet recovers at a bounded rate. When nothing licenses an act, the
// FIRST decision's own effect is reported so a `crash-loop` (capped), `jump`
// (kept) or `cannot-tell` (skipped) is visible rather than a bare `none`.
func (p *DeadWorkerProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Decisions == nil {
		return KeepaliveEffect{}, errors.New(ctx, "dead-worker has no decision source")
	}
	if p.Claim == nil {
		return KeepaliveEffect{}, errors.New(ctx, "dead-worker has no claim source")
	}
	if p.Act == nil {
		return KeepaliveEffect{}, errors.New(ctx, "dead-worker has no act")
	}
	decisions, err := p.Decisions(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read worker-keepalive decisions failed")
	}
	if len(decisions) == 0 {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	// ⚠️ **No head-of-line blocking.** A lost claim (or a session-less resume row)
	// must NOT stop the scan: one already-claimed worker at the front of the list
	// would otherwise block every other dead worker forever. The loop CONTINUES
	// past a skip and only acts on the first candidate it actually wins.
	var firstSkip *KeepaliveEffect
	for _, decision := range decisions {
		acted, skip, actErr := p.tryResume(ctx, decision)
		if actErr != nil {
			return KeepaliveEffect{}, actErr
		}
		if acted {
			return KeepaliveEffect{
				Result:    KeepaliveResultResumed,
				SessionID: decision.SessionID,
			}, nil
		}
		if skip != nil && firstSkip == nil {
			firstSkip = skip
		}
	}
	// Nothing was acted on. Report the FIRST non-kept skip (hold / hold-unknown /
	// claimed); otherwise the first decision's own effect, so a `capped` or
	// `cannot-tell` is visible rather than a bare `none`.
	if firstSkip != nil {
		return *firstSkip, nil
	}
	effect, err := KeepaliveEffectForVerdict(ctx, decisions[0].Verdict)
	if err != nil {
		return KeepaliveEffect{}, err
	}
	effect.SessionID = decisions[0].SessionID
	return effect, nil
}

// tryResume runs the act path for one decision: hold → claim → act. It returns
// acted=true when it resumed; a non-nil skip effect when the candidate was skipped
// (held / unknown / claimed) so the caller reports the first one; and err on a
// hold, claim or act failure. A non-resume row or one naming no session returns
// (false, nil, nil) — nothing to do, keep scanning.
func (p *DeadWorkerProcedure) tryResume(
	ctx context.Context,
	decision DeadWorkerDecision,
) (bool, *KeepaliveEffect, error) {
	if decision.Verdict != "resume" || decision.SessionID == "" {
		return false, nil, nil
	}
	// hold → claim → act: a held (or unknown) candidate is skipped before the claim.
	skip, holdErr := p.holdSkip(ctx, decision)
	if holdErr != nil {
		return false, nil, holdErr
	}
	if skip != nil {
		return false, skip, nil
	}
	claimed, claimErr := p.Claim(ctx, decision.SessionID)
	if claimErr != nil {
		return false, nil, errors.Wrapf(ctx, claimErr, "claim %s failed", decision.SessionID)
	}
	if !claimed {
		return false, &KeepaliveEffect{
			Result: KeepaliveResultSkipped, SessionID: decision.SessionID, Reason: "claimed",
		}, nil
	}
	if actErr := p.Act(ctx, "resume", LapsedManagerDecision{
		Vault:     decision.Vault,
		Name:      decision.Task,
		Branch:    "", // the worker tier opens OpenKindTask
		SessionID: decision.SessionID,
	}); actErr != nil {
		return false, nil, errors.Wrapf(ctx, actErr, "resume %s failed", decision.SessionID)
	}
	// The worker tier enqueues a later-tick record carrying Vault + Task, so the
	// drain records through `worker-keepalive.py --vault --task --record-resume`.
	if p.Pending != nil {
		record := PendingRecord{
			SessionID: decision.SessionID,
			Vault:     decision.Vault,
			Task:      decision.Task,
			Kind:      "worker",
			CreatedAt: p.now(),
		}
		if addErr := p.Pending.Add(ctx, record); addErr != nil {
			return false, nil, errors.Wrapf(ctx, addErr, "enqueue pending record %s failed", decision.SessionID)
		}
	}
	return true, nil, nil
}

// now returns the clock's time, or wall-clock when no clock is wired.
func (p *DeadWorkerProcedure) now() time.Time {
	if p.Now != nil {
		return p.Now.Now().Time()
	}
	return time.Now()
}

// holdSkip returns a non-nil skip effect when the candidate is held or its
// heldness is unknown, and nil when it may be acted on (or no Hold is wired).
func (p *DeadWorkerProcedure) holdSkip(
	ctx context.Context,
	decision DeadWorkerDecision,
) (*KeepaliveEffect, error) {
	if p.Hold == nil {
		return nil, nil
	}
	state, err := p.Hold(ctx, decision.SessionID)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "hold check %s failed", decision.SessionID)
	}
	switch state {
	case HoldHeld:
		return &KeepaliveEffect{
			Result: KeepaliveResultSkipped, SessionID: decision.SessionID, Reason: "hold",
		}, nil
	case HoldUnknown:
		return &KeepaliveEffect{
			Result: KeepaliveResultSkipped, SessionID: decision.SessionID, Reason: "hold-unknown",
		}, nil
	default:
		return nil, nil
	}
}
