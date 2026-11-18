// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bborbe/errors"
)

// osc8Re matches an OSC 8 hyperlink sequence.
var osc8Re = regexp.MustCompile("\x1b\\]8;[^\x07\x1b]*(?:\x07|\x1b\\\\)")

// BusyStamp is the cross-tick busy clock the state file carries.
type BusyStamp struct {
	Since    float64 `json:"since"`
	Progress string  `json:"progress"`
}

// State is the stored digest record.
//
// RecordedAt is stamped by the caller from the injected clock (cfg.Now), in local
// time with seconds precision. The state-file formats are frozen for the
// switchover: renaming a field costs a spurious change on the first tick and a
// lost digest.
type State struct {
	Topic      string               `json:"topic"`
	Digest     string               `json:"digest"`
	Members    int                  `json:"members"`
	BusySince  map[string]BusyStamp `json:"busy_since"`
	RecordedAt string               `json:"recorded_at"`
}

// Settled is the settled-ask memory for one subject.
type Settled struct {
	Subject    string `json:"subject"`
	RowKey     string `json:"row_key"`
	RecordedAt string `json:"recorded_at"`
}

// LoadState returns the raw stored payload, or the zero State when it cannot be
// read or parsed. It is used for the cross-tick busy-since map.
func LoadState(ctx context.Context, path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}, nil
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, nil
	}
	return state, nil
}

// LoadStored returns the stored digest and, when the state cannot be trusted, a
// non-empty fail reason that forces a full sweep.
//
// A missing file, an unreadable file, a parse error and an object with no digest
// key each return a fail reason and no error.
func LoadStored(ctx context.Context, path string) (digest string, failReason string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "first run — no state file", nil
		}
		return "", fmt.Sprintf("state unreadable (%v)", err), nil
	}
	var data any
	if unmarshalErr := json.Unmarshal(raw, &data); unmarshalErr != nil {
		return "", fmt.Sprintf("state parse error (%v)", unmarshalErr), nil
	}
	object, ok := data.(map[string]any)
	if !ok {
		return "", "state parse error (no digest key)", nil
	}
	value, ok := object["digest"]
	if !ok {
		return "", "state parse error (no digest key)", nil
	}
	return stringify(value), "", nil
}

// SaveStored writes the state atomically.
func SaveStored(ctx context.Context, path string, state State) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal state for %s failed", path)
	}
	return writeFileAtomic(ctx, path, encoded)
}

// LoadSettled returns the recorded ask identity, or the zero Settled when the
// record is missing or half-written. That is "no memory", which re-posts — the
// safe direction is a re-post, never a silence.
func LoadSettled(ctx context.Context, path string) (Settled, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settled{}, nil
	}
	var settled Settled
	if err := json.Unmarshal(raw, &settled); err != nil {
		return Settled{}, nil
	}
	return settled, nil
}

// SaveSettled writes the settled-ask record atomically.
func SaveSettled(ctx context.Context, path string, settled Settled) error {
	encoded, err := json.MarshalIndent(settled, "", "  ")
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal settled record for %s failed", path)
	}
	return writeFileAtomic(ctx, path, encoded)
}

// WriteTickFile writes the link-free frame to the tick file atomically.
//
// The file is mode 0600 at creation, never a chmod after the write, so it is
// never world-readable even for an instant. Trailing newlines are normalised to
// exactly one.
func WriteTickFile(ctx context.Context, path, frame string) error {
	content := strings.TrimRight(StripLinks(frame), "\n") + "\n"
	return writeFileAtomic(ctx, path, []byte(content))
}

// WriteCardFile writes the card payload, or REMOVES the file when payload is nil.
//
// Removal rather than an empty file: a stale payload would re-post an answered
// card. A missing file is not an error.
func WriteCardFile(ctx context.Context, path string, payload []byte) error {
	if payload == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return errors.Wrapf(ctx, err, "remove card file %s failed", path)
		}
		return nil
	}
	return writeFileAtomic(ctx, path, payload)
}

// StripLinks drops OSC 8 hyperlink escapes, keeping the text they wrapped.
func StripLinks(text string) string {
	return osc8Re.ReplaceAllString(text, "")
}

// writeFileAtomic writes to `<path>.tmp` and renames it into place.
func writeFileAtomic(ctx context.Context, path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create directory for %s failed", path)
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.Wrapf(ctx, err, "open %s failed", tmp)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return errors.Wrapf(ctx, err, "write %s failed", tmp)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(ctx, err, "close %s failed", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(ctx, err, "rename %s failed", path)
	}
	return nil
}

// stringify renders a decoded JSON value the way Python's str() would for the
// scalar shapes a digest can hold.
func stringify(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
