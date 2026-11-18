// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// gitReadOnlySubcommands are the subcommands that only read.
var gitReadOnlySubcommands = map[string]bool{
	"show": true, "log": true, "diff": true, "grep": true, "ls-tree": true,
	"rev-parse": true, "status": true, "cat-file": true, "blame": true,
	"ls-files": true, "merge-base": true, "rev-list": true, "describe": true,
	"ls-remote": true,
}

var (
	// gitBadArg matches a short or long option that reads or executes something the
	// shape guard cannot bound.
	gitBadArg = regexp.MustCompile(
		`^(-c|-O.*|--open-files-in-pager.*|--output.*|--ext-diff|--textconv|--exec.*` +
			`|--config.*|--git-dir.*|--work-tree.*|--upload-pack.*|--receive-pack.*` +
			`|--paginate|--no-index|--namespace.*|--super-prefix.*|--batch-command.*)$`,
	)
	// gitTagOption matches the value-taking and formatting options of `git tag` in
	// list mode.
	gitTagOption = regexp.MustCompile(`^(--sort=[\w:-]+|-n\d*|--column|--no-column|--ignore-case)$`)
)

// gitBadLongPrefixes are the long options refused by unique prefix too, because git
// accepts any unique prefix.
var gitBadLongPrefixes = []string{
	"output", "open-files-in-pager", "ext-diff", "textconv", "filters", "exec-path",
	"exec", "config", "config-env", "git-dir", "work-tree", "upload-pack",
	"receive-pack", "paginate", "no-index", "namespace", "super-prefix",
	"batch-command", "contents",
}

// gitTagListFlags are the `git tag` flags that select list mode.
var gitTagListFlags = map[string]bool{
	"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true,
	"--points-at": true, "--list": true, "-l": true,
}

// gitTagValueFlags are the list flags that consume the next argument.
var gitTagValueFlags = map[string]bool{
	"--contains": true, "--no-contains": true, "--merged": true, "--no-merged": true,
	"--points-at": true,
}

// gitWorktreeListFlags are the only flags `git worktree list` accepts.
var gitWorktreeListFlags = map[string]bool{
	"--porcelain": true, "-v": true, "--verbose": true, "-z": true,
}

// GitShapeAllows reports whether `git [flags] <subcommand> <args>` is a read-only
// invocation under roots. Every word must be static; the -C operand, when present,
// must resolve under one of roots.
func GitShapeAllows(values []string, cwd string, roots []string) bool {
	index := 0
	if len(values) > 0 && values[0] == "-C" {
		if len(values) < 2 || !UnderRoots(values[1], roots) {
			return false
		}
		index = 2
	} else if !UnderRoots(cwd, roots) {
		return false
	}
	if index < len(values) && values[index] == "tag" {
		return gitTagList(values[index+1:])
	}
	if index < len(values) && values[index] == "worktree" {
		return gitWorktreeList(values[index+1:])
	}
	if index >= len(values) || !gitReadOnlySubcommands[values[index]] {
		return false
	}
	if values[index] == "ls-remote" {
		for _, value := range values[index+1:] {
			if strings.Contains(value, ":") {
				return false
			}
		}
	}
	if gitDiffFamily[values[index]] && gitShortOutput(values[index+1:]) {
		return false
	}
	return gitReadOnlyArgs(values[index+1:])
}

// gitDiffFamily are the subcommands where `-o <file>` is the short `--output`.
var gitDiffFamily = map[string]bool{"log": true, "show": true, "diff": true}

// gitShortOutput reports whether a short-option cluster carries `o`, which in the
// diff family writes the output to a file (`-o/tmp/x`, `-po /tmp/x`).
func gitShortOutput(args []string) bool {
	for _, value := range args {
		if value == "--" {
			return false
		}
		if strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "--") &&
			strings.Contains(value[1:], "o") {
			return true
		}
	}
	return false
}

// gitReadOnlyArgs reports whether every argument of a read-only subcommand is static.
func gitReadOnlyArgs(args []string) bool {
	for _, value := range args {
		if value == "--" {
			return true
		}
		if gitBadArg.MatchString(value) {
			return false
		}
		if strings.HasPrefix(value, "--") {
			name, _, _ := strings.Cut(strings.TrimPrefix(value, "--"), "=")
			if name == "" || gitBadLongPrefix(name) {
				return false
			}
			continue
		}
		if strings.HasPrefix(value, "-") && strings.Contains(value[1:], "O") {
			return false
		}
	}
	return true
}

// gitBadLongPrefix reports whether a long option name is refused by prefix.
func gitBadLongPrefix(name string) bool {
	for _, bad := range gitBadLongPrefixes {
		if strings.HasPrefix(bad, name) {
			return true
		}
	}
	return false
}

// gitTagList reports whether `git tag` runs in list mode with only safe options.
func gitTagList(args []string) bool {
	listed := false
	for _, arg := range args {
		if gitTagListFlags[arg] {
			listed = true
		}
	}
	if !listed {
		return false
	}
	skip := false
	for _, arg := range args {
		if skip {
			skip = false
			if strings.HasPrefix(arg, "-") {
				return false
			}
			continue
		}
		switch {
		case gitTagValueFlags[arg]:
			skip = true
		case strings.HasPrefix(arg, "-") && !gitTagListFlags[arg] && !gitTagOption.MatchString(arg):
			return false
		}
	}
	return true
}

// gitWorktreeList reports whether the invocation is `git worktree list` alone.
func gitWorktreeList(args []string) bool {
	if len(args) == 0 || args[0] != "list" {
		return false
	}
	for _, arg := range args[1:] {
		if !gitWorktreeListFlags[arg] {
			return false
		}
	}
	return true
}

// UnderRoots reports whether path resolves under one of roots. A `~` prefix is
// expanded against the real home, and a relative path is never under a root.
func UnderRoots(path string, roots []string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		return false
	}
	// A path that does not resolve is refused, never matched raw: an uncleaned
	// `<root>/x/../../..` passes a prefix test while the kernel walks out of root.
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return false
	}
	real += string(os.PathSeparator)
	for _, root := range roots {
		resolved := root
		if link, linkErr := filepath.EvalSymlinks(root); linkErr == nil {
			resolved = link
		}
		if strings.HasPrefix(real, resolved+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// DefaultGitRoots returns the two directories a read-only git call may read from.
func DefaultGitRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Documents", "workspaces"),
		filepath.Join(home, "Documents", "Obsidian"),
	}
}

var (
	// gitMergeBad matches the flags that discard or reshape a merge.
	gitMergeBad = regexp.MustCompile(
		`^(--abort|--quit|--squash|--no-commit|--strategy|--strategy=.*|--strategy-option` +
			`|--autostash|--edit|-e|--cleanup|--cleanup=.*|--no-verify|--verify|-s|-s.*|-X.*)$`,
	)
)

// DefaultMergeRoots returns the two directories a `git merge` may run in. The
// Obsidian vaults are excluded because obsidian-git owns them.
func DefaultMergeRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Documents", "workspaces"),
		filepath.Join(home, ".claude"),
	}
}

// GitMergeAllows reports whether the invocation is the documented deploy-worktree
// sync: `git -C <dir under the merge roots> merge <remote>/<branch>`.
//
// The source must be a remote-tracking ref — a name containing `/`. A bare branch
// name, a bare `git merge` and a commit id all fall through, so the guard cannot
// allow `git merge master`, the form the trading guide forbids.
func GitMergeAllows(values []string, roots []string) bool {
	if len(values) < 4 || values[0] != "-C" || values[1] == "" {
		return false
	}
	if !UnderRoots(values[1], roots) || values[2] != "merge" {
		return false
	}
	source := ""
	for _, value := range values[3:] {
		if strings.HasPrefix(value, "-") {
			if gitMergeBad.MatchString(value) || gitBadArg.MatchString(value) {
				return false
			}
			continue
		}
		if source != "" {
			return false
		}
		source = value
	}
	return mergeSourceOK(source)
}

// mergeSourceOK reports whether a merge source is a remote-tracking ref.
func mergeSourceOK(source string) bool {
	if source == "" || !strings.Contains(source, "/") {
		return false
	}
	if strings.HasPrefix(source, "/") || strings.Contains(source, "..") {
		return false
	}
	return true
}
