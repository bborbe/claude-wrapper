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
	// Vault is the subject's vault. The open surface opens a row BY NAME, so a
	// resume carries the vault the name lives in — a decision that drops it cannot
	// resume.
	Vault string
	// Name is the subject's page name (topic, goal or task title). The resume act
	// opens this name through the open surface.
	Name string
	// Branch is the tier the subject belongs to (`topic` / `goal` / anything else
	// is the worker task tier). The resume act derives the open row's Kind from it.
	Branch string
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
	// Hold is the hold check, run BEFORE the claim (hold → claim → act). A held
	// session reports `skipped reason=hold`, one whose heldness is unknown reports
	// `skipped reason=hold-unknown` — distinct, so an unreadable hold store is
	// visible. nil means no check (a wiring that does not set it skips this leg);
	// the manager tiers 1 and 2 MUST set it.
	Hold KeepaliveHold
	// Pending is where a successful `resume` enqueues its later-tick record. nil means
	// no enqueue; the manager tiers MUST set it. A `restart` enqueues nothing — the
	// session is live and already recorded.
	Pending PendingRecordStore
	// Now supplies the PendingRecord's CreatedAt. Required when Pending is set.
	Now libtime.CurrentDateTimeGetter
	// Resume brings a session back where it is (`resume`), or restarts it in
	// place (`restart`). The verdict picks which; the caller supplies both. It
	// receives the whole decision so the resume path has the subject it opens by.
	Act func(ctx context.Context, verdict string, decision LapsedManagerDecision) error
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
		return p.performAct(ctx, decision)
	}
	return effect, nil
}

// performAct runs the act-verdict path: hold → claim → act, returning the effect.
// `claim-taken` carries no act of its own — the probe already decided — so it is
// treated as `resume`.
func (p *LapsedManagerProcedure) performAct(
	ctx context.Context,
	decision LapsedManagerDecision,
) (KeepaliveEffect, error) {
	if decision.SessionID == "" {
		return KeepaliveEffect{}, errors.Errorf(ctx, "verdict %q names no session", decision.Verdict)
	}
	// hold → claim → act: a held (or unknown) session is skipped before the claim is
	// taken, so the claim is never spent on a session the tick must leave alone.
	holdEffect, held, holdErr := p.holdSkip(ctx, decision.SessionID)
	if holdErr != nil {
		return KeepaliveEffect{}, holdErr
	}
	if held {
		return holdEffect, nil
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
	actVerdict := decision.Verdict
	if actVerdict == "claim-taken" {
		actVerdict = "resume"
	}
	if actErr := p.Act(ctx, actVerdict, decision); actErr != nil {
		return KeepaliveEffect{}, errors.Wrapf(ctx, actErr, "act %s on %s failed", actVerdict, decision.SessionID)
	}
	// A `resume` enqueues a later-tick record (the session reads STARTING now); a
	// `restart` re-drives a live session and enqueues nothing.
	if actVerdict == "resume" && p.Pending != nil {
		record := PendingRecord{
			SessionID: decision.SessionID,
			Kind:      "manager",
			CreatedAt: p.now(),
		}
		if addErr := p.Pending.Add(ctx, record); addErr != nil {
			return KeepaliveEffect{}, errors.Wrapf(ctx, addErr, "enqueue pending record %s failed", decision.SessionID)
		}
	}
	result := KeepaliveResultResumed
	if actVerdict == "restart" {
		result = KeepaliveResultRestarted
	}
	return KeepaliveEffect{Result: result, SessionID: decision.SessionID}, nil
}

// holdSkip reports whether the session is held (or its heldness unknown), and if
// so the skip effect to return. A nil Hold means no check (not held).
func (p *LapsedManagerProcedure) holdSkip(
	ctx context.Context,
	sessionID string,
) (KeepaliveEffect, bool, error) {
	if p.Hold == nil {
		return KeepaliveEffect{}, false, nil
	}
	state, err := p.Hold(ctx, sessionID)
	if err != nil {
		return KeepaliveEffect{}, false, errors.Wrapf(ctx, err, "hold check %s failed", sessionID)
	}
	switch state {
	case HoldHeld:
		return KeepaliveEffect{
			Result: KeepaliveResultSkipped, SessionID: sessionID, Reason: "hold",
		}, true, nil
	case HoldUnknown:
		return KeepaliveEffect{
			Result: KeepaliveResultSkipped, SessionID: sessionID, Reason: "hold-unknown",
		}, true, nil
	default:
		return KeepaliveEffect{}, false, nil
	}
}

// now returns the clock's time, or wall-clock when no clock is wired.
func (p *LapsedManagerProcedure) now() time.Time {
	if p.Now != nil {
		return p.Now.Now().Time()
	}
	return time.Now()
}
