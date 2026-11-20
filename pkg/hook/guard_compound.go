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

// credentialGlobPaths matches a glob inside the dot-directory NAME, which selects a
// credential store without spelling it (`cat ~/.a?s/credentials`). A glob in a later
// segment (`~/.claude/*.json`) stays allowed.
var credentialGlobPaths = regexp.MustCompile(
	`[~/]\.[A-Za-z0-9_.-]*[*?\[]|/(Users|home)/[^/\s]+/\.[A-Za-z0-9_.-]*[*?\[]`,
)

// CredentialRead reports whether the command names a credential store or a
// secret-printing CLI.
//
// The glob rule is tested against the command with its quoted spans blanked: the
// shell expands a glob only unquoted, so `gh api "repos/o/r/contents/x.yml?ref=master"`
// reads a file and never selects a directory, while `cat ~/.a?s/credentials` does.
func CredentialRead(command string) bool {
	if credentialPaths.MatchString(command) {
		return true
	}
	return credentialGlobPaths.MatchString(unquotedText(command))
}

// unquotedText blanks every single- and double-quoted span, so a pattern can be
// matched against the text the shell still expands. An unterminated quote blanks
// the rest of the line, which errs towards allowing a match, never towards missing
// one: the literal patterns above are tested on the raw text regardless.
func unquotedText(command string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(command) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		case c == '\\' && i+1 < len(command):
			i++
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
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
	plan, ok := planCompound(original)
	if !ok {
		return Decision{}
	}
	if plan.command != original && CredentialRead(plan.command) {
		// A literal prefix can assemble the path after the raw scan ran:
		// `d=.ssh; cat ~/$d/id_rsa` names no store until the rewrite lands.
		return Decision{}
	}
	rules, err := g.loadRules(input.Cwd)
	if err != nil {
		return Decision{}
	}
	if plan.rewritten && originalGated(original, rules) {
		return Decision{}
	}
	if plan.allows(rules, input.Cwd, DefaultGitRoots()) {
		return AllowDecision(fmt.Sprintf(compoundAllowReason, len(plan.segments)))
	}
	return Decision{}
}

// compoundPlan is the text a compound is judged on and the arm that judges it.
type compoundPlan struct {
	command   string
	segments  []string
	rewritten bool
	treeOnly  bool
}

// planCompound rewrites a leading literal prefix and decides which arm judges the
// rest. Text the split cannot account for — a redirect, a heredoc, a substitution,
// a long chain, a prefix the rewrite cannot carry, a single command — goes to the
// syntax tree alone, which models each node or refuses it.
func planCompound(original string) (compoundPlan, bool) {
	plan := compoundPlan{command: original}
	if substituted, rewritten := SubstituteLiterals(original); rewritten {
		if body, ok := dropLiteralPrefix(original, substituted); ok {
			plan.command, plan.rewritten = body, true
		} else {
			// The tree arm models the assignment itself: a fresh local whose
			// value is walked, read back through a read-only expansion.
			plan.treeOnly = true
		}
	}
	// The rewritten text carries the syntax check; the original only has to be
	// free of a substitution, since its `${NAME}` words are what got rewritten.
	plan.treeOnly = plan.treeOnly || UnsafeSyntax(plan.command) ||
		strings.Contains(original, "`") || strings.Contains(original, "$(")
	segments, ok := SplitCompound(plan.command)
	if !ok {
		// The text split does not model a backslash inside quotes; the parser does.
		plan.treeOnly = true
		segments = []string{plan.command}
	}
	if len(segments) < 1 {
		return compoundPlan{}, false
	}
	plan.segments = segments
	// A single command matching a `permissions.allow` rule goes to the tree arm too:
	// under `defaultMode: auto` Claude Code grants nothing from that list, so a
	// headless worker parked on every routine `ls` / `grep` / `vault-cli task get`.
	// The tree arm applies the rule with the same gates as a compound.
	if len(segments) > maxCompoundSegments ||
		(len(segments) == 1 && !singleShapeHeads[firstWord(segments[0])]) {
		plan.treeOnly = true
	}
	return plan, true
}

// allows runs the arm the plan chose.
func (p compoundPlan) allows(rules CompoundRules, cwd string, roots []string) bool {
	if p.treeOnly {
		return CompoundTreeAllows(p.command, rules, cwd, roots)
	}
	if len(p.segments) == 1 {
		return compoundSingleAllows(p.segments[0], p.command, rules, cwd, roots)
	}
	return compoundTextAllows(p.segments, rules, cwd, roots) ||
		CompoundTreeAllows(p.command, rules, cwd, roots)
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
