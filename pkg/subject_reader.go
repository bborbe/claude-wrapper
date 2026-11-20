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

	"github.com/bborbe/errors"
)

// standdownBranches is every tier a manager can have been stood down as.
//
// ⚠️ All four are checked, because the exited session is a MANAGER and a manager
// can have been stopped as a goal, a topic, a fleet subject or a task. A
// same-named subject on another tier therefore also skips this one, and that is
// the deliberate direction: over-skipping costs an adoption the next round
// repeats, under-skipping costs the operator's decision.
var standdownBranches = []string{"goal", "topic", "fleet", "task"}

// FileSubjectReader is the SubjectReader backed by the on-disk stores.
type FileSubjectReader struct {
	// SubjectsDir holds `<session-id>.json`, each recording its session's subject.
	SubjectsDir string
	// StateDir is the sweep-gate state dir where `<branch>--<slug>.stopped` lives.
	StateDir string
}

// NewFileSubjectReader builds the reader, defaulting both dirs.
func NewFileSubjectReader(subjectsDir, stateDir string) *FileSubjectReader {
	return &FileSubjectReader{
		SubjectsDir: subjectsDir,
		StateDir:    stateDir,
	}
}

// Subject returns the subject sessionID recorded, folded, or "" when it recorded none.
//
// ⚠️ A malformed record is "no subject", never an error: the subject file is
// written by another process and a half-written one must not stop the tick. The
// caller's fail-closed guard covers the read error it does return.
func (r *FileSubjectReader) Subject(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" || r.SubjectsDir == "" {
		return "", nil
	}
	raw, err := os.ReadFile(filepath.Join(r.SubjectsDir, sessionID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", errors.Wrap(ctx, err, "read subject record")
	}
	var record map[string]interface{}
	if err := json.Unmarshal(raw, &record); err != nil {
		return "", nil
	}
	subject, _ := record["subject"].(string)
	return normaliseSubject(subject), nil
}

// Stopped returns the marker standing subject down, and whether one exists.
func (r *FileSubjectReader) Stopped(
	ctx context.Context,
	subject string,
) (string, bool, error) {
	if subject == "" || r.StateDir == "" {
		return "", false, nil
	}
	folded := baseSlug(subject)
	for _, branch := range standdownBranches {
		name := branch + "--" + folded + ".stopped"
		// ⚠️ Only "not found" means "not stopped". Any other stat error — an
		// unreadable or unsearchable StateDir — is returned, so the caller's guard
		// fails CLOSED. Swallowing it here would read as "no marker" and adopt a
		// worker whose subject the operator explicitly stood down.
		_, err := os.Stat(filepath.Join(r.StateDir, name))
		if err == nil {
			return name, true, nil
		}
		if !os.IsNotExist(err) {
			return "", false, errors.Wrapf(ctx, err, "stat stand-down marker %s", name)
		}
	}
	return "", false, nil
}

// normaliseSubject folds a subject for comparison: casefolded and
// whitespace-collapsed, exactly as `adopt-orphans.py`'s `normalise` does.
func normaliseSubject(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}
