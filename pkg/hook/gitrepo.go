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

// commonDir returns the directory shared by every worktree of the repo: the git
// dir itself in a main checkout, and the main repo's .git in a linked worktree.
func commonDir(git string) string {
	data, err := os.ReadFile(filepath.Join(git, "commondir")) //#nosec G304 -- a fixed name
	if err != nil {
		return git
	}
	target := strings.TrimSpace(string(data))
	if !filepath.IsAbs(target) {
		target = filepath.Join(git, target)
	}
	return filepath.Clean(target)
}

// isMainCheckout reports whether dir resolves to a repository whose git dir IS
// its common dir. A linked worktree, or no repository at all, is out of scope.
func isMainCheckout(dir string) (string, bool) {
	root, ok := repoRoot(dir)
	if !ok {
		return "", false
	}
	git, ok := gitDir(root)
	if !ok {
		return "", false
	}
	if git != commonDir(git) {
		return "", false
	}
	return root, true
}

// refExists reports whether name resolves to a commit in the repo at root: a
// local branch, a tag, a remote-tracking branch, or a hex object name.
func refExists(root string, name string) bool {
	git, ok := gitDir(root)
	if !ok {
		return false
	}
	common := commonDir(git)
	candidates := []string{
		"refs/heads/" + name,
		"refs/tags/" + name,
		name,
	}
	for _, ref := range candidates {
		if fileExists(filepath.Join(common, ref)) || fileExists(filepath.Join(git, ref)) {
			return true
		}
	}
	if packedRefsContain(filepath.Join(common, "packed-refs"), candidates) {
		return true
	}
	if packedRefsContain(filepath.Join(git, "packed-refs"), candidates) {
		return true
	}
	if remoteRefExists(filepath.Join(common, "refs", "remotes"), name) ||
		remoteRefExists(filepath.Join(git, "refs", "remotes"), name) {
		return true
	}
	return hexObjectName.MatchString(name)
}

// fileExists reports whether path is an existing file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// packedRefsContain reports whether any of refs appears in a packed-refs file.
func packedRefsContain(path string, refs []string) bool {
	data, err := os.ReadFile(path) //#nosec G304 -- a fixed name under a resolved git dir
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, ref := range refs {
			if fields[len(fields)-1] == ref {
				return true
			}
		}
	}
	return false
}

// remoteRefExists reports whether refs/remotes/<any>/<name> exists on disk.
func remoteRefExists(remoteDir string, name string) bool {
	entries, err := os.ReadDir(remoteDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if fileExists(filepath.Join(remoteDir, entry.Name(), name)) {
			return true
		}
	}
	return false
}
