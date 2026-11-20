// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// claimEntry is one ownership claim as `ownership-claim.py` writes it.
//
// The field names and their order are the store's, not this port's: the same file
// is read by `gate-owner-filter.py` and by `ownership-claim.py list`, and a
// renamed field would read as an absent claim to both.
type claimEntry struct {
	Session   string `json:"session"`
	Manager   string `json:"manager"`
	Note      string `json:"note"`
	ClaimedAt string `json:"claimed_at"`
}

// claimFile is the store's top-level shape.
type claimFile struct {
	Version int                   `json:"version"`
	Claims  map[string]claimEntry `json:"claims"`
}

// FileClaimStore is the ClaimStore backed by `ownership-claim.py`'s own store.
//
// ⚠️ It writes the store's exact format and takes the store's own sidecar lock,
// because that file has a second writer. A port that skipped the flock would
// interleave its read-modify-write with a manager's `ownership-claim.py claim`
// and drop one of the two claims — the duplicate the store exists to prevent.
type FileClaimStore struct {
	// Path is `~/.claude/state/ownership-claims.json` unless overridden.
	Path string
	// Now supplies `claimed_at`; the store writes UTC seconds, never a local zone.
	Now libtime.CurrentDateTimeGetter
}

// NewFileClaimStore builds the store, defaulting the path and the clock.
func NewFileClaimStore(path string, now libtime.CurrentDateTimeGetter) *FileClaimStore {
	if path == "" {
		path = defaultOwnershipClaimsPath()
	}
	if now == nil {
		now = libtime.NewCurrentDateTime()
	}
	return &FileClaimStore{Path: path, Now: now}
}

// defaultOwnershipClaimsPath is the store's path, honouring the same environment
// override the script does so a test and the daemon agree on which file is live.
func defaultOwnershipClaimsPath() string {
	if path := os.Getenv("SUPERVISOR_OWNERSHIP_CLAIMS"); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "state", "ownership-claims.json")
}

// Holder returns the manager that holds worker, or "" when it is unclaimed.
func (s *FileClaimStore) Holder(ctx context.Context, worker string) (string, error) {
	data, err := s.read(ctx)
	if err != nil {
		return "", errors.Wrap(ctx, err, "read ownership claims")
	}
	return data.Claims[worker].Manager, nil
}

// Claim records manager as the owner of worker, under the store's lock.
func (s *FileClaimStore) Claim(
	ctx context.Context,
	worker, manager, note string,
) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := s.read(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read ownership claims")
	}
	data.Claims[worker] = claimEntry{
		Session:   worker,
		Manager:   manager,
		Note:      note,
		ClaimedAt: s.Now.Now().Time().UTC().Format("2006-01-02T15:04:05Z"),
	}
	return s.write(ctx, data)
}

// read returns the store, treating an absent or malformed file as empty.
//
// ⚠️ Malformed is empty, not an error: the store's own reader does the same, and
// refusing here would stop every adoption on a file a human hand-edited. The
// write that follows repairs it.
func (s *FileClaimStore) read(ctx context.Context) (claimFile, error) {
	empty := claimFile{Version: 1, Claims: map[string]claimEntry{}}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, errors.Wrap(ctx, err, "read claim file")
	}
	var data claimFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return empty, nil
	}
	if data.Claims == nil {
		data.Claims = map[string]claimEntry{}
	}
	if data.Version == 0 {
		data.Version = 1
	}
	return data, nil
}

// write replaces the store atomically, in the shape `json.dump(indent=2,
// sort_keys=True)` produces — the format the file already has on disk, so a diff
// after a daemon claim shows one entry rather than a whole-file rewrite.
func (s *FileClaimStore) write(ctx context.Context, data claimFile) error {
	if dir := filepath.Dir(s.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return errors.Wrap(ctx, err, "create claim dir")
		}
	}
	tmp := s.Path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.Wrap(ctx, err, "create claim tmp")
	}
	encoded, err := encodeClaims(ctx, data)
	if err != nil {
		_ = out.Close()
		return err
	}
	if _, err := out.Write(encoded); err != nil {
		_ = out.Close()
		return errors.Wrap(ctx, err, "write claim tmp")
	}
	// fsync before the rename: without it a crash can leave the replaced file
	// empty while the rename already happened.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return errors.Wrap(ctx, err, "sync claim tmp")
	}
	if err := out.Close(); err != nil {
		return errors.Wrap(ctx, err, "close claim tmp")
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return errors.Wrap(ctx, err, "replace claim file")
	}
	return nil
}

// encodeClaims renders the store the way `ownership-claim.py` writes it:
// two-space indent, keys sorted, one trailing newline.
//
// ⚠️ The top level is a MAP, not the struct, so `encoding/json` sorts the keys —
// Python's `sort_keys=True` writes `claims` before `version`, and a struct would
// emit them in field order and rewrite the whole file on every claim.
func encodeClaims(ctx context.Context, data claimFile) ([]byte, error) {
	// ⚠️ The entries are maps too, not the struct: Python's `sort_keys=True` sorts
	// inside each entry (`claimed_at` first), and a struct would emit field order
	// and rewrite every entry line on each claim.
	claims := make(map[string]interface{}, len(data.Claims))
	for session, entry := range data.Claims {
		claims[session] = map[string]string{
			"session":    entry.Session,
			"manager":    entry.Manager,
			"note":       entry.Note,
			"claimed_at": entry.ClaimedAt,
		}
	}
	document := map[string]interface{}{
		"version": data.Version,
		"claims":  claims,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, errors.Wrap(ctx, err, "marshal claim file")
	}
	return append(encoded, '\n'), nil
}

// lock takes the store's sidecar lock, creating it when absent.
func (s *FileClaimStore) lock(ctx context.Context) (func(), error) {
	path := s.Path + ".lock"
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.Wrap(ctx, err, "create lock dir")
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "open claim lock")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, errors.Wrap(ctx, err, "lock claim file")
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
