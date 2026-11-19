// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	libtime "github.com/bborbe/time"
)

// ClaimStore reads and writes worker ownership claims.
//
// The port keeps the claim write behind this interface rather than reimplementing
// `ownership-claim.py`'s store: the store is the single writer of ownership, and a
// second writer would be the duplicate it exists to prevent.
type ClaimStore interface {
	// Holder returns the manager that holds the worker, or "" when unclaimed.
	Holder(ctx context.Context, worker string) (string, error)
	// Claim records manager as the owner of worker.
	Claim(ctx context.Context, worker, manager, note string) error
}

// SubjectReader resolves a session's subject and whether it is stood down.
type SubjectReader interface {
	// Subject returns the subject a session records, or "" when it records none.
	Subject(ctx context.Context, sessionID string) (string, error)
	// Stopped returns the marker that stands the subject down, and whether one
	// exists. An empty subject is never stopped.
	Stopped(ctx context.Context, subject string) (string, bool, error)
}

// AdoptTick adopts the live workers an exited manager left behind, so their gates
// have one owner.
//
// Ported from `claude-supervisor@e2dd8b1:scripts/adopt-orphans.py` (fleet-round
// row 4). The guards are the script's own: a worker must be live, its spawner must
// be a non-live manager, a stood-down subject is skipped, exactly one live
// same-subject successor adopts (else this session does), and a worker a live
// manager already holds is not re-adopted.
type AdoptTick struct {
	Ledger   LedgerReader
	Registry SessionRegistry
	Claims   ClaimStore
	Subjects SubjectReader
	// Self is the adopter when no unique successor exists.
	Self string
	Now  libtime.CurrentDateTimeGetter
	Logf func(format string, args ...any)
}

// AdoptResult is one round's outcome.
type AdoptResult struct {
	Adopted []string
	Skipped []string
}

// Run adopts every stranded worker this round, applying the five guards.
func (t *AdoptTick) Run(ctx context.Context) (AdoptResult, error) {
	var result AdoptResult
	if t.Ledger == nil || t.Registry == nil || t.Claims == nil || t.Self == "" {
		return result, nil
	}
	live, err := t.liveSet(ctx)
	if err != nil {
		return result, err
	}
	rows, err := t.Ledger.Rows(ctx)
	if err != nil {
		return result, err
	}
	for exited, workers := range strandedWorkers(rows, live) {
		if err := t.adoptManager(ctx, exited, workers, live, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// liveSet is the set of live session ids.
func (t *AdoptTick) liveSet(ctx context.Context) (map[string]bool, error) {
	entries, err := t.Registry.Live(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(entries))
	for _, entry := range entries {
		live[entry.SessionID] = true
	}
	return live, nil
}

// subjectOfErr resolves one session's subject, returning the read error so the
// stood-down guard can fail CLOSED: a discarded error reads as "not stopped" and
// would write a claim beside the operator's own stand-down.
func (t *AdoptTick) subjectOfErr(ctx context.Context, sessionID string) (string, error) {
	if t.Subjects == nil {
		return "", nil
	}
	return t.Subjects.Subject(ctx, sessionID)
}

// adoptManager applies the stood-down guard, then adopts every worker the exited
// manager left behind.
func (t *AdoptTick) adoptManager(
	ctx context.Context,
	exited string,
	workers []string,
	live map[string]bool,
	result *AdoptResult,
) error {
	subject, err := t.subjectOfErr(ctx, exited)
	if err != nil {
		result.Skipped = append(result.Skipped, exited+": subject read failed: "+err.Error())
		return nil
	}
	if subject != "" {
		marker, stopped, err := t.Subjects.Stopped(ctx, subject)
		if err != nil {
			result.Skipped = append(
				result.Skipped, exited+": stand-down read failed: "+err.Error(),
			)
			return nil
		}
		if stopped {
			result.Skipped = append(
				result.Skipped, exited+": subject stood down ("+marker+")",
			)
			return nil
		}
	}
	// The successor join is lenient: a failed subject read falls back to self,
	// which is live, so it never writes beside a dead holder.
	subjectOf := func(id string) string {
		s, _ := t.subjectOfErr(ctx, id)
		return s
	}
	adopter, how := resolveAdopter(exited, live, t.Self, subjectOf)
	for _, worker := range workers {
		if err := t.adoptWorker(ctx, exited, worker, adopter, how, live, result); err != nil {
			return err
		}
	}
	return nil
}

// adoptWorker claims one worker unless a live manager already holds it.
func (t *AdoptTick) adoptWorker(
	ctx context.Context,
	exited, worker, adopter, how string,
	live map[string]bool,
	result *AdoptResult,
) error {
	holder, err := t.Claims.Holder(ctx, worker)
	if err != nil {
		return err
	}
	if alreadyOwned(holder, live) {
		return nil
	}
	if err := t.Claims.Claim(ctx, worker, adopter, "adopted from "+exited+" ("+how+")"); err != nil {
		return err
	}
	result.Adopted = append(result.Adopted, worker)
	return nil
}

// strandedWorkers returns {exited manager: [live worker, ...]} for LIVE workers
// whose spawner is a non-live manager.
//
// The worker must be live: a worker that also exited holds no gate. The spawner
// must be a manager (`Role == "manager"`) and must not be live. A wrong spawner
// field reads as empty, which is treated as "no spawner" and skipped — never as
// a reason to adopt.
func strandedWorkers(rows []LedgerRow, live map[string]bool) map[string][]string {
	out := map[string][]string{}
	for _, row := range rows {
		if row.SessionID == "" || !live[row.SessionID] {
			continue
		}
		parent := row.SpawnerSessionID
		if parent == "" || live[parent] {
			continue
		}
		// The ROW must be a worker (never a manager — a manager is not adopted),
		// and its PARENT must be a manager. The `&&` form admitted a manager row
		// whose spawner was not a manager, writing a claim under a non-manager.
		if row.Role == "manager" || !isManagerRow(rows, parent) {
			continue
		}
		out[parent] = append(out[parent], row.SessionID)
	}
	return out
}

// isManagerRow reports whether the session id appears as a manager in the ledger.
func isManagerRow(rows []LedgerRow, sessionID string) bool {
	for _, row := range rows {
		if row.SessionID == sessionID && row.Role == "manager" {
			return true
		}
	}
	return false
}

// alreadyOwned reports whether a LIVE manager already holds the worker.
//
// A claim held by a manager that is gone is not ownership, so the holder must be
// live; otherwise every later tick re-adopts the same worker forever.
func alreadyOwned(holder string, live map[string]bool) bool {
	return holder != "" && live[holder]
}

// resolveAdopter returns (adopter, how): a unique live same-subject successor when
// one exists, else this session.
func resolveAdopter(
	exited string,
	live map[string]bool,
	self string,
	subjectOf func(string) string,
) (string, string) {
	subject := subjectOf(exited)
	if subject != "" {
		var matches []string
		for sessionID := range live {
			if sessionID == exited {
				continue
			}
			if subjectOf(sessionID) == subject {
				matches = append(matches, sessionID)
			}
		}
		if len(matches) == 1 {
			return matches[0], "successor"
		}
	}
	return self, "fallback"
}
