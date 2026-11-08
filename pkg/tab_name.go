// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
)

// tabNameGlyph prefixes every tab title the wrapper sets.
//
// The glyph is the join key: the registry records the tab title as the session's
// `name`, and the title is the ONLY thing a spawned tab tells us about the
// session it became. A title without the glyph is not one of ours.
const tabNameGlyph = "⚙"

// TabNameFor returns the base tab name for a label.
func TabNameFor(label string) string {
	return tabNameGlyph + " " + label
}

// UniqueTabName returns a tab name no live registry entry currently holds.
//
// ⚠️ Not what a spawn should call: it reads the registry but reserves nothing, so
// two concurrent spawns of one label both get the same answer. The spawn path
// uses `tabSpawner.reserveTabName`, which holds the name until its spawn has
// resolved an identity. This is the pure chooser, kept for the naming rule.
//
// The name is the join back to a session id — a tab worker never reports its own
// id — so two workers sharing a title make the join ambiguous and the wrong
// session id gets written to the ledger. `⚙ foo`, then `⚙ foo (2)`, `(3)`, …
//
// An unreadable registry returns baseName unchanged rather than an error, which
// matches the node server: the caller still holds the `preexisting` exclusion
// snapshot, so a collision is survivable, and refusing to spawn because a
// read-only probe failed would be a worse outcome than an untidy title.
func UniqueTabName(ctx context.Context, registry SessionRegistry, baseName string) string {
	held, err := registry.Named(ctx, baseName)
	if err != nil || len(held) == 0 {
		return baseName
	}
	for n := 2; n < 100; n++ {
		candidate := fmt.Sprintf("%s (%d)", baseName, n)
		ids, err := registry.Named(ctx, candidate)
		if err != nil {
			return baseName
		}
		if len(ids) == 0 {
			return candidate
		}
	}
	return baseName
}

// PreexistingHolders snapshots the session ids already registered under a tab
// name, taken AFTER the name is chosen and BEFORE the process starts.
//
// This is the race guard, not the naming rule: a session that registers under
// the chosen name between the snapshot and the poll is not ours, so the poll
// must not adopt it. Without it a slow register from a previous spawn is
// indistinguishable from this one's.
func PreexistingHolders(ctx context.Context, registry SessionRegistry, tabName string) map[string]struct{} {
	ids, err := registry.Named(ctx, tabName)
	if err != nil {
		return map[string]struct{}{}
	}
	held := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		held[id] = struct{}{}
	}
	return held
}

// FindRegisteredByName returns the session id registered under name that is not
// in exclude, or "" when none is.
//
// One read, no polling: the caller owns the deadline, exactly as the node
// server's `findRegisteredByName` does.
func FindRegisteredByName(
	ctx context.Context,
	registry SessionRegistry,
	name string,
	exclude map[string]struct{},
) (string, error) {
	ids, err := registry.Named(ctx, name)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		if _, skip := exclude[id]; skip {
			continue
		}
		return id, nil
	}
	return "", nil
}
