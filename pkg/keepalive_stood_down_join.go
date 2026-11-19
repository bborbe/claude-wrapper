// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// StoodDownCandidate is one `<slug>` row `manager-liveness.py --stood-down-past`
// reported.
type StoodDownCandidate struct {
	// Slug is the marker's name, `<branch>--<base>`.
	Slug string
	// Branch is the part before `--`.
	Branch string
	// Base is the part after `--`.
	Base string
}

// ParseStoodDownCandidates reads `manager-liveness.py --stood-down-past`'s rows.
//
// Each row is a tab-indented `<slug>`. ⚠️ **`task--` slugs are dropped here, not
// later.** This leg closes MANAGERS, never workers: a `task--<base>` marker is
// written by `/supervisor:stop "<task>"`, and the LIVE test downstream passes on
// it *by construction* — the guard that wrote it required the caller to be
// exactly the session the page records. Consuming one would turn a deliberate
// stand-down into a close.
func ParseStoodDownCandidates(ctx context.Context, output string) ([]StoodDownCandidate, error) {
	candidates := []StoodDownCandidate{}
	for _, line := range strings.Split(output, "\n") {
		slug := strings.TrimSpace(line)
		if slug == "" || strings.ContainsAny(slug, " \t") {
			// Not a slug row — the script's own prose, a header, or a summary.
			continue
		}
		branch, base, found := strings.Cut(slug, "--")
		if !found || branch == "" || base == "" {
			return nil, errors.Errorf(ctx, "stood-down row is not <branch>--<base>: %q", slug)
		}
		if branch == "task" {
			continue
		}
		candidates = append(candidates, StoodDownCandidate{Slug: slug, Branch: branch, Base: base})
	}
	return candidates, nil
}

// StoodDownSessionsToClose joins candidates to their pages and keeps only those
// whose recorded session is LIVE.
//
// ⚠️ **The marker is not the population.** Measured 2026-11-09: of 83 `.stopped`
// markers, 41 sat on pages still holding a `claude_session_id` and only 2 had a
// session that read LIVE. The marker outlives the session it describes, so a
// selector's answer is a candidate list — and closing on it directly would target
// 41 sessions, 39 of them already gone.
//
// `sessionFor` returns the page's recorded `claude_session_id` (empty when it
// records none); `live` answers whether that session is running.
func StoodDownSessionsToClose(
	ctx context.Context,
	candidates []StoodDownCandidate,
	sessionFor func(ctx context.Context, base string) (string, error),
	live func(ctx context.Context, sessionID string) (bool, error),
) ([]string, error) {
	if sessionFor == nil || live == nil {
		return nil, errors.New(ctx, "stood-down join needs a page read and a liveness check")
	}
	toClose := []string{}
	for _, candidate := range candidates {
		sessionID, err := sessionFor(ctx, candidate.Base)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "resolve page %s failed", candidate.Base)
		}
		if sessionID == "" {
			// The page records no session: nothing to close, and the marker simply
			// outlived it.
			continue
		}
		isLive, err := live(ctx, sessionID)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "read liveness of %s failed", sessionID)
		}
		if !isLive {
			// A dead session needs no close — the marker outlived it.
			continue
		}
		toClose = append(toClose, sessionID)
	}
	return toClose, nil
}
