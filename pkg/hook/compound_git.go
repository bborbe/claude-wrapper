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
	index, ok := gitShapeIndex(values, cwd, roots)
	if !ok {
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
			// `-u` is the short `--upload-pack` on older git.
			if strings.Contains(value, ":") || strings.HasPrefix(value, "-u") {
				return false
			}
		}
	}
	if gitDiffFamily[values[index]] && gitShortOutput(values[index+1:]) {
		return false
	}
	if !gitOperandsInRoots(values[index+1:], roots) {
		return false
	}
	return gitReadOnlyArgs(values[index+1:])
}

// gitShapeIndex resolves where the subcommand starts, after an optional `-C`.
//
// The `-C` operand must resolve under roots, and without one the cwd must.
func gitShapeIndex(values []string, cwd string, roots []string) (int, bool) {
	if len(values) > 0 && values[0] == "-C" {
		if len(values) < 2 || !UnderRoots(values[1], roots) {
			return 0, false
		}
		return 2, true
	}
	if !UnderRoots(cwd, roots) {
		return 0, false
	}
	return 0, true
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
	// A `..` component is refused outright: a lexical Clean folds `<root>/link/..`
	// back into the root while the kernel follows the link first and walks out of
	// it. A path that does not exist yet is judged by its longest existing prefix,
	// resolved — never matched raw — so a removed worktree reads as the place it
	// was, and a symlink anywhere in the existing part is followed.
	return WriteTargetUnderRoots(path, roots)
}

// hasDotDot reports whether path carries a `..` component.
func hasDotDot(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// gitOperandsInRoots reports whether every path-shaped argument stays in roots.
// Git reads a file named on its command line whatever the subcommand: `diff` turns
// into `--no-index` for a path outside the work tree, `grep -f` and `blame -S`
// read a pattern or revs file. A short option carrying a path, and any `..`, is
// refused; a long `--opt=<path>` value and an absolute operand must be under roots.
func gitOperandsInRoots(args []string, roots []string) bool {
	for _, value := range args {
		if value == "--" {
			continue
		}
		if hasDotDot(value) || strings.HasPrefix(value, "..") {
			return false
		}
		if strings.HasPrefix(value, "--") {
			if _, rest, ok := strings.Cut(value, "="); ok && pathShaped(rest) &&
				!UnderRoots(rest, roots) {
				return false
			}
			continue
		}
		if strings.HasPrefix(value, "-") {
			if strings.Contains(value, "/") {
				return false
			}
			continue
		}
		if pathShaped(value) && !UnderRoots(value, roots) {
			return false
		}
	}
	return true
}

// pathShaped reports whether value names an absolute or home-relative path.
func pathShaped(value string) bool {
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~")
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

// GitWriteAllows reports whether the invocation is a local, additive git write
// under the write roots (~/Documents/workspaces and ~/.claude): `commit`, `fetch`,
// `add` or `worktree add`, always with an explicit `-C <dir>`.
//
// None of these leaves the machine or discards work: `fetch` moves only
// remote-tracking refs, `add` only stages, `worktree add` only creates a checkout,
// and `commit` records staged work (`--amend` rewrites history and is refused). The
// Obsidian vaults are outside the roots because obsidian-git owns their commits.
// `push`, `reset`, `worktree remove` and the rest stay with the permission prompt.
func GitWriteAllows(values []string, roots []string) bool {
	if len(values) < 3 || values[0] != "-C" || values[1] == "" {
		return false
	}
	dir := values[1]
	if !UnderRoots(dir, roots) {
		return false
	}
	switch values[2] {
	case "commit":
		return gitWriteArgs(values[3:], func(value string) bool {
			return strings.HasPrefix(value, "--amend")
		})
	case "fetch", "add":
		return gitWriteArgs(values[3:], nil)
	case "worktree":
		if len(values) < 4 || values[3] != "add" {
			return false
		}
		return gitWriteArgs(values[4:], nil) && worktreePathsInRoots(values[4:], dir, roots)
	}
	return false
}

// gitWriteArgs refuses an option that reads or executes something unbounded, and
// any option the caller names as extra.
func gitWriteArgs(args []string, extra func(string) bool) bool {
	for _, arg := range args {
		if arg == "--" {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		if gitBadArg.MatchString(arg) || (extra != nil && extra(arg)) {
			return false
		}
		if strings.HasPrefix(arg, "--") {
			name, _, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			if gitBadLongPrefix(name) {
				return false
			}
		}
	}
	return true
}

// worktreePathsInRoots reports whether the path operand of `worktree add` (its
// first positional) resolves, against the real `-C` directory, under the roots — so a
// checkout cannot be created outside the trees the session already writes. The `-C`
// directory is resolved before joining, so `..` walks out of its real location the
// way the kernel would, not out of a symlink's name.
func worktreePathsInRoots(args []string, dir string, roots []string) bool {
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~/"))
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	valueFlags := map[string]bool{"-b": true, "-B": true, "--reason": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if valueFlags[arg] {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		target := arg
		if strings.HasPrefix(target, "~") {
			return false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(base, target)
		}
		target = filepath.Clean(target)
		return WriteTargetUnderRoots(target, roots) || TmpTarget(target)
	}
	return false
}
