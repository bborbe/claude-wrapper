// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// ParkNudge is one nudge the script emitted.
type ParkNudge struct {
	// SessionID is the session the nudge is addressed to.
	SessionID string
	// Name is the roster name the session maps to. Empty when the roster does not
	// map it — a headless or pane-less session — which is reported as undelivered
	// rather than guessed at.
	Name string
	// Message is the text after the `::` separator, verbatim.
	Message string
}

// ParkNudgeProcedure sends the nudge that `park-nudge.py` selected.
//
// ⚠️ **It sends ONE nudge per tick, and it never decides who to nudge.** The
// script holds the selection, the suppression and the text — including the
// ledger keyed on session id AND `watching_since`, which is what makes a nudge
// once-per-episode rather than once-per-tick. A second selection here would
// bypass that ledger and nudge the same watcher every 15 minutes.
//
// ⚠️ **A failed send is `skipped reason=undelivered`, not an error.** The
// script's ledger already counts the attempt, so retrying would not re-select it;
// reporting it as an error would make one undeliverable watcher look like a
// broken tick.
type ParkNudgeProcedure struct {
	// Nudges reads the script's NUDGE lines.
	Nudges func(ctx context.Context) ([]ParkNudge, error)
	// Send delivers one nudge to the named SESSION, through the pane route
	// (`KeepalivePaneSender`/`SendWhenReady`) — a nudged watcher is a TAB, so
	// `Supervisor.Send` would refuse it. It takes the session id, not the roster
	// name, because the name→pane join is not the identity; the session id is.
	Send func(ctx context.Context, sessionID string, message string) error
}

// Name is the `action=` value this procedure writes.
func (p *ParkNudgeProcedure) Name() string { return "park-nudge" }

// Run sends at most one nudge.
func (p *ParkNudgeProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Nudges == nil {
		return KeepaliveEffect{}, errors.New(ctx, "park-nudge has no nudge source")
	}
	if p.Send == nil {
		return KeepaliveEffect{}, errors.New(ctx, "park-nudge has no send action")
	}
	nudges, err := p.Nudges(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read park nudges failed")
	}
	if len(nudges) == 0 {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	nudge := nudges[0]
	if nudge.SessionID == "" {
		return KeepaliveEffect{}, errors.New(ctx, "park nudge names no session")
	}
	// A missing pane (the pane sender's ErrKeepaliveNoPane) or a send failure is
	// `skipped reason=undelivered`, never an error — the script's ledger already
	// counts the attempt, so retrying would not re-select it.
	if sendErr := p.Send(ctx, nudge.SessionID, nudge.Message); sendErr != nil {
		return KeepaliveEffect{
			Result:    KeepaliveResultSkipped,
			SessionID: nudge.SessionID,
			Reason:    "undelivered",
		}, nil
	}
	return KeepaliveEffect{
		Result:    KeepaliveResultNudged,
		SessionID: nudge.SessionID,
	}, nil
}
