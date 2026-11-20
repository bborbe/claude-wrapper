// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// DefaultPendingRecordMaxAge is how long a pending record waits for its session to
// read `live` before the drain DROPS it as stale — 2 ticks + slack (long enough for
// a slow-starting resume, short enough that a dead one does not linger).
const DefaultPendingRecordMaxAge = 45 * time.Minute

// PendingRecord is one session awaiting its LATER-TICK `--record-resume`.
//
// ⚠️ **The record cannot happen in the tick that acts.** A session opened this
// tick reads `STARTING`, so `--record-resume` exits 1 and the cap never arms —
// the exact failure the adapter exists to prevent. So the act's tick ADDS a
// pending record; a later tick, once the id reads `live`, RECORDS it and removes
// the entry. This type is the entry that spans those two ticks.
type PendingRecord struct {
	SessionID string `json:"session_id"`
	// Vault and Task are the WORKER tier's subject — `worker-keepalive.py --vault
	// <vault> --task <task> --record-resume <id>`. Empty for the manager tier.
	Vault string `json:"vault,omitempty"`
	Task  string `json:"task,omitempty"`
	// Kind selects the recording script: `manager` (topic-keepalive.py
	// --record-resume), `worker` (worker-keepalive.py --vault --task
	// --record-resume), or `decision-park` (decision-park-resume.py --record
	// --sid). The drain's record action dispatches on it.
	Kind string `json:"kind"`
	// CreatedAt is when the record was enqueued (the tick that acted). The drain
	// DROPS an entry older than its max age whose id still does not read live —
	// a resume that never came up must not sit on disk and be re-checked forever.
	CreatedAt time.Time `json:"created_at"`
}

// PendingRecordStore persists pending records ACROSS ticks and across a daemon
// reload.
//
// ⚠️ **It must be on disk, not in memory.** Subtask 6a restarts the daemon
// between tick N (which adds the pending record) and tick N+1 (which records it);
// an in-memory set loses every entry on that reload, so the cap silently never
// arms again. One entry per pending session, under the wrapper's state dir.
type PendingRecordStore interface {
	Add(ctx context.Context, record PendingRecord) error
	List(ctx context.Context) ([]PendingRecord, error)
	Remove(ctx context.Context, sessionID string) error
}

// NewPendingRecordDir builds a filesystem-backed store: one `<sessionID>.json`
// file per pending session under dir. The directory is created on first Add.
func NewPendingRecordDir(dir string) PendingRecordStore {
	return &pendingRecordDir{dir: dir}
}

type pendingRecordDir struct {
	dir string
}

// Add writes (or overwrites) the pending record for its session. Overwriting is
// idempotent on purpose: re-adding the same pending record must not error.
func (s *pendingRecordDir) Add(ctx context.Context, record PendingRecord) error {
	if record.SessionID == "" {
		return errors.New(ctx, "pending record needs a session id")
	}
	if err := s.ensureDir(ctx); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal pending record %s failed", record.SessionID)
	}
	path := s.path(record.SessionID)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write pending record %s failed", path)
	}
	return nil
}

// List returns every pending record on disk. A read failure surfaces rather than
// returning an empty set: "no pending records" and "the store is unreadable" are
// different facts, and collapsing them would report a quiet tick over a store the
// drain never reached.
func (s *pendingRecordDir) List(ctx context.Context) ([]PendingRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(ctx, err, "read pending-record dir %s failed", s.dir)
	}
	records := make([]PendingRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if readErr != nil {
			return nil, errors.Wrapf(ctx, readErr, "read pending record %s failed", entry.Name())
		}
		var record PendingRecord
		if unmarshalErr := json.Unmarshal(raw, &record); unmarshalErr != nil {
			return nil, errors.Wrapf(ctx, unmarshalErr, "parse pending record %s failed", entry.Name())
		}
		records = append(records, record)
	}
	return records, nil
}

// Remove deletes the pending record for a session. A missing file is not an error
// — the record was already drained.
func (s *pendingRecordDir) Remove(ctx context.Context, sessionID string) error {
	if err := os.Remove(s.path(sessionID)); err != nil && !os.IsNotExist(err) {
		return errors.Wrapf(ctx, err, "remove pending record %s failed", sessionID)
	}
	return nil
}

func (s *pendingRecordDir) ensureDir(ctx context.Context) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return errors.Wrapf(ctx, err, "create pending-record dir %s failed", s.dir)
	}
	return nil
}

// path is the record file for a session. The id is validated so it cannot escape
// the directory — a session id carrying a path separator would be a path-traversal
// write.
func (s *pendingRecordDir) path(sessionID string) string {
	return filepath.Join(s.dir, pendingRecordName(sessionID))
}

// pendingRecordName is the record filename for a session, with a separator or
// ".." refused rather than written.
func pendingRecordName(sessionID string) string {
	if strings.ContainsAny(sessionID, `/\`) || strings.Contains(sessionID, "..") {
		// path() is a pure string join; a hostile id must not reach it. The caller
		// validates too, but keeping the guard here means no future caller can
		// bypass it.
		return "_invalid_" + strings.NewReplacer("/", "_", `\`, "_").Replace(sessionID) + ".json"
	}
	return sessionID + ".json"
}

// DrainPendingRecords records every pending session whose id NOW reads live, then
// removes it. A session still not live is left for a later tick — UNLESS it is
// older than maxAge, in which case it is DROPPED (removed) and counted stale: a
// resume that never came up must not sit on disk and be re-checked forever.
//
// This is the later-tick half of the record: tick N added the pending record when
// it acted; this drain, on tick N+1 or later, records once the session is up. A
// `record` failure is returned (not swallowed) — the entry stays on disk, so the
// next tick retries rather than losing the record.
func DrainPendingRecords(
	ctx context.Context,
	store PendingRecordStore,
	live func(ctx context.Context, sessionID string) (bool, error),
	record func(ctx context.Context, pending PendingRecord) error,
	maxAge time.Duration,
	now libtime.CurrentDateTimeGetter,
) (recorded int, stale int, err error) {
	if store == nil {
		return 0, 0, errors.New(ctx, "drain has no pending-record store")
	}
	if live == nil {
		return 0, 0, errors.New(ctx, "drain has no live check")
	}
	if record == nil {
		return 0, 0, errors.New(ctx, "drain has no record action")
	}
	if now == nil {
		return 0, 0, errors.New(ctx, "drain has no clock")
	}
	pending, err := store.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range pending {
		// Stale first: an entry whose id never read live within maxAge is dropped,
		// not retried forever.
		if maxAge > 0 && now.Now().Time().Sub(entry.CreatedAt) > maxAge {
			if removeErr := store.Remove(ctx, entry.SessionID); removeErr != nil {
				return recorded, stale, removeErr
			}
			stale++
			continue
		}
		isLive, liveErr := live(ctx, entry.SessionID)
		if liveErr != nil {
			return recorded, stale, errors.Wrapf(ctx, liveErr, "liveness check for pending record %s failed", entry.SessionID)
		}
		if !isLive {
			continue
		}
		if recordErr := record(ctx, entry); recordErr != nil {
			return recorded, stale, errors.Wrapf(ctx, recordErr, "record pending resume %s failed", entry.SessionID)
		}
		if removeErr := store.Remove(ctx, entry.SessionID); removeErr != nil {
			return recorded, stale, removeErr
		}
		recorded++
	}
	return recorded, stale, nil
}
