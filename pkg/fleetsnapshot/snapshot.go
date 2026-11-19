// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package fleetsnapshot owns the fleet round's previous-sweep store.
//
// The store is keyed by SESSION ID, not by task: it answers "what did the last
// round see for this session", which is what makes `stall_count` a count across
// rounds rather than a fact about one. It is deliberately not `sweepgate`'s
// `Snapshot`, which is a subject → tracked-tasks resolver — two different
// questions that would be a false friend under one name.
//
// ⚠️ The load guard is the reason this package exists as a type rather than a
// pair of functions: `stall_count` is computed from the PREVIOUS snapshot, so a
// round that persists without having loaded one resets every counter and makes
// every session read first-seen. The loss is silent — the file it writes is
// well-formed — so the refusal is enforced here rather than left to a caller.
package fleetsnapshot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
)

// Entry is one session's row in the fleet snapshot.
type Entry struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	TaskFile   string `json:"task_file"`
	TaskMtime  string `json:"task_mtime"`
	StallCount int    `json:"stall_count"`
}

// Snapshot is the whole store: every session the previous round saw, plus when.
type Snapshot struct {
	SweptAt  string           `json:"swept_at"`
	Sessions map[string]Entry `json:"sessions"`
}

// Load reads the snapshot at path.
//
// A missing file is not an error: a fresh install's first round has no previous
// snapshot, and the caller must be able to tell that apart from a read that
// failed. So the second return is "a previous snapshot was loaded" — it is false
// for a missing file AND for any failure, while the error distinguishes the two
// for the caller's own logging.
func Load(ctx context.Context, path string) (Snapshot, bool, error) {
	empty := Snapshot{Sessions: map[string]Entry{}}
	raw, err := os.ReadFile(path) //nolint:gosec // path is the caller's own state file
	if err != nil {
		if os.IsNotExist(err) {
			return empty, false, nil
		}
		return empty, false, errors.Wrapf(ctx, err, "read fleet snapshot %q failed", path)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return empty, false, errors.Wrapf(ctx, err, "parse fleet snapshot %q failed", path)
	}
	if snap.Sessions == nil {
		snap.Sessions = map[string]Entry{}
	}
	return snap, true, nil
}

// Persist writes snap atomically, refusing when the previous snapshot was not loaded.
//
// `loaded` is the guard made explicit: a caller that skipped Load passes false
// and is refused, so persist-without-load cannot happen by omission. The refusal
// leaves the existing file byte-identical — the same discipline as refusing an
// empty write, reached through the ORDER of the round rather than its payload.
//
// The write is tmp + rename so a crash mid-write leaves the previous snapshot
// intact; a truncated file would read as an empty fleet and reset every counter.
func Persist(ctx context.Context, path string, snap Snapshot, loaded bool) error {
	if !loaded {
		return errors.Errorf(ctx,
			"refusing to persist the fleet snapshot: the previous one was not loaded")
	}
	if len(snap.Sessions) == 0 {
		// A failed read and an empty fleet must not look alike. This is the same
		// refusal the Python makes, and it is separate from the load guard: a
		// round CAN load correctly and still hold no sessions.
		return errors.Errorf(ctx,
			"refusing to persist an empty fleet snapshot: a failed read and an empty fleet must not look alike")
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal fleet snapshot failed")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return errors.Wrapf(ctx, err, "create fleet snapshot dir %q failed", dir)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write fleet snapshot temp %q failed", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(ctx, err, "replace fleet snapshot %q failed", path)
	}
	return nil
}
