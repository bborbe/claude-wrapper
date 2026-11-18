// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"
)

// MAX_AGE_S is how long a park on an unprobeable observable may last before
// the row is reopened anyway.
//
// It mirrors claude-supervisor/scripts/declared-wait.py's rule for a
// declaration naming no future clock: one extra hour, never an indefinite pin.
// It is a package constant, not a config key, and there is no per-class
// variant of it.
const MAX_AGE_S = 3600 * time.Second //nolint:revive // the name is frozen: the spec's mutation check and docs/parked-waits.md both use it

// ParkedBaseline is one row's recorded first sighting of a park.
type ParkedBaseline struct {
	// Class is the wait class the declaration carried when it was recorded.
	Class ParkedClass `json:"class"`
	// Fingerprint is the observable's reading at first sighting.
	Fingerprint string `json:"fingerprint"`
	// FirstSeen is when this park was first recorded, on the daemon's clock.
	FirstSeen time.Time `json:"first_seen"`
	// SessionID is the session whose closer declared the park.
	SessionID string `json:"session_id"`
	// CloserHash is the sha256 hex of the closer text, so a resumed session
	// that parks again on different words starts a new baseline.
	CloserHash string `json:"closer_hash"`
}

// ParkedStore reads and writes one baseline per row.
type ParkedStore interface {
	// Load returns the row's recorded baseline, or nil when none is readable.
	// A missing or corrupt file is nil, never an error: a stale baseline costs
	// one tick and self-heals.
	Load(ctx context.Context, key string) *ParkedBaseline
	// Save writes the row's baseline.
	Save(ctx context.Context, key string, baseline ParkedBaseline) error
}

// NewParkedStore creates a store rooted at dir.
func NewParkedStore(dir string) ParkedStore {
	return &parkedStore{dir: dir}
}

type parkedStore struct {
	dir string
}

// parkedBaselinePath returns the file one row's baseline lives in.
//
// The key is hashed so a key containing ".." or "/" can never escape dir.
func parkedBaselinePath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

// Load reads one baseline, returning nil when the file is missing or its body
// cannot be decoded.
func (s *parkedStore) Load(_ context.Context, key string) *ParkedBaseline {
	body, err := os.ReadFile(parkedBaselinePath(s.dir, key))
	if err != nil {
		return nil
	}
	var baseline ParkedBaseline
	if err := json.Unmarshal(body, &baseline); err != nil {
		return nil
	}
	return &baseline
}

// Save writes one baseline through a temp file in the same directory followed
// by a rename, so a crash cannot leave a half-written body.
func (s *parkedStore) Save(ctx context.Context, key string, baseline ParkedBaseline) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return errors.Wrapf(ctx, err, "create parked baseline dir %s failed", s.dir)
	}
	body, err := json.Marshal(baseline)
	if err != nil {
		return errors.Wrapf(ctx, err, "encode parked baseline failed")
	}
	tmp, err := os.CreateTemp(s.dir, "*.json.tmp")
	if err != nil {
		return errors.Wrapf(ctx, err, "create parked baseline temp file failed")
	}
	tmpName := tmp.Name()
	// A successful rename removes the temp name, so this only cleans up a
	// failed write.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return errors.Wrapf(ctx, err, "write parked baseline temp file failed")
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrapf(ctx, err, "close parked baseline temp file failed")
	}
	if err := os.Rename(tmpName, parkedBaselinePath(s.dir, key)); err != nil {
		return errors.Wrapf(ctx, err, "rename parked baseline failed")
	}
	return nil
}

// StateDirs resolves the sessions directory and its parked/ sibling from the
// --state-dir flag value.
//
// An empty value follows the same chain NewSessionStore follows, so the two
// can never disagree about where the daemon's state lives.
func StateDirs(stateDir string) (sessions string, parked string, err error) {
	sessions, err = SessionRoot(context.Background(), stateDir, "", nil)
	if err != nil {
		return "", "", errors.Wrapf(context.Background(), err, "resolve sessions directory failed")
	}
	return sessions, filepath.Join(filepath.Dir(sessions), "parked"), nil
}
