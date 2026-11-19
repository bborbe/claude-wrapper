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
// round that persists after a FAILED load resets every counter and makes every
// session read first-seen. The loss is silent — the file it writes is
// well-formed — so the refusal is enforced here rather than left to a caller.
//
// ⚠️ **The guard is about a failed load, never about an absent file.** A fresh
// install's first round has no previous snapshot and MUST still be able to
// create one; refusing there is a deadlock, not a safety property. That is why
// `Persist` takes the load's `error` rather than a boolean: a boolean named
// "loaded" reads the same at both call sites while meaning two different things
// (a file existed / the read succeeded), and the first wiring that used it that
// way made every fresh install persist nothing forever.
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

// Load reads the previous snapshot at path.
//
// ⚠️ **A missing file is not an error, and the empty snapshot it returns is a
// legitimate value to diff against.** The first round of a fresh install has no
// previous snapshot; every session correctly reads first-seen and every counter
// starts at zero. Only a file that EXISTS and cannot be read or parsed returns
// an error — that is the case where the round cannot know what it is overwriting.
func Load(ctx context.Context, path string) (Snapshot, error) {
	empty := Snapshot{Sessions: map[string]Entry{}}
	raw, err := os.ReadFile(path) //nolint:gosec // path is the caller's own state file
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, errors.Wrapf(ctx, err, "read fleet snapshot %q failed", path)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return empty, errors.Wrapf(ctx, err, "parse fleet snapshot %q failed", path)
	}
	if snap.Sessions == nil {
		snap.Sessions = map[string]Entry{}
	}
	return snap, nil
}

// Persist writes snap atomically, refusing when the caller's Load FAILED.
//
// `loadErr` is the error Load returned — pass it through unchanged. It is nil
// for a successful load AND for an absent file, so a fresh install persists
// normally; it is non-nil only when a previous snapshot existed and could not be
// read, which is exactly the case where writing would destroy a baseline the
// round never saw.
//
// The refusal leaves the existing file byte-identical — the same discipline as
// refusing an empty write, reached through the ORDER of the round rather than
// its payload. The write is tmp + rename so a crash mid-write cannot truncate
// the previous snapshot; a truncated file would read as an empty fleet and reset
// every counter.
func Persist(ctx context.Context, path string, snap Snapshot, loadErr error) error {
	if loadErr != nil {
		return errors.Wrapf(ctx, loadErr,
			"refusing to persist the fleet snapshot: the previous one could not be loaded")
	}
	if len(snap.Sessions) == 0 {
		// A failed read and an empty fleet must not look alike. This is separate
		// from the load guard: a round CAN load correctly and still hold no
		// sessions.
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
