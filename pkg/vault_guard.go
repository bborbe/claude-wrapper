// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
)

// DaemonVault is the vault a claude-wrapper daemon serves.
//
// A daemon serves exactly one vault FAMILY — the vault it was launched for and
// every vault sharing that family's prefix (`private`, `seibert`, `assistant`).
// Family members share one launcher script and one manager population, so a
// row from another family belongs to a different daemon: its parks would land on
// a daemon whose manager never queries them, and the worker would exit with its
// prompt unanswered.
//
// Name and Path are both optional; an empty DaemonVault (neither set) disables
// the guard, which is the only safe default for a daemon that was started
// without naming a vault.
type DaemonVault struct {
	Name string
	Path string
}

// Enabled reports whether the daemon named a vault, and so whether the guard runs.
func (d DaemonVault) Enabled() bool {
	return strings.TrimSpace(d.Name) != "" || strings.TrimSpace(d.Path) != ""
}

// Family returns the vault family, the vault name up to its first `-`.
func (d DaemonVault) Family() string {
	return vaultFamily(d.Name)
}

// ResolveDaemonVault resolves the vault a daemon serves from its flags.
//
// An explicit `-vault` name wins. Otherwise the `-sweep-gate-vault` path is
// matched against the configured vault list, because both production daemons
// already name their own vault there. An unreadable vault list falls back to the
// path's basename as the name, so the family still resolves. Neither flag set
// returns the zero DaemonVault, which disables the guard.
func ResolveDaemonVault(
	ctx context.Context,
	vaults VaultLister,
	name string,
	sweepPath string,
) DaemonVault {
	name = strings.TrimSpace(name)
	sweepPath = strings.TrimSpace(sweepPath)
	if name == "" && sweepPath == "" {
		return DaemonVault{}
	}
	var list []Vault
	if vaults != nil {
		if read, err := vaults.List(ctx); err == nil {
			list = read
		}
	}
	if name != "" {
		for _, vault := range list {
			if vault.Name == name {
				return DaemonVault{Name: vault.Name, Path: vault.Path}
			}
		}
		return DaemonVault{Name: name}
	}
	for _, vault := range list {
		if vault.Path != "" && sameVaultPath(vault.Path, sweepPath) {
			return DaemonVault{Name: vault.Name, Path: vault.Path}
		}
	}
	return DaemonVault{Name: filepath.Base(sweepPath), Path: sweepPath}
}

// sameVaultPath reports whether two paths name the same directory, resolving
// symlinks first. The Obsidian vaults are routinely opened through a symlink and
// `/tmp` is `/private/tmp` on macOS, so a string comparison would call the same
// vault foreign.
func sameVaultPath(a, b string) bool {
	resolvedA, errA := resolvePath(a)
	resolvedB, errB := resolvePath(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return resolvedA == resolvedB
}

// RefuseForeignVault refuses a target vault outside the daemon's own vault
// family, naming both vaults it found in conflict.
//
// It is the mechanical half of claude-supervisor `fleet-surface.md` § Spawn a
// worker's world precondition: the vault resolved from the subject and the vault
// the task file lives in must be the same, and a mismatch is refused rather than
// spawned. The guard is disabled when the daemon named no vault; the same vault
// by path is always allowed; otherwise the target's family must match the
// daemon's family.
func RefuseForeignVault(
	ctx context.Context,
	own DaemonVault,
	targetName string,
	targetPath string,
) error {
	if !own.Enabled() {
		return nil
	}
	targetName = strings.TrimSpace(targetName)
	targetPath = strings.TrimSpace(targetPath)
	if targetName == "" && targetPath == "" {
		return nil
	}
	if own.Path != "" && targetPath != "" && sameVaultPath(own.Path, targetPath) {
		return nil
	}
	ownFamily := own.Family()
	targetFamily := vaultFamily(targetName)
	if targetName != "" && ownFamily != "" && targetFamily == ownFamily {
		return nil
	}
	target := targetName
	if target == "" {
		target = targetPath
	}
	return errors.Wrapf(
		ctx,
		ErrForeignVault,
		"refusing to open a row from vault %q (family %q): this daemon serves vault %q "+
			"(family %q), and a daemon opens only its own vault family — open it through a "+
			"daemon of that family",
		target,
		targetFamily,
		own.Name,
		ownFamily,
	)
}
