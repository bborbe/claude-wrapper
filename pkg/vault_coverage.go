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
// serves: the vault, its family, and one flagged task file as evidence.
type VaultCoverageIssue struct {
	Vault  string
	Family string
	Task   string
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
// An empty served set is an error, never a pass. A check that answers "all clear"
// because it was told nothing is worse than no check: it reads as evidence the
// coverage was verified.
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
	issues := make([]VaultCoverageIssue, 0)
	for _, vault := range vaults {
		family := vaultFamily(vault.Name)
		if served[family] {
			continue
		}
		task, err := firstFlaggedTask(ctx, vault)
		if err != nil {
			return nil, err
		}
		if task == "" {
			continue
		}
		issues = append(issues, VaultCoverageIssue{
			Vault: vault.Name, Family: family, Task: task,
		})
	}
	return issues, nil
}

// firstFlaggedTask returns the first task file in the vault's task folder whose
// frontmatter carries `flag: true`, or "" when the vault holds none.
//
// The configured `tasks_dir` is preferred, because a vault may place its tasks
// outside the three folder names the lister probes. When it names none, the
// lister's own folder list is used so this check and the row source cannot
// disagree about where a task lives.
func firstFlaggedTask(ctx context.Context, vault Vault) (string, error) {
	root := strings.TrimSpace(vault.Path)
	if root == "" {
		return "", nil
	}
	dirs := taskFolders
	if tasksDir := strings.TrimSpace(vault.TasksDir); tasksDir != "" {
		dirs = []string{tasksDir}
	}
	for _, folder := range dirs {
		dir := filepath.Join(root, folder)
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A vault with no task folder holds no flagged row; the question is
			// "is one here", not "is every vault well formed".
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
			if readErr != nil {
				continue
			}
			if frontmatterValue(string(raw), "flag") == "true" {
				return filepath.Join(folder, entry.Name()), nil
			}
		}
	}
	return "", nil
}
