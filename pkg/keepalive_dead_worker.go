// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// DeadWorkerProcedure resumes a worker whose session is gone.
//
// It is the first of the eight keep-alive procedures because it is the one that
// needs nothing but the liveness verdict and the wrapper's own child table: no
// vault read, no park store, no script. If the verdict wiring is wrong, this
// procedure shows it before any of the others can mask it.
//
// ⚠️ **Liveness comes from the verdict, never from a pid probe.** `os.kill(pid,
// 0)` answers "is this pid alive", which is a different question from "is this
// session running" — the pid can be reused, and a session whose process is alive
// but whose loop died reads as healthy to a pid probe. The verdict already
// answers the right question, and every consumer that re-derived it disagreed
// with every other, which is what the liveness endpoint exists to stop.
type DeadWorkerProcedure struct {
	// Verdicts reads one verdict per session. In the daemon this is
	// LivenessVerdicts over the registry, the child table and the heartbeat store.
	Verdicts func(ctx context.Context) ([]LivenessVerdict, error)
	// Claim takes the resume claim for a session before this procedure acts.
	//
	// ⚠️ It is NOT optional and it is NOT a Go-side lock. While the model round
	// still runs its own keep-alive leg — every tick between this job going live
	// and the round's rows being removed — both actors see the same population,
	// and the claim is the only thing that stops a double resume. It reports
	// `false` when another writer holds it.
	Claim func(ctx context.Context, sessionID string) (bool, error)
	// Resume brings the session back.
	Resume KeepaliveResume
}

// KeepaliveResume brings one session back and reports the mechanism it used.
type KeepaliveResume func(ctx context.Context, sessionID string) error

// Name is the `action=` value this procedure writes.
func (p *DeadWorkerProcedure) Name() string { return "dead-worker" }

// Run resumes at most one dead worker per tick.
//
// ⚠️ **One per tick is deliberate.** A tick that resumed every dead session it
// found would fire the whole backlog at once after a daemon restart — dozens of
// spawns in one second, each loading a session, which is the load spike the
// 3-resumes-in-30-minutes cap exists to prevent. The next tick takes the next
// one, so the fleet recovers at a bounded rate.
func (p *DeadWorkerProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Verdicts == nil {
		return KeepaliveEffect{}, errors.New(ctx, "dead-worker has no verdict source")
	}
	if p.Claim == nil {
		return KeepaliveEffect{}, errors.New(ctx, "dead-worker has no claim source")
	}
	if p.Resume == nil {
		return KeepaliveEffect{}, errors.New(ctx, "dead-worker has no resume action")
	}
	verdicts, err := p.Verdicts(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read liveness verdicts failed")
	}
	for _, verdict := range verdicts {
		if verdict.Liveness == LivenessLive {
			continue
		}
		if verdict.SessionID == "" {
			continue
		}
		claimed, claimErr := p.Claim(ctx, verdict.SessionID)
		if claimErr != nil {
			return KeepaliveEffect{}, errors.Wrapf(ctx, claimErr, "claim %s failed", verdict.SessionID)
		}
		if !claimed {
			// Another writer is already resuming this one. The script's word for
			// THAT writer is `claimed`; `claim-taken` means the opposite — that
			// this writer won the claim — so reporting it here would invert the
			// one vocabulary the round and the tick share.
			return KeepaliveEffect{
				Result:    KeepaliveResultSkipped,
				SessionID: verdict.SessionID,
				Reason:    "claimed",
			}, nil
		}
		if resumeErr := p.Resume(ctx, verdict.SessionID); resumeErr != nil {
			return KeepaliveEffect{}, errors.Wrapf(ctx, resumeErr, "resume %s failed", verdict.SessionID)
		}
		return KeepaliveEffect{
			Result:    KeepaliveResultResumed,
			SessionID: verdict.SessionID,
		}, nil
	}
	return KeepaliveEffect{Result: KeepaliveResultNone}, nil
}
