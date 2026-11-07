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

// The three roles a spawn may declare.
const (
	RoleManager = "manager"
	RoleAgent   = "agent"
	RoleHuman   = "human"
)

// DefaultRole is the role an absent or empty `role` argument resolves to.
const DefaultRole = RoleAgent

// rolePurposes maps a role to the role-map key that carries its window and chip.
//
// The purpose is the map's own vocabulary, not the role's: the operator names
// windows "Managers" and "Agents", and the map is edited by hand.
var rolePurposes = map[string]string{
	RoleManager: "Managers",
	RoleAgent:   "Agents",
	RoleHuman:   "Direct",
}

// familyChips maps a vault's family to the chip a human-role spawn takes.
//
// A human spawn is the operator's own work, so it is coloured by which vault it
// belongs to rather than by a role — two humans in two vaults are two people's
// work, and one colour for both loses exactly the distinction the colour exists
// to carry.
var familyChips = map[string]string{
	"seibert": "green",
	"private": "cyan",
}

// RoleResolution is the window and chip a role resolves to.
type RoleResolution struct {
	Role string
	// Purpose is the role-map key the role resolved to.
	Purpose string
	// Chip is the `/color` argument, or "" when the role map gave none.
	//
	// Empty is a real answer, not a missing one: the node server sends NO colour
	// for an unresolved role rather than defaulting to one, because a wrong role
	// signal is worse than a missing one — painting every worker the agent colour
	// is how a manager came up indistinguishable from its workers.
	Chip string
	// WindowID is the WezTerm window the tab opens in, or nil when unset.
	//
	// A pointer rather than a sentinel because 0 is a legal window id.
	WindowID *int
	// Resolved reports whether the role map actually carried this role. A false
	// value means the caller's explicit window and chip stand alone.
	Resolved bool
}

// RoleMapReader reads the WezTerm role map.
//
// Read on every spawn rather than cached: the operator edits this file precisely
// because the next spawn should differ from the last.
type RoleMapReader interface {
	// Read returns the role map, keyed by purpose.
	Read(ctx context.Context) (map[string]RoleMapEntry, error)
}

// RoleMapEntry is one purpose's window and chip.
type RoleMapEntry struct {
	Chip     *string `json:"chip"`
	WindowID *int    `json:"window_id"`
}

// NewRoleMapReader creates a RoleMapReader over path.
//
// path defaults to ~/.cache/wezterm-role-map.json when empty.
func NewRoleMapReader(path string) RoleMapReader {
	return &roleMapReader{path: path}
}

type roleMapReader struct {
	path string
}

func (r *roleMapReader) resolvePath(ctx context.Context) (string, error) {
	if r.path != "" {
		return r.path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".cache", "wezterm-role-map.json"), nil
}

// Read returns the role map, or an empty map when the file is absent.
//
// An absent role map is not an error: a machine that never configured one gets
// uncoloured, default-window tabs, which is a working spawn. A file that exists
// but does not parse IS an error, because silently falling back there would hide
// a typo the operator made in the file they just edited.
func (r *roleMapReader) Read(ctx context.Context) (map[string]RoleMapEntry, error) {
	path, err := r.resolvePath(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]RoleMapEntry{}, nil
		}
		return nil, errors.Wrapf(ctx, err, "read role map %q failed", path)
	}
	entries := map[string]RoleMapEntry{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errors.Wrapf(ctx, err, "parse role map %q failed", path)
	}
	return entries, nil
}

// ResolveRole resolves a role argument against the role map.
//
// An unknown role is an error, never a fallback to the default: a worker opened
// in the wrong window with the wrong colour is discovered only by noticing it,
// so guessing here trades a refusal the caller can read for a defect they must
// spot.
func ResolveRole(ctx context.Context, reader RoleMapReader, role string) (*RoleResolution, error) {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		trimmed = DefaultRole
	}
	purpose, ok := rolePurposes[trimmed]
	if !ok {
		return nil, errors.Wrapf(
			ctx,
			ErrUnknownRole,
			"%q is not a role — refusing to spawn rather than guessing, since a worker "+
				"opened in the wrong window with the wrong colour is discovered only by "+
				"noticing it. Valid values: manager, agent, human (or omit the argument "+
				"for %q)",
			trimmed,
			DefaultRole,
		)
	}
	entries, err := reader.Read(ctx)
	if err != nil {
		return nil, err
	}
	resolution := &RoleResolution{Role: trimmed, Purpose: purpose}
	entry, ok := entries[purpose]
	if !ok {
		return resolution, nil
	}
	if entry.Chip != nil && *entry.Chip != "" {
		resolution.Chip = *entry.Chip
	}
	resolution.WindowID = entry.WindowID
	resolution.Resolved = true
	return resolution, nil
}

// ChipForRole returns the `/color` argument for a resolution, or "" for none.
//
// A human spawn takes its vault family's chip in place of the role's, falling
// back to the role chip when the family is not one this wrapper knows.
func ChipForRole(resolution *RoleResolution, vault string) string {
	if resolution == nil {
		return ""
	}
	if resolution.Role != RoleHuman {
		return resolution.Chip
	}
	family := vaultFamily(vault)
	if chip, ok := familyChips[family]; ok {
		return chip
	}
	return resolution.Chip
}

// vaultFamily returns the part of a vault name before its first dash.
func vaultFamily(vault string) string {
	if index := strings.Index(vault, "-"); index > 0 {
		return vault[:index]
	}
	return vault
}
