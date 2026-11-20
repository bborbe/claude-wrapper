// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "context"

// KeepalivePageSource enumerates the raw pages (with frontmatter) for one kind
// across the filtered vaults. The filesystem listing (resolve-folders + page reads)
// lives behind it, so the lister is testable without a vault on disk.
type KeepalivePageSource func(ctx context.Context) ([]KeepalivePage, error)

// KeepaliveLister selects the pages a procedure examines this tick and persists the
// cursor. It ties the three pure pieces together: FilterCandidates (live population)
// → RotatePages (per-vault cap + rotating cursor) → KeepaliveCursorStore (persistence).
type KeepaliveLister struct {
	Source KeepalivePageSource
	Cursor KeepaliveCursorStore
	// Kind is KeepaliveKindTopic (row 1) or KeepaliveKindTask (row 6).
	Kind string
	// Cap is the pages examined PER VAULT per tick (20).
	Cap int
}

// Select loads the cursor, enumerates, filters to the live population, and rotates.
// It returns the pages to examine this tick (up to Cap per vault, starting after
// each vault's cursor).
func (l *KeepaliveLister) Select(ctx context.Context) ([]KeepalivePageRef, error) {
	cursor, err := l.Cursor.Load(ctx)
	if err != nil {
		return nil, err
	}
	pages, err := l.Source(ctx)
	if err != nil {
		return nil, err
	}
	refs := FilterCandidates(pages, l.Kind)
	selected, _ := RotatePages(cursor, refs, l.Cap)
	return selected, nil
}

// Advance persists the cursor to the last page EVALUATED per vault — the procedure
// tracks it (via its `onEvaluated` callback) and calls this when it stops, whether
// because it acted or ran the whole selection. Passing the last-EVALUATED (not
// last-selected) map is what stops an early act from starving the pages behind it.
func (l *KeepaliveLister) Advance(ctx context.Context, lastEvaluated map[string]string) error {
	return l.Cursor.Save(ctx, lastEvaluated)
}
