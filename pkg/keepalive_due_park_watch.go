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
	// DueTasks reports every TASK whose watch has come due, sorted.
	//
	// ⚠️ A task NAME, not a session id. `park-event.py due` prints one line per
	// watch naming the task — several sessions parked on one watch are still ONE
	// line — and the act is `/supervisor:open --task "<name>"`, which takes a name.
	// Naming a session here would make the act open the wrong thing.
	DueTasks func(ctx context.Context) ([]string, error)
	// Blockers returns the row's UNRESOLVED `blocked_by` entries, resolved through
	// the same vault-api read the flagged tick uses. A non-empty result skips the
	// task. nil means no check, which is the pre-fix behaviour and is wired only by
	// a caller that genuinely has no index.
	Blockers func(ctx context.Context, task string) ([]string, error)
	// Resume opens the task BY NAME through the open surface (`OpenRow{Kind: task,
	// Vault, Name}` — the vault resolved by first hit across the filtered vault
	// list). It takes the TASK, not a session id: the watch names a task, and the
	// resume opens that task so a fresh session picks it up.
	Resume func(ctx context.Context, task string) error
}

// Name is the `action=` value this procedure writes.
func (p *DueParkWatchProcedure) Name() string { return "due-park-watch" }

// Run resumes at most one due watch per tick.
//
// One per tick for the same reason the dead-worker procedure resumes one: a
// backlog of due watches after a long outage must not fire at once.
func (p *DueParkWatchProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.DueTasks == nil {
		return KeepaliveEffect{}, errors.New(ctx, "due-park-watch has no watch source")
	}
	if p.Resume == nil {
		return KeepaliveEffect{}, errors.New(ctx, "due-park-watch has no resume action")
	}
	tasks, err := p.DueTasks(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read park watches failed")
	}
	if len(tasks) == 0 {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	return p.actOnFirstFree(ctx, tasks)
}

// actOnFirstFree resumes the first due task that is not gated, and reports the
// first gated skip when none is free.
//
// ⚠️ **No head-of-line blocking, and no silent stall.** A gated task must not stop
// the scan: `DueTasks` is sorted, so the same blocked task would otherwise be the
// first every tick and the leg would resume nothing at all while any blocker
// stayed open. Skip it, remember the first skip for the log, continue.
func (p *DueParkWatchProcedure) actOnFirstFree(
	ctx context.Context,
	tasks []string,
) (KeepaliveEffect, error) {
	var firstSkip *KeepaliveEffect
	for _, task := range tasks {
		if task == "" {
			return KeepaliveEffect{}, errors.New(ctx, "due park watch names no task")
		}
		blockers, err := p.blockers(ctx, task)
		if err != nil {
			return KeepaliveEffect{}, err
		}
		if len(blockers) > 0 {
			if firstSkip == nil {
				firstSkip = blockedSkip(task, blockers)
			}
			continue
		}
		if resumeErr := p.Resume(ctx, task); resumeErr != nil {
			return KeepaliveEffect{}, errors.Wrapf(ctx, resumeErr, "resume %s failed", task)
		}
		return KeepaliveEffect{Result: KeepaliveResultResumed, SessionID: task}, nil
	}
	if firstSkip != nil {
		return *firstSkip, nil
	}
	return KeepaliveEffect{Result: KeepaliveResultNone}, nil
}

// blockers returns the task's unresolved blockers, or nil when no check is wired.
func (p *DueParkWatchProcedure) blockers(ctx context.Context, task string) ([]string, error) {
	if p.Blockers == nil {
		return nil, nil
	}
	blockers, err := p.Blockers(ctx, task)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read blockers for %s failed", task)
	}
	return blockers, nil
}

// blockedSkip builds the attributable skip: the row, the reason, and the blockers
// that caused it.
func blockedSkip(task string, blockers []string) *KeepaliveEffect {
	return &KeepaliveEffect{
		Result:    KeepaliveResultSkipped,
		SessionID: task,
		Reason:    "blocked",
		Blockers:  blockers,
	}
}
