// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// StoodDownCloseProcedure DECIDES whether a session was deliberately stood down.
//
// ⚠️ **It does not close anything.** `stood-down-close` is one act split across
// two jobs: this procedure reads the marker and reports `kept`, and the
// `ReaperJob`'s `stood-down-close` kind performs the close. The split is why the
// name appears in both tables, and a worker must not "resolve" that duplication
// by deleting one half — the tick cannot close (closing is the reaper's job) and
// the reaper cannot decide (deciding needs the marker read this procedure
// already performs).
//
// ⚠️ **`kept` is the correct word for a stood-down session, not `closed`.** A
// `closed` here would claim the tick closed it, and the reaper's own line would
// then be the second close of one session.
type StoodDownCloseProcedure struct {
	// Marker reports the session the stood-down marker names, and whether the
	// marker is present. An absent marker is a quiet tick, not an error.
	Marker func(ctx context.Context) (sessionID string, stoodDown bool, err error)
}

// Name is the `action=` value this procedure writes.
func (p *StoodDownCloseProcedure) Name() string { return "stood-down-close" }

// Run reports whether a session is stood down, and hands the close to the reaper.
func (p *StoodDownCloseProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Marker == nil {
		return KeepaliveEffect{}, errors.New(ctx, "stood-down-close has no marker source")
	}
	sessionID, stoodDown, err := p.Marker(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read stood-down marker failed")
	}
	if !stoodDown {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	if sessionID == "" {
		// A marker naming no session cannot be joined to the reaper's close, so it
		// is a defect in the marker rather than a quiet tick.
		return KeepaliveEffect{}, errors.New(ctx, "stood-down marker names no session")
	}
	return KeepaliveEffect{
		Result:    KeepaliveResultKept,
		SessionID: sessionID,
		Reason:    "stood-down",
	}, nil
}
