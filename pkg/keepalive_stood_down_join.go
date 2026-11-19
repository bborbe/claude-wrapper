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
// ⚠️ **Each row is `<age-seconds>\t<slug>`, not a bare slug.** The script's own
// emitting line is `print(f"{int(age)}\t{s}")`, so a parser that skips lines
// containing a tab drops EVERY row and leaves this leg reporting `none` forever —
// the failure mode is silent, because a fixture built on the wrong shape stays
// green. The age is discarded here; it is the script's ordering key, not part of
// the slug.
//
// ⚠️ **`task--` slugs are dropped here, not later.** This leg closes MANAGERS,
// never workers: a `task--<base>` marker is written by `/supervisor:stop "<task>"`,
// and the LIVE test downstream passes on it *by construction* — the guard that
// wrote it required the caller to be exactly the session the page records.
// Consuming one would turn a deliberate stand-down into a close.
func ParseStoodDownCandidates(ctx context.Context, output string) ([]StoodDownCandidate, error) {
	candidates := []StoodDownCandidate{}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// The age column is separated by a TAB; everything after the first tab is
		// the slug. A line with no tab is prose or a summary and is skipped.
		_, slug, found := strings.Cut(trimmed, "\t")
		if !found {
			continue
		}
		slug = strings.TrimSpace(slug)
		if slug == "" || strings.ContainsAny(slug, " \t") {
			continue
		}
		branch, base, split := strings.Cut(slug, "--")
		if !split || branch == "" || base == "" {
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
