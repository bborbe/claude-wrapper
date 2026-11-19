// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

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
	for _, entry := range live {
		if entry.Name == row.Name {
			return true, nil
		}
	}
	return false, nil
}
