// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// WakeMiss is one subject whose last actionable wake never landed.
type WakeMiss struct {
	// Subject is the goal or topic whose manager was never woken.
	Subject string
	// SessionID is the session the ledger recorded, when it recorded one.
	SessionID string
	// Result is the gate's wake outcome — `NO-MANAGER`, `NO-PANE`, `HEADLESS`, or
	// a failed wake run.
	Result string
	// Live is the ledger's own `live=` column: yes, no, or unknown.
	Live string
}

// WakeMissProcedure REPORTS the subjects a session-based check never enumerated.
//
// ⚠️ **It does not resume anything, and that is the source's own instruction.**
// `agents/fleet-keepalive-actor.md` § 4c reads *"Run
// `<plugin root>/commands/steps/wake-misses.md`; report only."*, and the step's
// own table gives the keep-alive leg a `wake-miss:` LINE, not an act. A port that
// resumed here would be the one procedure in this tick that acts on a reading the
// source calls a report.
//
// The finding is `MISS`, never `STALE`: a stale subject may simply have stopped
// producing actionable ticks, and the step sends that one to fleet-triage, not to
// this leg.
type WakeMissProcedure struct {
	// Misses reads the script's findings.
	Misses func(ctx context.Context) ([]WakeMiss, error)
}

// Name is the `action=` value this procedure writes.
func (p *WakeMissProcedure) Name() string { return "wake-miss" }

// Run reports the first miss, so the line names a subject rather than a count.
func (p *WakeMissProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Misses == nil {
		return KeepaliveEffect{}, errors.New(ctx, "wake-miss has no finding source")
	}
	misses, err := p.Misses(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read wake misses failed")
	}
	if len(misses) == 0 {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	miss := misses[0]
	if miss.SessionID == "" {
		// A miss with no session is still a miss — the subject had no manager at
		// all — but it cannot be joined to a session line, so the reason carries
		// the subject instead.
		return KeepaliveEffect{
			Result: KeepaliveResultKept,
			Reason: "subject=" + miss.Subject,
		}, nil
	}
	return KeepaliveEffect{
		Result:    KeepaliveResultKept,
		SessionID: miss.SessionID,
		Reason:    "wake=" + miss.Result + " live=" + miss.Live,
	}, nil
}
