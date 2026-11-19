// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// LapsedManagerDecision is what `topic-keepalive.py` answered for one subject:
// the verdict token on its first stdout line, and the session the goal page
// records.
type LapsedManagerDecision struct {
	// Verdict is the script's own word — `resume`, `restart`, `leave`, `jump`,
	// `crash-loop`, `claimed`, `claim-taken`, `cannot-tell`, `open`,
	// `open-unflagged`.
	Verdict string
	// SessionID is the subject's recorded `claude_session_id`. Empty when the
	// page records none, which is exactly the `open` / `open-unflagged` case.
	SessionID string
}

// LapsedManagerProcedure keeps one topic's goal manager alive.
//
// It decides nothing itself: `topic-keepalive.py` holds both signals (is a
// session there, and is its loop armed) and prints the verdict. This procedure
// runs the script, maps its word with KeepaliveEffectForVerdict, and performs the
// act the word licenses — nothing more. A second implementation of "is this
// manager lapsed" would drift from the one the operator's rulings were made
// against.
type LapsedManagerProcedure struct {
	// Action is the `action=` value this procedure writes. The topic tier and the
	// topicless-goal tier share this type and differ only in their decision
	// source, so the name is injected rather than hard-coded.
	Action string
	// Decide runs the script and reads its verdict.
	Decide func(ctx context.Context) (LapsedManagerDecision, error)
	// Claim takes the session's resume claim. ⚠️ Not optional: the model round
	// still runs its own keep-alive leg until the round's rows are removed, and
	// the claim is the only thing that stops two writers resuming one session.
	Claim func(ctx context.Context, sessionID string) (bool, error)
	// Resume brings a session back where it is (`resume`), or restarts it in
	// place (`restart`). The verdict picks which; the caller supplies both.
	Act func(ctx context.Context, verdict string, sessionID string) error
}

// Name is the `action=` value this procedure writes.
func (p *LapsedManagerProcedure) Name() string {
	if p.Action == "" {
		return "lapsed-topic-manager"
	}
	return p.Action
}

// Run runs the decision and acts on its verdict.
func (p *LapsedManagerProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Decide == nil {
		return KeepaliveEffect{}, errors.New(ctx, "lapsed-manager has no decision source")
	}
	if p.Claim == nil {
		return KeepaliveEffect{}, errors.New(ctx, "lapsed-manager has no claim source")
	}
	if p.Act == nil {
		return KeepaliveEffect{}, errors.New(ctx, "lapsed-manager has no act")
	}
	decision, err := p.Decide(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read keepalive verdict failed")
	}
	effect, err := KeepaliveEffectForVerdict(ctx, decision.Verdict)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrapf(ctx, err, "verdict %q", decision.Verdict)
	}
	effect.SessionID = decision.SessionID

	switch decision.Verdict {
	case "resume", "restart", "claim-taken":
		// The three words that license an act. `claim-taken` means THIS writer won
		// the claim, so the act follows here rather than being reported as a skip.
		if decision.SessionID == "" {
			return KeepaliveEffect{}, errors.Errorf(ctx, "verdict %q names no session", decision.Verdict)
		}
		claimed, claimErr := p.Claim(ctx, decision.SessionID)
		if claimErr != nil {
			return KeepaliveEffect{}, errors.Wrapf(ctx, claimErr, "claim %s failed", decision.SessionID)
		}
		if !claimed {
			// The script's word for the writer that LOST the claim is `claimed`;
			// `claim-taken` means this writer won it.
			return KeepaliveEffect{
				Result:    KeepaliveResultSkipped,
				SessionID: decision.SessionID,
				Reason:    "claimed",
			}, nil
		}
		// `claim-taken` carries no act of its own — the probe already decided what
		// to do, so it is treated as `resume` when the caller has not said
		// otherwise. The act reports nothing; the verdict names the effect.
		actVerdict := decision.Verdict
		if actVerdict == "claim-taken" {
			actVerdict = "resume"
		}
		if actErr := p.Act(ctx, actVerdict, decision.SessionID); actErr != nil {
			return KeepaliveEffect{}, errors.Wrapf(ctx, actErr, "act %s on %s failed", actVerdict, decision.SessionID)
		}
		if actVerdict == "restart" {
			effect.Result = KeepaliveResultRestarted
		} else {
			effect.Result = KeepaliveResultResumed
		}
	}
	return effect, nil
}
