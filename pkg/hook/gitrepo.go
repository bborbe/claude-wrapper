// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"path/filepath"
	"strings"
)

// repoRoot returns the working-tree root for dir: the nearest ancestor holding a
// .git entry, whether that is a directory or a worktree pointer file.
func repoRoot(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	current := abs
	for {
		if _, statErr := os.Lstat(filepath.Join(current, ".git")); statErr == nil {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

// gitDir returns the git directory for the repo at root, following a worktree's
// pointer file.
func gitDir(root string) (string, bool) {
	entry := filepath.Join(root, ".git")
	info, err := os.Stat(entry)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return entry, true
	}
	data, err := os.ReadFile(entry) //#nosec G304 -- a fixed name under a resolved repo root
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(data))
	target, found := strings.CutPrefix(line, "gitdir:")
	if !found {
		return "", false
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	return filepath.Clean(target), true
}

// currentBranch returns the branch HEAD points at, or the empty string for a
// detached HEAD or an unreadable repository.
func currentBranch(dir string) string {
	root, ok := repoRoot(dir)
	if !ok {
		return ""
	}
	git, ok := gitDir(root)
	if !ok {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(git, "HEAD")) //#nosec G304 -- a fixed name
	if err != nil {
		return ""
	}
	branch, found := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: refs/heads/")
	if !found {
		return ""
	}
	return branch
}

// remoteURL returns the push URL configured for remote in the repo at root.
func remoteURL(root string, remote string) string {
	git, ok := gitDir(root)
	if !ok {
		return ""
	}
	for _, path := range []string{filepath.Join(git, "config"), filepath.Join(git, "commondir")} {
		data, err := os.ReadFile(path) //#nosec G304 -- a fixed name
		if err != nil {
			continue
		}
		if url := parseRemoteURL(string(data), remote); url != "" {
			return url
		}
	}
	return ""
}

// parseRemoteURL reads one remote's url out of a git config body.
func parseRemoteURL(config string, remote string) string {
	inSection := false
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inSection = trimmed == `[remote "`+remote+`"]`
			continue
		}
		if !inSection {
			continue
		}
		if value, found := strings.CutPrefix(trimmed, "url"); found {
			value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "="))
			if value != "" {
				return value
			}
		}
	}
	return ""
}
