// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bborbe/errors"
)

// nonSlug mirrors the reference's `re.sub(r"[^a-z0-9]+", "-", ...)`.
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a vault-authored subject name into a file-name component.
//
// It reproduces the reference's
// `re.sub(r"[^a-z0-9]+", "-", topic.lower()).strip("-")` exactly, then refuses a
// result that is empty, is `.` or `..`, or contains a path separator. This is the
// only thing between a vault-authored subject and a path, so a refusal here is a
// caller error rather than a silent fallback.
// RefuseUnsafeSubject refuses a subject that would traverse a path.
//
// It is the SINGLE HOME for this rule. Two functions join a subject into a path —
// Slug (state-dir files) and ResolveSubject (vault pages) — and validating in one
// only leaves the other open. That asymmetry was a real defect: Slug refused
// `../../x` while ResolveSubject joined it straight into `24 Goals/`, so a
// subject reaching ResolveSubject could read a file outside the vault.
func RefuseUnsafeSubject(ctx context.Context, subject string) error {
	if strings.Contains(subject, "..") || strings.ContainsAny(subject, `/\`) {
		return errors.Errorf(ctx, "subject %q contains a path traversal", subject)
	}
	return nil
}

func Slug(ctx context.Context, subject string) (string, error) {
	// Refuse a traversal attempt before slugging. The reference's slug would
	// flatten `../../etc/x` to `etc-x` and produce a safe name, but a subject that
	// carries a separator or a parent reference is a caller error, not a name to
	// normalise.
	if err := RefuseUnsafeSubject(ctx, subject); err != nil {
		return "", err
	}
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(subject), "-"), "-")
	if slug == "" || slug == "." || slug == ".." {
		return "", errors.Errorf(ctx, "subject %q does not slug to a safe file name", subject)
	}
	if strings.ContainsAny(slug, `/\`) || strings.ContainsRune(slug, filepath.Separator) {
		return "", errors.Errorf(ctx, "subject %q slugs to a path separator", subject)
	}
	return slug, nil
}

// StatePath is `<StateDir>/<slug>.json` — the stored digest record.
func StatePath(ctx context.Context, cfg Config, subject string) (string, error) {
	return sluggedPath(ctx, cfg.StateDir, subject, ".json")
}

// TickPath is `<StateDir>/<slug>.tick.txt` — the operator-facing frame.
func TickPath(ctx context.Context, cfg Config, subject string) (string, error) {
	return sluggedPath(ctx, cfg.StateDir, subject, ".tick.txt")
}

// CardPath is `<StateDir>/<slug>.card.json` — the card payload side file.
func CardPath(ctx context.Context, cfg Config, subject string) (string, error) {
	return sluggedPath(ctx, cfg.StateDir, subject, ".card.json")
}

// SettledPath is `<SettledDir>/<slug>.json` — the settled-ask memory.
func SettledPath(ctx context.Context, cfg Config, subject string) (string, error) {
	return sluggedPath(ctx, cfg.SettledDir, subject, ".json")
}

// CadencePath is `<StateDir>/<branch>--<slug>.cadence`.
//
// The branch prefix is load-bearing: a topic and a goal may share a name, and the
// two cadence records must not collide.
func CadencePath(ctx context.Context, cfg Config, branch, subject string) (string, error) {
	slug, err := Slug(ctx, subject)
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg.StateDir, branch+"--"+slug+".cadence"), nil
}

// sluggedPath joins a slugged subject and a suffix under a directory.
func sluggedPath(ctx context.Context, dir, subject, suffix string) (string, error) {
	slug, err := Slug(ctx, subject)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, slug+suffix), nil
}
