// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// DecisionParkedWorkerProcedure resumes a worker whose parked wait has cleared.
//
// The script (`decision-park-resume.py`) decides and RECORDS: it carries the
// resume cap (`spent()` / `RESUME_CAP`, reported as `crash-loop`), it records
// each resume through its own `--record <task> --sid <session-id>`, and it
// persists its counts beside the round's digest. This procedure therefore takes
// NO claim — the script's own record is the de-duplication, and a second claim
// store would be a second owner of the same fact.
//
// A row's shape is `{task, sid, verdict, kind, detail}`; this procedure acts on
// one `resume` row per tick, for the same reason every other procedure acts on
// one: a cleared backlog must not fire at once.
type DecisionParkedWorkerProcedure struct {
	// Decide runs the script and returns the row it wants acted on.
	Decide func(ctx context.Context) (LapsedManagerDecision, error)
	// Act resumes the session. The verdict is passed through so the act can
	// distinguish a cleared approval (`a`) from a cleared `blocked_by` (`d`) if it
	// ever needs to.
	Act func(ctx context.Context, verdict string, sessionID string) error
}

// Name is the `action=` value this procedure writes.
func (p *DecisionParkedWorkerProcedure) Name() string { return "decision-parked-worker" }

// Run acts on the one row the script marked `resume`.
func (p *DecisionParkedWorkerProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Decide == nil {
		return KeepaliveEffect{}, errors.New(ctx, "decision-parked-worker has no decision source")
	}
	if p.Act == nil {
		return KeepaliveEffect{}, errors.New(ctx, "decision-parked-worker has no act")
	}
	decision, err := p.Decide(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read decision-park verdict failed")
	}
	effect, err := KeepaliveEffectForVerdict(ctx, decision.Verdict)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrapf(ctx, err, "verdict %q", decision.Verdict)
	}
	effect.SessionID = decision.SessionID
	if decision.Verdict != "resume" {
		// `crash-loop` is the script's spent cap and maps to `capped`; every other
		// word is a non-act. None of them may reach the act.
		return effect, nil
	}
	if decision.SessionID == "" {
		return KeepaliveEffect{}, errors.New(ctx, "resume row names no session")
	}
	if actErr := p.Act(ctx, decision.Verdict, decision.SessionID); actErr != nil {
		return KeepaliveEffect{}, errors.Wrapf(ctx, actErr, "resume %s failed", decision.SessionID)
	}
	effect.Result = KeepaliveResultResumed
	return effect, nil
}
