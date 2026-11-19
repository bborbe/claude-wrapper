// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"fmt"
	"regexp"
	"strings"
)

// credentialPaths matches a reference to a credential store or a secret-printing
// CLI. A command naming one is never approved by this guard.
var credentialPaths = regexp.MustCompile(
	`\.netrc\b|\.ssh/|\bid_(rsa|ed25519|ecdsa|dsa)\b|\.aws/|\.kube/config|` +
		`\.docker/config\.json|gh/hosts\.yml|\.gnupg/|\.pgpass\b|\.npmrc\b|\.pypirc\b|` +
		`\bteamvault[-a-z]*\b|\bsecurity\s+find-(generic|internet)-password\b`,
)

// CredentialRead reports whether the command names a credential store or a
// secret-printing CLI.
func CredentialRead(command string) bool {
	return credentialPaths.MatchString(command)
}

// maxCompoundSegments bounds how many subcommands a compound may carry; a long
// chain is not something to compose.
const maxCompoundSegments = 8

// compoundAllowReason is the allow reason the script emits.
const compoundAllowReason = "allow-compound-bash: all %d subcommands match an existing " +
	"allow rule"

// CompoundBashGuard replaces allow-compound-bash.py.
//
// It allows a compound command when every subcommand matches an existing `Bash(...)`
// allow rule, or a shape guard, and nothing in the text can hide another command.
// It composes rules; it never invents a permission outside the shape guards, and a
// deny or ask rule always wins.
type CompoundBashGuard struct {
	loadRules func(cwd string) (CompoundRules, error)
}

// NewCompoundBashGuard returns the compound-composition guard.
func NewCompoundBashGuard() *CompoundBashGuard {
	return &CompoundBashGuard{loadRules: LoadRules}
}

// NewCompoundBashGuardWithRules returns the guard over a stubbed rule loader.
func NewCompoundBashGuardWithRules(
	load func(cwd string) (CompoundRules, error),
) *CompoundBashGuard {
	return &CompoundBashGuard{loadRules: load}
}

// MayAllow grants this guard allow authority; see Registry.Evaluate.
func (g *CompoundBashGuard) MayAllow() bool { return true }

// Name returns the script this guard replaces.
func (g *CompoundBashGuard) Name() string { return "allow-compound-bash.py" }

// Applies reports whether this is a PreToolUse Bash call.
func (g *CompoundBashGuard) Applies(input Input) bool {
	if input.HookEventName != EventPreToolUse || !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.TrimSpace(command) != ""
}

// singleShapeHeads are the programs whose single-command form only a shape guard
// can approve. Any other single command is the permission rules' own job.
var singleShapeHeads = map[string]bool{"git": true, "sed": true, "gh": true}

// Evaluate allows a compound whose every subcommand is already permitted, and a
// single `git` / `sed` / `gh` call a shape guard approves — `git -C <root> status`
// matches no `Bash(git <sub>:*)` rule, so only the shape guard can approve it.
func (g *CompoundBashGuard) Evaluate(input Input) Decision {
	original, _ := input.BashCommand()
	if CredentialRead(original) {
		// No arm may approve a read of a credential store, whatever rule names the
		// program: the value would land in the transcript.
		return Decision{}
	}
	command := original
	substituted, rewritten := SubstituteLiterals(original)
	literalFallback := false
	if rewritten {
		body, ok := dropLiteralPrefix(original, substituted)
		if ok {
			command = body
		} else {
			// The tree arm models the assignment itself: a fresh local whose
			// value is walked, read back through a read-only expansion.
			literalFallback = true
			rewritten = false
		}
	}
	// The rewritten text carries the syntax check; the original only has to be
	// free of a substitution, since its `${NAME}` words are what got rewritten.
	// Text the split cannot account for — a redirect, a heredoc, a substitution,
	// a long chain — goes to the syntax tree alone, which models each node or
	// refuses it.
	treeOnly := UnsafeSyntax(command) || strings.Contains(original, "`") ||
		strings.Contains(original, "$(")
	treeOnly = treeOnly || literalFallback
	segments, ok := SplitCompound(command)
	if !ok {
		// The text split does not model a backslash inside quotes; the parser does.
		treeOnly = true
		segments = []string{command}
	}
	if len(segments) < 1 {
		return Decision{}
	}
	if len(segments) > maxCompoundSegments {
		treeOnly = true
	}
	if len(segments) == 1 && !treeOnly && !singleShapeHeads[firstWord(segments[0])] {
		// A single command matching a `permissions.allow` rule. Under
		// `defaultMode: auto` Claude Code grants nothing from that list, so a headless
		// worker parked on every routine `ls` / `grep` / `vault-cli task get`. The
		// tree arm applies the rule with the same gates as a compound: deny and ask
		// rules win, wrappers and shell-state builtins are refused.
		treeOnly = true
	}
	rules, err := g.loadRules(input.Cwd)
	if err != nil {
		return Decision{}
	}
	if rewritten && originalGated(original, rules) {
		return Decision{}
	}
	roots := DefaultGitRoots()
	if treeOnly {
		if CompoundTreeAllows(command, rules, input.Cwd, roots) {
			return AllowDecision(fmt.Sprintf(compoundAllowReason, len(segments)))
		}
		return Decision{}
	}
	if len(segments) == 1 {
		if compoundSingleAllows(segments[0], command, rules, input.Cwd, roots) {
			return AllowDecision(fmt.Sprintf(compoundAllowReason, 1))
		}
		return Decision{}
	}
	if compoundTextAllows(segments, rules, input.Cwd, roots) {
		return AllowDecision(fmt.Sprintf(compoundAllowReason, len(segments)))
	}
	if CompoundTreeAllows(command, rules, input.Cwd, roots) {
		return AllowDecision(fmt.Sprintf(compoundAllowReason, len(segments)))
	}
	return Decision{}
}

// compoundSingleAllows reports whether a single command is approved by a shape guard.
//
// Matching it against the allow rules is Claude Code's own job, and a second
// matcher that disagrees with it is a bypass. A quoted word with a space
// (`grep 'a b'`) only parses right as a tree.
func compoundSingleAllows(
	segment, command string, rules CompoundRules, cwd string, roots []string,
) bool {
	if matchesAnyPattern(segment, rules.Gated) {
		return false
	}
	words := unquoteStaticWords(strings.Fields(segment))
	if staticWords(words) && compoundShapeAllows(words, cwd, roots) {
		return true
	}
	return CompoundTreeAllows(command, rules, cwd, roots)
}

// shellReadNames are the variables the shell itself reads, so assigning one changes
// what a later command does even unexported.
var shellReadNames = map[string]bool{
	"PATH": true, "IFS": true, "CDPATH": true, "HOME": true, "ENV": true,
	"PS4": true, "SHELLOPTS": true, "BASHOPTS": true, "GLOBIGNORE": true,
	"PROMPT_COMMAND": true, "PWD": true, "OLDPWD": true, "TMPDIR": true,
	"FPATH": true, "ZDOTDIR": true,
}

// envRedirectPrefixes name the tool variables that redirect where a tool reads.
var envRedirectPrefixes = []string{"GIT_", "DOCKER_", "AWS_", "XDG_", "LD_", "DYLD_", "BASH_"}

// dropLiteralPrefix returns the substituted command without its leading
// assignments. Every assignment is an unexported local whose value now sits in the
// words that read it, so dropping it leaves the command the shell runs. It refuses
// a name the shell or a tool reads, and a value the shell would glob or expand.
func dropLiteralPrefix(original string, substituted string) (string, bool) {
	prefix := compoundAssignPrefix.FindString(original)
	if prefix == "" || !strings.HasPrefix(substituted, prefix) {
		return "", false
	}
	for _, pair := range compoundAssignPair.FindAllStringSubmatch(prefix, -1) {
		// A leading `~/` expands at assignment exactly as it does in the word the
		// rewrite carries it into; a `~` anywhere else does not.
		if shellReadName(pair[1]) || strings.ContainsAny(strings.TrimPrefix(pair[2], "~/"), "*?[]~!#") {
			return "", false
		}
	}
	return substituted[len(prefix):], true
}

// shellReadName reports whether assigning name changes what the shell or a tool
// reads.
func shellReadName(name string) bool {
	if shellReadNames[name] {
		return true
	}
	for _, prefix := range envRedirectPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// firstWord returns the program word of a segment.
func firstWord(segment string) string {
	words := strings.Fields(segment)
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// originalGated reports whether any segment of the unrewritten text matches a
// deny or ask rule, so a substitution can never release a gate.
func originalGated(original string, rules CompoundRules) bool {
	segments, ok := SplitCompound(original)
	if !ok {
		return true
	}
	for _, segment := range segments {
		if matchesAnyPattern(segment, rules.Gated) {
			return true
		}
	}
	return false
}

// staticWords reports whether every word is a plain literal. The shape guards were
// written against the syntax-tree path, which only ever hands them literal words;
// feeding them a raw `Fields` split lets `git log $X` or a quoted operand through.
func staticWords(words []string) bool {
	for _, word := range words {
		if strings.ContainsAny(word, "$`'\"\\") {
			return false
		}
	}
	return true
}

// unquoteStaticWords strips the double quotes from a word that is nothing but a
// quoted literal — `"/x/y"` is the same single word as `/x/y`. Any other word is
// left as it is, so staticWords still refuses it.
func unquoteStaticWords(words []string) []string {
	out := make([]string, len(words))
	for i, word := range words {
		inner := strings.TrimSuffix(strings.TrimPrefix(word, `"`), `"`)
		if len(word) >= 2 && word[0] == '"' && word[len(word)-1] == '"' &&
			!strings.ContainsAny(inner, "$`'\"\\") && !strings.HasPrefix(inner, "~") {
			out[i] = inner
			continue
		}
		// `'x'` passes x verbatim; a word with a second quote inside is left as is.
		if len(word) >= 2 && word[0] == '\'' && word[len(word)-1] == '\'' &&
			!strings.ContainsAny(word[1:len(word)-1], "'\"") &&
			!strings.HasPrefix(word[1:len(word)-1], "~") {
			out[i] = word[1 : len(word)-1]
			continue
		}
		out[i] = word
	}
	return out
}

// compoundTextAllows reports whether every segment is permitted on its own.
func compoundTextAllows(
	segments []string,
	rules CompoundRules,
	cwd string,
	roots []string,
) bool {
	for _, segment := range segments {
		words := unquoteStaticWords(strings.Fields(segment))
		if len(words) == 0 {
			return false
		}
		if matchesAnyPattern(segment, rules.Gated) {
			return false
		}
		if compoundShapeAllows(words, cwd, roots) && staticWords(words) {
			continue
		}
		if singleShapeHeads[words[0]] {
			// git / sed / gh are approved by their shape guard or not at all: a broad
			// rule like `Bash(sed -n:*)` would otherwise pass `sed -n -i`.
			return false
		}
		if wrapperProgram(words[0]) {
			return false
		}
		if !matchesAnyPattern(segment, rules.Allow) {
			return false
		}
	}
	return true
}
