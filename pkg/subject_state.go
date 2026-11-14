// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// SubjectStateReader answers the one subject question the wake guard consumes:
// is this session's manager date-gated asleep?
//
// It is a NARROW port of `manager-liveness.py --state`, not the whole classifier:
// the guard acts only on `SLEEPING` (refuse the wake) and `UNREADABLE` (fail
// closed), so those are the two states this reader can produce. Every other
// manager state — OK, STALE, STOPPED, UNARMED — is reported as the empty string,
// which the guard treats exactly as it treats a session with no subject record:
// the screen decides.
type SubjectStateReader interface {
	// State returns the subject state for a session.
	//
	// The second result reports whether a subject RESOLVED at all. A session with
	// no worker-manager record — a hand-opened tab — resolves nothing, and the
	// guard must then read the screen rather than assume a state either way.
	State(ctx context.Context, sessionID string) (state string, resolved bool, err error)
}

// NewSubjectStateReader creates a reader over the worker-manager records and the
// sweep-gate state directory.
//
// Both directories default to the paths the Python tools use, so a session
// recorded by `fleet-register.py` resolves here without a second registration.
func NewSubjectStateReader(
	workerManagerDir string,
	stateDir string,
	now libtime.CurrentDateTimeGetter,
) SubjectStateReader {
	return &subjectStateReader{workerManagerDir: workerManagerDir, stateDir: stateDir, now: now}
}

type subjectStateReader struct {
	workerManagerDir string
	stateDir         string
	now              libtime.CurrentDateTimeGetter
}

// workerManagerRecord is the part of a worker-manager record this reader needs.
type workerManagerRecord struct {
	Subject string `json:"subject"`
	Branch  string `json:"branch"`
}

// State returns the subject state, or the empty string when the subject is awake.
func (r *subjectStateReader) State(
	ctx context.Context,
	sessionID string,
) (string, bool, error) {
	if sessionID == "" {
		return "", false, nil
	}
	dir, err := r.workerDir(ctx)
	if err != nil {
		return "", false, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, sessionID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			// No record: no subject resolved. Not an error — a hand-opened tab has
			// none, and the guard must fall through to the screen.
			return "", false, nil
		}
		return "", false, errors.Wrapf(ctx, err, "read worker-manager record for %s", sessionID)
	}
	var record workerManagerRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		// A malformed record resolves no subject; the Python treats a record
		// without subject/branch the same way.
		return "", false, nil
	}
	if record.Subject == "" || record.Branch == "" {
		return "", false, nil
	}
	slug := record.Branch + "--" + baseSlug(record.Subject)
	stateDir, err := r.stateDirectory(ctx)
	if err != nil {
		return "", true, err
	}
	until, ok, err := readSleepMarker(ctx, stateDir, slug)
	if err != nil {
		return "", true, err
	}
	if ok && r.now.Now().Time().Before(until) {
		return SubjectStateSleeping, true, nil
	}
	return "", true, nil
}

// workerDir resolves the worker-manager record directory.
func (r *subjectStateReader) workerDir(ctx context.Context) (string, error) {
	if r.workerManagerDir != "" {
		return r.workerManagerDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".claude", "state", "worker-manager"), nil
}

// stateDirectory resolves the sweep-gate state directory.
func (r *subjectStateReader) stateDirectory(ctx context.Context) (string, error) {
	if r.stateDir != "" {
		return r.stateDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".claude", "state", "sweep-gate"), nil
}

// readSleepMarker returns the instant a `<slug>.sleeping` marker names.
//
// An absent, empty, or unparseable marker is reported as absent, never as a
// future instant: an unreadable sleep must not suppress a real lapse. The
// boolean is "a future instant was recorded", the instant is that instant.
func readSleepMarker(ctx context.Context, stateDir, slug string) (time.Time, bool, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, slug+".sleeping"))
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, errors.Wrapf(ctx, err, "read sleep marker %s", slug)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return time.Time{}, false, nil
	}
	until, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, false, nil
	}
	return until, true, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// baseSlug mirrors `manager-liveness.py` `base_slug`.
func baseSlug(subject string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(subject), "-"), "-")
}
