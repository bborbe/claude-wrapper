// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// DueParkWatchProcedure resumes a worker whose park watch has come due.
//
// A park watch is a session that parked itself with a re-check time. Nothing else
// wakes it: the worker is not lapsed (its process may be gone) and no round owns
// it, so a missed re-check leaves the row parked forever. This procedure is the
// only leg that resumes one.
//
// ⚠️ **The watch is CONSUMED by the resume, and that is the de-duplication.**
// There is no separate claim store here, because the store the watch lives in is
// the same store the resume writes: once the row is resumed it is no longer due,
// so a second writer finds nothing. That is why this procedure does not take
// `--claim-resume` — the script's claim covers the topic tier's recorded session
// ids, and a park watch is not one of those.
type DueParkWatchProcedure struct {
	// Due reports the session whose watch has come due. `false` is a quiet tick,
	// not an error.
	Due func(ctx context.Context) (sessionID string, due bool, err error)
	// Resume brings the session back.
	Resume KeepaliveResume
}

// Name is the `action=` value this procedure writes.
func (p *DueParkWatchProcedure) Name() string { return "due-park-watch" }

// Run resumes at most one due watch per tick.
//
// One per tick for the same reason the dead-worker procedure resumes one: a
// backlog of due watches after a long outage must not fire at once.
func (p *DueParkWatchProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Due == nil {
		return KeepaliveEffect{}, errors.New(ctx, "due-park-watch has no watch source")
	}
	if p.Resume == nil {
		return KeepaliveEffect{}, errors.New(ctx, "due-park-watch has no resume action")
	}
	sessionID, due, err := p.Due(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read park watches failed")
	}
	if !due {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	if sessionID == "" {
		return KeepaliveEffect{}, errors.New(ctx, "due park watch names no session")
	}
	if resumeErr := p.Resume(ctx, sessionID); resumeErr != nil {
		return KeepaliveEffect{}, errors.Wrapf(ctx, resumeErr, "resume %s failed", sessionID)
	}
	return KeepaliveEffect{
		Result:    KeepaliveResultResumed,
		SessionID: sessionID,
	}, nil
}
