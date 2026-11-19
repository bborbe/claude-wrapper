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
	return TmpPath(path)
}

// TmpPath reports whether an absolute path with no `..` resolves inside /tmp. It
// applies no shell rules, so a file tool's literal `$$` in a name is just a name.
func TmpPath(path string) bool {
	if !filepath.IsAbs(path) || hasDotDot(path) {
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

// ghAPIMethods are the only methods the read shape accepts.
var ghAPIMethods = map[string]bool{"GET": true, "HEAD": true}

// ghAPIBool, ghAPIValue and ghAPIRefuse are the api read shape's flags. Every switch
// in ghAPIRefuse writes (`-f`/`-F`/`--input` turn gh into a POST), re-targets
// another host, or prints the request headers and with them the auth token.
var (
	ghAPIBool  = map[string]bool{"--paginate": true, "--slurp": true, "--silent": true, "-i": true, "--include": true}
	ghAPIValue = map[string]bool{
		"-q": true, "--jq": true, "-t": true, "--template": true, "--cache": true,
		"-H": true, "--header": true,
	}
	ghAPIRefuse = map[string]bool{
		"-f": true, "--raw-field": true, "-F": true, "--field": true, "--input": true,
		"-v": true, "--verbose": true, "--debug": true, "--hostname": true, "--web": true,
		"-p": true, "--preview": true,
	}
)

// GhAPIAllows reports whether the invocation is a read of one REST path:
// `gh api <endpoint>` with the default GET method or an explicit `-X GET|HEAD`.
// The endpoint is a literal path: no `://` (a full URL is egress) and no `..`.
func GhAPIAllows(values []string) bool {
	if len(values) == 0 || values[0] != "api" {
		return false
	}
	endpoint := ""
	for i := 1; i < len(values); i++ {
		next, ok := ghAPIStep(values, i, &endpoint)
		if !ok {
			return false
		}
		i = next
	}
	return endpoint != ""
}

// ghAPIStep validates the argument at i and returns the index of the last one it
// consumed. The first plain operand becomes the endpoint.
func ghAPIStep(values []string, i int, endpoint *string) (int, bool) {
	value := values[i]
	switch {
	case ghAPIRefuse[value]:
		return i, false
	case value == "-X" || value == "--method":
		return i + 1, i+1 < len(values) && ghAPIMethods[values[i+1]]
	case ghAPIValue[value]:
		return i + 1, i+1 < len(values)
	case ghAPIBool[value]:
		return i, true
	case strings.HasPrefix(value, "--") && strings.Contains(value, "="):
		name, flagValue, _ := strings.Cut(value, "=")
		if name == "--method" {
			return i, ghAPIMethods[flagValue]
		}
		return i, ghAPIValue[name]
	case *endpoint == "" && !strings.HasPrefix(value, "-") &&
		!strings.Contains(value, "://") && !strings.Contains(value, ".."):
		*endpoint = value
		return i, true
	}
	return i, false
}

// ghRuleReadVerbs are the gh subcommands that only read, so an allow rule naming
// one cannot reach a write. Write verbs (`pr close`, `pr review`, `pr ready`, …)
// are left to the shape guards and the permission prompt even when a rule names them.
var ghRuleReadVerbs = map[string]map[string]bool{
	"repo":     {"view": true, "list": true},
	"run":      {"view": true, "list": true},
	"release":  {"view": true, "list": true},
	"issue":    {"view": true, "list": true},
	"workflow": {"view": true, "list": true},
	"pr":       {"view": true, "list": true, "checks": true, "diff": true, "status": true},
}

// GhRuleReadable reports whether a gh call is a read-only verb an allow rule may
// approve: `gh [-R <owner>/<repo>] <group> <read verb> …` or `gh search …`, with no
// `--web` (it opens a browser) and every repo value well-formed.
func GhRuleReadable(words []string) bool {
	if len(words) < 2 || words[0] != "gh" {
		return false
	}
	rest := words[1:]
	if len(rest) >= 2 && (rest[0] == "-R" || rest[0] == "--repo") {
		if !ghRepoOK(rest[1]) {
			return false
		}
		rest = rest[2:]
	}
	for i, word := range rest {
		if word == "--web" || word == "-w" || strings.HasPrefix(word, "--web=") {
			return false
		}
		if (word == "-R" || word == "--repo") && (i+1 >= len(rest) || !ghRepoOK(rest[i+1])) {
			return false
		}
	}
	if len(rest) >= 1 && rest[0] == "search" {
		return true
	}
	return len(rest) >= 2 && ghRuleReadVerbs[rest[0]][rest[1]]
}
