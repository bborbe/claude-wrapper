// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
)

// VaultCoverageIssue names one configured vault whose flagged rows no daemon
// serves: the vault, its family, and one flagged page as evidence.
type VaultCoverageIssue struct {
	Vault  string
	Family string
	Page   string
}

// CheckVaultCoverage reports every configured vault whose flagged rows sit in a
// vault family no daemon serves.
//
// The guard is per-FAMILY (see RefuseForeignVault), so "served" is a property of
// the family, not of one vault: a daemon named for one vault admits every vault
// sharing its prefix. A configured vault outside every served family can never
// have a row opened, and the failure is silent — every daemon reports healthy
// while the row stays dark for the whole window.
//
// Two inputs are refused rather than trusted, because either would let the check
// report "all clear" without having established anything:
//
//   - An empty served set. A check told nothing would pass on every vault.
//   - A served family that matches no configured vault. A typo (`privte`) reads as
//     "no vault is in this family", which is indistinguishable from a covered one.
func CheckVaultCoverage(
	ctx context.Context,
	vaults []Vault,
	servedFamilies []string,
) ([]VaultCoverageIssue, error) {
	served := make(map[string]bool, len(servedFamilies))
	for _, family := range servedFamilies {
		if trimmed := strings.TrimSpace(family); trimmed != "" {
			served[trimmed] = true
		}
	}
	if len(served) == 0 {
		return nil, errors.New(
			ctx,
			"no served vault family named — the check would pass on every vault, so it "+
				"is refused rather than answered",
		)
	}
	configured := make(map[string]bool, len(vaults))
	for _, vault := range vaults {
		configured[vaultFamily(vault.Name)] = true
	}
	for family := range served {
		if !configured[family] {
			return nil, errors.Wrapf(
				ctx,
				ErrUnknownVault,
				"served family %q matches no configured vault — likely a typo, and it "+
					"would read as covered rather than unserved",
				family,
			)
		}
	}
	issues := make([]VaultCoverageIssue, 0)
	for _, vault := range vaults {
		family := vaultFamily(vault.Name)
		if served[family] {
			continue
		}
		page, err := firstFlaggedPage(ctx, vault)
		if err != nil {
			return nil, err
		}
		if page == "" {
			continue
		}
		issues = append(issues, VaultCoverageIssue{
			Vault: vault.Name, Family: family, Page: page,
		})
	}
	return issues, nil
}

// firstFlaggedPage returns the first page in the vault whose `flag:` opens it,
// or "" when the vault holds none.
//
// Every kind is scanned, not only tasks: the flagged row source yields tasks,
// goals and topics alike, so a flagged goal in an unserved family would be missed
// by a task-only scan. The predicate is FlagSet, the same one the row source
// applies, so a spelling this check accepts cannot be one the tick refuses.
//
// An unreadable folder or file is an ERROR, never "nothing there". In an unserved
// family "I could not look" must not read as "no flagged row": the check exists
// precisely to prove the absence, so only a genuinely missing folder is skipped.
func firstFlaggedPage(ctx context.Context, vault Vault) (string, error) {
	root := strings.TrimSpace(vault.Path)
	if root == "" {
		return "", errors.Wrapf(
			ctx,
			ErrUnknownVault,
			"vault %q has no path, so its flagged rows cannot be checked",
			vault.Name,
		)
	}
	for _, kind := range kindDirs {
		dirs := kind.Dirs
		if kind.Kind == "task" {
			if tasksDir := strings.TrimSpace(vault.TasksDir); tasksDir != "" {
				dirs = []string{tasksDir}
			}
		}
		for _, folder := range dirs {
			page, err := firstFlaggedIn(ctx, filepath.Join(root, folder), folder, kind.Kind)
			if err != nil {
				return "", errors.Wrapf(ctx, err, "scan vault %q folder %q failed", vault.Name, folder)
			}
			if page != "" {
				return page, nil
			}
		}
	}
	return "", nil
}

// firstFlaggedIn returns the first flagged page in one folder, or "".
func firstFlaggedIn(ctx context.Context, dir, folder, kind string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", errors.Wrapf(ctx, err, "read dir %q failed", dir)
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return "", errors.Wrap(ctx, ctx.Err(), "vault coverage scan cancelled")
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return "", errors.Wrapf(ctx, readErr, "read %q failed", entry.Name())
		}
		if FlagSet(frontmatterBlock(string(raw)), kind) {
			return filepath.Join(folder, entry.Name()), nil
		}
	}
	return "", nil
}

// frontmatterBlock returns the text between the leading `---` markers, or "" when
// the file does not open with one.
//
// FlagSet matches `^flag:` in a whole-frontmatter block, so the block is what it
// must be handed — a body line reading `flag:` must never be mistaken for the
// frontmatter field.
func frontmatterBlock(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n")
		}
	}
	return ""
}
