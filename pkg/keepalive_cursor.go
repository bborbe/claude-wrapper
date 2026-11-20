// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
)

// KeepaliveCursorStore persists the rotation cursor: `vault → last page evaluated`,
// one store per lister (topic / task).
type KeepaliveCursorStore interface {
	// Load returns the cursor map. A missing or corrupt file returns an EMPTY map
	// and no error — a corrupt cursor must not stop the tick; the scan simply starts
	// from the beginning.
	Load(ctx context.Context) (map[string]string, error)
	Save(ctx context.Context, cursor map[string]string) error
}

// NewKeepaliveCursorFile builds a filesystem-backed cursor store at path (a JSON
// object of `vault → page name`).
func NewKeepaliveCursorFile(path string) KeepaliveCursorStore {
	return &keepaliveCursorFile{path: path}
}

type keepaliveCursorFile struct {
	path string
}

// Load reads the cursor, treating a missing or corrupt file as empty.
func (s *keepaliveCursorFile) Load(ctx context.Context) (map[string]string, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return map[string]string{}, errors.Wrapf(ctx, err, "read cursor %s failed", s.path)
	}
	cursor := map[string]string{}
	if unmarshalErr := json.Unmarshal(raw, &cursor); unmarshalErr != nil {
		// ⚠️ A corrupt cursor is EMPTY, not an error: the tick must still run, starting
		// from the beginning, rather than aborting on a bad state file.
		return map[string]string{}, nil
	}
	return cursor, nil
}

// Save writes the cursor, creating the parent directory.
func (s *keepaliveCursorFile) Save(ctx context.Context, cursor map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.Wrapf(ctx, err, "create cursor dir for %s failed", s.path)
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal cursor failed")
	}
	if err := os.WriteFile(s.path, encoded, 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write cursor %s failed", s.path)
	}
	return nil
}
