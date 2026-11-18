// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// branchSwitchReason builds the refusal for one offending span.
//
// The text is the script's, byte-identical apart from the two substitutions.
const branchSwitchReason = "Refused: `%s` switches the branch of a repo's main checkout (%s). " +
	"The main checkout stays on main/master — a branch lives in a worktree. Create it with:\n" +
	"  git fetch && git worktree add --no-track ../<repo>-<name> -b <branch> origin/master\n" +
	"Or switch back to trunk. Work inside an existing linked worktree is unaffected."

var (
	// branchSwitchWrapper matches a shell wrapper or eval, whose quoted argument is
	// code rather than prose.
	branchSwitchWrapper = regexp.MustCompile(
		`(^|[^[:alnum:]_-])(bash|sh|zsh|dash|ksh|ash|csh|tcsh|fish|busybox)` +
			`([[:space:]]+-[^[:space:]]+([[:space:]]+[^-[:space:]][^[:space:]]*)?)*` +
			`[[:space:]]+-[[:alnum:]]*c[[:alnum:]]*([[:space:]]|$)`,
	)
	branchSwitchEval = regexp.MustCompile(`(^|[^[:alnum:]_-])eval[[:space:]]`)
	// branchSwitchSpan finds one `git …` span, carrying its leading boundary so
	// `legit checkout` is not a git call. `.` is excluded too: a path ending in
	// `.git` otherwise matches at the `.git`.
	branchSwitchSpan = regexp.MustCompile(`(^|[^[:alnum:]_.-])(git[[:space:]][^&;|]*)`)
	// branchSwitchCd finds the last `cd <operand>` before a span.
	branchSwitchCd       = regexp.MustCompile(`(^|[^[:alnum:]_-])cd[[:space:]]+[^[:space:]&;|]+`)
	branchSwitchCdPrefix = regexp.MustCompile(`^[^[:alnum:]]*cd[[:space:]]+`)
	// branchSwitchDashC matches `git -C <path>` inside a span.
	branchSwitchDashC = regexp.MustCompile(`(^|[[:space:]])-C[[:space:]]*([^[:space:]&;|]+)`)
	// branchSwitchGitDirFlag matches `--git-dir=<path>` inside a span.
	branchSwitchGitDirFlag = regexp.MustCompile(`--git-dir=([^[:space:]&;|]+)`)
	// branchSwitchGitDirEnv matches a `GIT_DIR=<path>` prefix.
	branchSwitchGitDirEnv = regexp.MustCompile(`(^|[^[:alnum:]_])GIT_DIR=([^[:space:]&;|]+)`)
	// branchSwitchGlobalOptions strips the options git accepts before a subcommand.
	branchSwitchGlobalOptions = regexp.MustCompile(
		`^git([[:space:]]+(-C[[:space:]]+[^[:space:]]+|-C[^[:space:]]+|-c[[:space:]]+[^[:space:]]+|` +
			`--git-dir=[^[:space:]]+|--work-tree=[^[:space:]]+|--namespace=[^[:space:]]+|` +
			`--exec-path=[^[:space:]]+|--no-pager|--paginate|--literal-pathspecs|-p))*`,
	)
	branchSwitchVerb          = regexp.MustCompile(`^[[:space:]]*(checkout|switch)([[:space:]]|$)`)
	branchSwitchAlias         = regexp.MustCompile(`-c[[:space:]]+alias\.([^=[:space:]]+)=(checkout|switch)`)
	branchSwitchCheckoutFlags = regexp.MustCompile(`^(-b|-B|--branch|--orphan|--detach)$`)
	branchSwitchSwitchFlags   = regexp.MustCompile(
		`^(-c|-C|--create|--force-create|-d|--detach)$`,
	)
	// hexObjectName matches a commit id, which `rev-parse` resolves.
	hexObjectName = regexp.MustCompile(`^[0-9a-f]{4,40}$`)
)

// BranchSwitchGuard replaces deny-branch-switch-in-main-checkout.sh.
//
// The main checkout always stays on main/master; a branch lives in a worktree.
// It fails OPEN on anything it cannot parse: this guards one narrow command shape,
// and failing closed would block every Bash call on a malformed payload.
type BranchSwitchGuard struct{}

// NewBranchSwitchGuard returns the branch-switch guard.
func NewBranchSwitchGuard() *BranchSwitchGuard {
	return &BranchSwitchGuard{}
}

// Name returns the script this guard replaces.
func (g *BranchSwitchGuard) Name() string { return "deny-branch-switch-in-main-checkout.sh" }

// Applies reports whether this is a Bash call naming a checkout or switch.
func (g *BranchSwitchGuard) Applies(input Input) bool {
	if !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.Contains(command, "checkout") || strings.Contains(command, "switch")
}

// Evaluate denies a branch switch made in a repo's main checkout.
func (g *BranchSwitchGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	if command == "" {
		return Decision{}
	}
	wrapper := branchSwitchWrapper.MatchString(command) || branchSwitchEval.MatchString(command)
	// A newline is a shell separator too; normalise it so a span stops there.
	scan := strings.ReplaceAll(command, "\n", ";")
	cwd := input.Cwd
	if cwd == "" {
		cwd = "/"
	}
	rest, consumed := scan, 0
	for len(rest) > 0 {
		loc := branchSwitchSpan.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		span := rest[loc[4]:loc[5]]
		pre := rest[:loc[4]]
		at := consumed + len(pre)
		if wrapper || !insideQuotes(scan, at) {
			if decision, denied := judgeSpan(scan, span, at, cwd); denied {
				return decision
			}
		}
		before := len(rest)
		rest = rest[loc[5]:]
		consumed += loc[5]
		if len(rest) >= before {
			break
		}
	}
	return Decision{}
}

// judgeSpan judges one `git` span, and reports whether it refused.
func judgeSpan(scan string, span string, at int, cwd string) (Decision, bool) {
	cand, verb := spanVerb(span)
	if verb == "" {
		return Decision{}, false
	}
	dir := spanRepo(scan, span, at, cwd)
	if _, ok := isMainCheckout(dir); !ok {
		return Decision{}, false
	}
	return judgeArgs(span, cand, verb, dir)
}

// spanVerb returns the span with its global options stripped, and the subcommand
// when it is a checkout or a switch — including an inline alias definition, which
// runs the same code path under another name.
func spanVerb(span string) (string, string) {
	cand := branchSwitchGlobalOptions.ReplaceAllString(span, "")
	if match := branchSwitchVerb.FindStringSubmatch(cand); match != nil {
		return cand, match[1]
	}
	alias := branchSwitchAlias.FindStringSubmatch(span)
	if alias == nil {
		return cand, ""
	}
	trimmed := strings.TrimSpace(cand)
	if !strings.HasPrefix(trimmed, alias[1]+" ") {
		return cand, ""
	}
	return alias[2] + strings.TrimPrefix(trimmed, alias[1]), alias[2]
}

// judgeArgs applies the flag, path-restore and target arms to one span.
func judgeArgs(span string, cand string, verb string, dir string) (Decision, bool) {
	args := argsAfterVerb(cand, verb)
	flagRe := branchSwitchCheckoutFlags
	if verb == "switch" {
		flagRe = branchSwitchSwitchFlags
	}
	for _, arg := range args {
		if flagRe.MatchString(arg) {
			return branchSwitchRefusal(span, dir), true
		}
	}
	for _, arg := range args {
		if arg == "--" {
			return Decision{}, false
		}
	}
	target := firstTarget(args)
	if target == "" {
		return Decision{}, false
	}
	return judgeTarget(span, verb, target, dir)
}

// firstTarget returns the first non-flag argument, with `-` meaning the previous
// branch.
func firstTarget(args []string) string {
	for _, arg := range args {
		if arg == "-" {
			return "-"
		}
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// judgeTarget decides on the branch a span would switch to.
func judgeTarget(span string, verb string, target string, dir string) (Decision, bool) {
	switch target {
	case "main", "master", "refs/heads/main", "refs/heads/master", "HEAD", "@":
		return Decision{}, false
	case "-":
		return branchSwitchRefusal(span, dir), true
	}
	// A revision expression is a branch switch even when no literal ref file names
	// it: `origin/master`, `HEAD~1` and `@{-1}` all detach the shared checkout, and
	// `rev-parse --verify` resolved them where a file probe cannot. Treating any
	// such shape as a ref is stricter than the script, which is the safe direction.
	if strings.ContainsAny(target, "/~^@:") {
		return branchSwitchRefusal(span, dir), true
	}
	if verb == "switch" || refExists(dir, target) {
		return branchSwitchRefusal(span, dir), true
	}
	return Decision{}, false
}

// spanRepo resolves the repository a span runs in: the last `cd` before it, then
// a `-C` operand, then a git-dir override.
func spanRepo(scan string, span string, at int, cwd string) string {
	dir := cwd
	// The `cd` is read from the QUOTE-STRIPPED prefix: a commit message containing
	// `cd /tmp` otherwise resolves the repo to /tmp and the real switch is never
	// judged. Losing a quoted `cd "<worktree>"` operand costs a false deny, which is
	// the safe direction.
	prefix := scanText(scan[:at])
	if matches := branchSwitchCd.FindAllString(prefix, -1); len(matches) > 0 {
		operand := branchSwitchCdPrefix.ReplaceAllString(matches[len(matches)-1], "")
		dir = resolvePathOperand(operand, cwd)
	}
	if match := branchSwitchDashC.FindStringSubmatch(span); match != nil {
		dir = resolvePathOperand(match[2], dir)
	}
	if match := branchSwitchGitDirFlag.FindStringSubmatch(span); match != nil {
		override := resolvePathOperand(match[1], dir)
		if filepath.Base(override) == ".git" {
			dir = filepath.Dir(override)
		}
	} else if match := branchSwitchGitDirEnv.FindStringSubmatch(prefix); match != nil {
		override := resolvePathOperand(match[2], dir)
		if filepath.Base(override) == ".git" {
			dir = filepath.Dir(override)
		}
	}
	return dir
}

// argsAfterVerb returns the arguments following the verb.
func argsAfterVerb(cand string, verb string) []string {
	index := strings.Index(cand, verb)
	if index < 0 {
		return nil
	}
	return strings.Fields(cand[index+len(verb):])
}

// resolvePathOperand strips surrounding quotes and resolves an operand.
func resolvePathOperand(operand string, base string) string {
	return resolveOperand(strings.Trim(operand, `"'`), base)
}

// insideQuotes reports whether byte offset at sits inside a quoted region.
func insideQuotes(text string, at int) bool {
	var quote byte
	for i := 0; i < len(text) && i < at; i++ {
		switch {
		case quote == 0 && (text[i] == '\'' || text[i] == '"'):
			quote = text[i]
		case quote != 0 && text[i] == quote:
			quote = 0
		}
	}
	return quote != 0
}

// branchSwitchRefusal builds the refusal decision for one span.
func branchSwitchRefusal(span string, dir string) Decision {
	return DenyDecision(fmt.Sprintf(branchSwitchReason, strings.TrimSpace(span), dir))
}
