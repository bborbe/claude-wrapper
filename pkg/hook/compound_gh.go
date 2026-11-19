// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// ghOwnRepo matches the operator's own repositories, the only ones a write shape
	// is approved for.
	ghOwnRepo = regexp.MustCompile(`^bborbe/[A-Za-z0-9_.-]+$`)
	// ghRef matches a branch name. A `:` would be a fork's owner:branch, and `..` is
	// refused separately.
	ghRef = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	// ghAnyRepo matches any owner/repo, which is enough for a read-only subcommand.
	ghAnyRepo = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
	// ghReadPos matches a PR number or a branch name.
	ghReadPos = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// ghMergeFlags are the only flags the merge shape accepts.
var ghMergeFlags = map[string]bool{"--merge": true, "--delete-branch": true, "-d": true}

// ghCreateValue and ghCreateBool are the create shape's flags.
var (
	ghCreateValue = map[string]bool{
		"--base": true, "-B": true, "--head": true, "-H": true, "--title": true,
		"-t": true, "--body": true, "-b": true, "--body-file": true, "-F": true,
	}
	ghCreateBool = map[string]bool{"--draft": true, "-d": true}
)

// ghReadSub are the read-only PR subcommands.
var ghReadSub = map[string]bool{"view": true, "list": true, "checks": true, "diff": true}

// ghReadValue and ghReadBool are the read shape's flags.
var (
	ghReadValue = map[string]bool{
		"--json": true, "-q": true, "--jq": true, "-t": true, "--template": true,
		"--repo": true, "-R": true, "--state": true, "-s": true, "--limit": true,
		"-L": true, "--author": true, "-a": true, "--assignee": true, "-A": true,
		"--label": true, "-l": true, "--search": true, "-S": true, "--base": true,
		"-B": true, "--head": true, "-H": true, "--color": true,
	}
	ghReadBool = map[string]bool{
		"--comments": true, "-c": true, "--draft": true, "--closed": true,
		"--merged": true, "--open": true, "--all": true, "--no-color": true,
		"--patch": true, "--name-only": true,
	}
)

// TmpTarget reports whether path is a literal absolute path whose resolved form is
// inside /tmp. A `$var`, a backtick, a `~` form, a relative path or the `-` sentinel
// all fail: the target must be checkable now.
func TmpTarget(path string) bool {
	if strings.ContainsAny(path, "$`") || strings.HasPrefix(path, "~") {
		return false
	}
	if !filepath.IsAbs(path) {
		return false
	}
	if hasDotDot(path) {
		return false
	}
	real := ResolvePath(path)
	return strings.HasPrefix(real, "/tmp/") || strings.HasPrefix(real, "/private/tmp/")
}

// WriteTargetUnderRoots reports whether a file a command writes lands under one of
// roots. Unlike UnderRoots the file need not exist yet: its longest existing prefix
// is resolved instead. A `..` component is refused, as in UnderRoots.
func WriteTargetUnderRoots(path string, roots []string) bool {
	if !filepath.IsAbs(path) || hasDotDot(path) {
		return false
	}
	real := ResolvePath(path) + string(filepath.Separator)
	for _, root := range roots {
		resolved := root
		if link, err := filepath.EvalSymlinks(root); err == nil {
			resolved = link
		}
		if strings.HasPrefix(real, resolved+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ResolvePath returns the cleaned absolute path with every symlink in
// its longest existing prefix resolved. A file that does not exist yet is written
// through its parent, so the parent's real location is what decides where it lands;
// falling back to the raw text let a not-yet-existing file under a symlinked
// directory pass a `/tmp/` prefix test. Callers refuse `..` first: Clean folds
// `<dir>/link/..` lexically while the kernel follows the link.
func ResolvePath(path string) string {
	clean := filepath.Clean(path)
	rest := ""
	for dir := clean; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if dir == filepath.Dir(dir) {
			return clean
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// GhMergeAllows reports whether the invocation is the one approved merge shape:
// `gh -R bborbe/<repo> pr merge <N> --merge [--delete-branch]`.
func GhMergeAllows(values []string) bool {
	if len(values) < 6 || values[0] != "-R" || !ghOwnRepo.MatchString(values[1]) {
		return false
	}
	if values[2] != "pr" || values[3] != "merge" || !allDigits(values[4]) {
		return false
	}
	flags := values[5:]
	seenMerge := false
	for _, flag := range flags {
		if !ghMergeFlags[flag] {
			return false
		}
		if flag == "--merge" {
			seenMerge = true
		}
	}
	return seenMerge
}

// GhCreateAllows reports whether the invocation is the approved create shape:
// `gh -R bborbe/<repo> pr create <flags>` with literal text only.
func GhCreateAllows(values []string) bool {
	if len(values) < 4 || values[0] != "-R" || !ghOwnRepo.MatchString(values[1]) {
		return false
	}
	if values[2] != "pr" || values[3] != "create" {
		return false
	}
	flags := values[4:]
	for i := 0; i < len(flags); i++ {
		flag := flags[i]
		switch {
		case ghCreateBool[flag]:
		case ghCreateValue[flag]:
			if i+1 >= len(flags) || !ghCreateValueOK(flag, flags[i+1]) {
				return false
			}
			i++
		case strings.HasPrefix(flag, "--") && strings.Contains(flag, "="):
			name, value, _ := strings.Cut(flag, "=")
			if !ghCreateValue[name] && !ghCreateBool[name] {
				return false
			}
			if !ghCreateValueOK(name, value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ghCreateValueOK validates one create flag's value.
func ghCreateValueOK(flag string, value string) bool {
	if flag == "--body-file" || flag == "-F" {
		return TmpTarget(value)
	}
	if flag == "--head" || flag == "-H" || flag == "--base" || flag == "-B" {
		return ghRef.MatchString(value) && !strings.Contains(value, "..")
	}
	return true
}

// GhReadAllows reports whether the invocation is a read-only PR query:
// `gh [-R <owner>/<repo>] pr view|list|checks|diff …`.
func GhReadAllows(values []string) bool {
	index := 0
	if len(values) >= 2 && (values[0] == "-R" || values[0] == "--repo") {
		if !ghRepoOK(values[1]) {
			return false
		}
		index = 2
	}
	if len(values) < index+2 || values[index] != "pr" || !ghReadSub[values[index+1]] {
		return false
	}
	return ghReadArgs(values[index+2:])
}

// ghReadArgs validates the flags and positionals of a read-only PR query.
func ghReadArgs(values []string) bool {
	for i := 0; i < len(values); i++ {
		value := values[i]
		switch {
		case ghReadBool[value]:
		case ghReadValue[value]:
			if i+1 >= len(values) {
				return false
			}
			if (value == "-R" || value == "--repo") && !ghRepoOK(values[i+1]) {
				return false
			}
			i++
		case isGhReadEqualsFlag(value):
			if !ghReadEqualsOK(value) {
				return false
			}
		case !strings.HasPrefix(value, "-") && ghReadPos.MatchString(value):
		default:
			return false
		}
	}
	return true
}

// isGhReadEqualsFlag reports whether value is a `--flag=value` form of a known
// read flag.
func isGhReadEqualsFlag(value string) bool {
	if !strings.HasPrefix(value, "--") || !strings.Contains(value, "=") {
		return false
	}
	return ghReadValue[strings.SplitN(value, "=", 2)[0]]
}

// ghReadEqualsOK validates the value of a `--flag=value` read flag.
func ghReadEqualsOK(value string) bool {
	name, val, _ := strings.Cut(value, "=")
	return name != "--repo" || ghRepoOK(val)
}

// ghRepoOK reports whether a repo value is safe: gh builds `/repos/<owner>/<repo>`,
// so `..` and a leading `-` in the repo part are refused.
func ghRepoOK(value string) bool {
	if !ghAnyRepo.MatchString(value) || strings.Contains(value, "..") {
		return false
	}
	_, repo, _ := strings.Cut(value, "/")
	return !strings.HasPrefix(repo, "-")
}

// allDigits reports whether text is a non-empty run of digits.
func allDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
