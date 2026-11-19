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
	// DueTask reports the TASK whose watch has come due.
	//
	// ⚠️ A task NAME, not a session id. `park-event.py due` prints one line per
	// watch naming the task — several sessions parked on one watch are still ONE
	// line — and the act is `/supervisor:open --task "<name>"`, which takes a name.
	// Naming a session here would make the act open the wrong thing.
	DueTask func(ctx context.Context) (task string, due bool, err error)
	// Resume brings the task's parked session back.
	Resume KeepaliveResume
}

// Name is the `action=` value this procedure writes.
func (p *DueParkWatchProcedure) Name() string { return "due-park-watch" }

// Run resumes at most one due watch per tick.
//
// One per tick for the same reason the dead-worker procedure resumes one: a
// backlog of due watches after a long outage must not fire at once.
func (p *DueParkWatchProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.DueTask == nil {
		return KeepaliveEffect{}, errors.New(ctx, "due-park-watch has no watch source")
	}
	if p.Resume == nil {
		return KeepaliveEffect{}, errors.New(ctx, "due-park-watch has no resume action")
	}
	task, due, err := p.DueTask(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read park watches failed")
	}
	if !due {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	if task == "" {
		return KeepaliveEffect{}, errors.New(ctx, "due park watch names no task")
	}
	if resumeErr := p.Resume(ctx, task); resumeErr != nil {
		return KeepaliveEffect{}, errors.Wrapf(ctx, resumeErr, "resume %s failed", task)
	}
	return KeepaliveEffect{
		Result:    KeepaliveResultResumed,
		SessionID: task,
	}, nil
}
