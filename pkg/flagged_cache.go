// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"
)

// flaggedCacheFileName is the file the last good flagged row set is persisted to.
//
// It exists because the cache is in process only, so a restart empties it and the
// first tick after a start has nothing to decide on: it logs `flagged-tick
// skipped: predicate has not returned yet` and opens nothing until the first read
// returns. Measured 2026-11-19 over `2026-11-18 13:18:17 → 2026-11-19 13:18:17`:
// 7 starts and 7 such ticks, one per start. The daemon restarts on every deploy
// (8 releases in that window), so the dead tick is a standing cost of shipping
// the wrapper at all — and a 24 h deploy freeze is not a practice this repo has.
const flaggedCacheFileName = "flagged-rows-cache.json"

// flaggedCacheFile is the on-disk form of one successful predicate read.
type flaggedCacheFile struct {
	// ReadAt is when the read behind Rows started. It is what the staleness bound
	// is measured from.
	ReadAt time.Time    `json:"read_at"`
	Rows   []FlaggedRow `json:"rows"`
}

// flaggedCachePath returns the cache file inside the daemon's state directory.
func flaggedCachePath(stateDir string) string {
	return filepath.Join(stateDir, flaggedCacheFileName)
}

// loadFlaggedCache reads the persisted row set.
//
// A missing, empty or undecodable file is not an error and never a failed start:
// the cache is an optimisation across a restart, not a source of truth, so a
// corrupt file costs one skipped tick — exactly the behaviour that predates it.
func loadFlaggedCache(stateDir string) (rows []FlaggedRow, readAt time.Time, ok bool) {
	if stateDir == "" {
		return nil, time.Time{}, false
	}
	body, err := os.ReadFile(flaggedCachePath(stateDir)) // #nosec G304 -- daemon state dir, not request input
	if err != nil {
		return nil, time.Time{}, false
	}
	var file flaggedCacheFile
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, time.Time{}, false
	}
	// ReadAt alone decides whether a cache exists. An absent or empty Rows is a
	// real answer — a predicate that found nothing eligible — so rejecting it here
	// would cost exactly the dead tick this file exists to remove.
	if file.ReadAt.IsZero() {
		return nil, time.Time{}, false
	}
	return file.Rows, file.ReadAt, true
}

// saveFlaggedCache writes the row set through a temp file and a rename, so a
// crash cannot leave a half-written body that the next start would refuse.
//
// The write is best effort: the caller logs a failure and keeps ticking on the
// in-process cache, because a read-only state directory must not cost a tick.
func saveFlaggedCache(
	ctx context.Context, stateDir string, rows []FlaggedRow, readAt time.Time,
) error {
	if stateDir == "" {
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return errors.Wrapf(ctx, err, "create flagged cache dir %s failed", stateDir)
	}
	body, err := json.Marshal(flaggedCacheFile{ReadAt: readAt, Rows: rows})
	if err != nil {
		return errors.Wrapf(ctx, err, "encode flagged cache failed")
	}
	tmp, err := os.CreateTemp(stateDir, "*.json.tmp")
	if err != nil {
		return errors.Wrapf(ctx, err, "create flagged cache temp file failed")
	}
	tmpName := tmp.Name()
	// A successful rename removes the temp name, so this only cleans up a failed
	// write.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return errors.Wrapf(ctx, err, "write flagged cache temp file failed")
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrapf(ctx, err, "close flagged cache temp file failed")
	}
	if err := os.Rename(tmpName, flaggedCachePath(stateDir)); err != nil {
		return errors.Wrapf(ctx, err, "rename flagged cache failed")
	}
	return nil
}
