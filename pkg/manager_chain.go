// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// ManagerChainProbe answers whether a goal or topic already has a live manager
// session, so the flagged tick can tell "no manager exists" from "a manager is
// live, or the probe could not read its store".
//
// The eligibility predicate cannot answer this: for a goal or topic it returns
// `create` whenever the manager chain is live OR unreadable, and it routes to
// `open-manager` only on a proven-dead chain. A never-opened goal or topic (no
// session at all) therefore arrives on the same `create` branch as a live one,
// and opening it blind would start a duplicate manager beside a live manager.
type ManagerChainProbe interface {
	// ChainLive reports whether a live session claims the row's goal or topic.
	// An error means the probe could not read its store — the caller must treat
	// that as unreadable and skip, never as "nothing is live".
	ChainLive(ctx context.Context, row FlaggedRow) (bool, error)
}

// registryChainProbe answers ChainLive from Claude Code's session registry.
//
// The join is the tab title: every flagged-row spawn records the row's own name
// as its label (see flaggedReopener.Reopen and SpawnOptions.Label), and the
// registry carries that title as RegistryEntry.Name. A live entry whose Name
// equals the row's Name is a live manager for that goal or topic.
type registryChainProbe struct {
	Registry SessionRegistry
}

// NewRegistryChainProbe creates a ManagerChainProbe over the session registry.
func NewRegistryChainProbe(registry SessionRegistry) ManagerChainProbe {
	return &registryChainProbe{Registry: registry}
}

// ChainLive reports whether a live registry entry carries the row's name.
//
// ⚠️ Live is pid-checked, so a stale file left by a crashed session does not
// read as a live manager — the one false positive that would suppress a real
// open. An unreadable registry is returned as an error, never as false.
func (p *registryChainProbe) ChainLive(ctx context.Context, row FlaggedRow) (bool, error) {
	if p.Registry == nil {
		return false, errors.Errorf(ctx, "no session registry is wired")
	}
	live, err := p.Registry.Live(ctx)
	if err != nil {
		return false, err
	}
	// The registry records the TAB TITLE, not the row name: `TabNameFor` prefixes
	// `⚙ ` and `UniqueTabName` appends ` (n)` on a collision. Joining on the raw
	// row name would never match a live tab-spawned manager, so the tick would
	// open a duplicate — the exact hazard this probe exists to prevent.
	name := strings.TrimSpace(row.Name)
	// Two conventions name a goal/topic manager tab: the flagged tick sets
	// `⚙ <name>` (TabNameFor), and the open endpoint sets `<name> Manager`
	// (open_decision.go's `verdict.Name+" Manager"`, open_tab.go's openTabName).
	// Matching only one misses a manager opened by the other path — and a live
	// manager is exactly what this probe exists to detect, so the miss would open
	// a duplicate. The ` (n)` collision suffix is handled by matchesTabName.
	for _, base := range []string{TabNameFor(name), name + " Manager"} {
		for _, entry := range live {
			if matchesTabName(entry.Name, base) {
				return true, nil
			}
		}
	}
	return false, nil
}

// matchesTabName reports whether a registry name is the base tab name, or the
// base with a ` (n)` collision suffix.
func matchesTabName(entryName, base string) bool {
	if entryName == base {
		return true
	}
	prefix := base + " ("
	if !strings.HasPrefix(entryName, prefix) || !strings.HasSuffix(entryName, ")") {
		return false
	}
	return allDigits(entryName[len(prefix) : len(entryName)-1])
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
