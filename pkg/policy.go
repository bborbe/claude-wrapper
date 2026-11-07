// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"strings"
	"unicode"
)

// Action is a policy rule's verdict.
type Action string

const (
	// ActionAllow lets the tool run without a manager.
	ActionAllow Action = "allow"
	// ActionDeny refuses the tool without a manager.
	ActionDeny Action = "deny"
	// ActionEscalate leaves the park for the manager.
	ActionEscalate Action = "escalate"
)

// Rule is one declarative approval rule.
//
// The field set mirrors the supervisor's `policy.json` exactly, because the
// policy file format is out of this slice's scope: only the deciding process
// moves, so a file written for the node server must evaluate here identically.
type Rule struct {
	Tool   string `json:"tool"`
	Match  string `json:"match"`
	Action Action `json:"action"`
	// MatchType selects the matcher. Empty keeps the historical substring
	// behaviour; "command" makes Match a whole-token prefix of a Bash command.
	MatchType string `json:"matchType"`
}

// Verdict is what the policy decided about one request.
type Verdict struct {
	Action Action
	Key    string
	// Rule is the rule that decided, or nil when nothing matched and the
	// request escalated. A synthetic rule is reported for a chained Bash
	// command allowed segment by segment.
	Rule *Rule
}

// MatchedRule is the `matched_rule` string a permissions record carries.
//
// It is `"<tool>:<match>"` or nil, the same shape the node server writes, so a
// record from either process is comparable in one log.
func (v Verdict) MatchedRule() *string {
	if v.Rule == nil {
		return nil
	}
	matched := v.Rule.Tool + ":" + v.Rule.Match
	return &matched
}

// InputKey is the value a rule matches against.
//
// Raw tool input is useless for mining — every Write differs — so it is
// normalized to the one field a rule could actually name.
func InputKey(input map[string]any) string {
	for _, field := range []string{"file_path", "path", "command", "pattern", "url"} {
		value, ok := input[field]
		if !ok {
			continue
		}
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

// hasShellMetacharacter reports whether the command carries anything that lets
// a second command ride along, plus redirection.
//
// A substring rule cannot see these: `match: "ls "` is contained in
// `rm -rf ~/Documents && ls `, so an allow written that way is a universal
// bypass.
func hasShellMetacharacter(command string) bool {
	for _, r := range command {
		switch r {
		case ';', '&', '|', '`', '<', '>', '\n', '(', ')', '{', '}':
			return true
		}
	}
	return false
}

// hasNonShellWhitespace reports whether the command carries whitespace the shell
// does not split on.
//
// The shell splits on space and tab only. unicode.IsSpace also matches U+00A0
// and other Unicode spaces, which bash keeps INSIDE a word — so `ls<U+00A0>x`
// would read as `ls` here while bash looks up a program literally named
// `ls<U+00A0>x`.
func hasNonShellWhitespace(command string) bool {
	for _, r := range command {
		if r == ' ' || r == '\t' {
			continue
		}
		if unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// isEnvAssignment reports whether the first token picks the environment.
//
// `PATH=/tmp/evil ls` runs /tmp/evil/ls, and `LD_PRELOAD=/tmp/x.so ls` loads
// attacker code into a genuine `ls`, so an assignment is refused rather than
// skipped.
func isEnvAssignment(token string) bool {
	index := strings.IndexByte(token, '=')
	if index <= 0 {
		return false
	}
	for position, r := range token {
		if position == index {
			return true
		}
		if !isIdentifierRune(r) {
			return false
		}
	}
	return false
}

func isLetterOrUnderscore(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// isIdentifierRune is the character class an environment variable name may use
// after its first character.
func isIdentifierRune(r rune) bool {
	return isLetterOrUnderscore(r) || (r >= '0' && r <= '9')
}

// CommandTokens returns the command's space/tab-separated tokens, or nil when
// the command is compound, carries an environment assignment, or contains
// whitespace the shell would not split on — any of which makes it unsafe to
// match on at all.
func CommandTokens(command string) []string {
	if hasShellMetacharacter(command) || hasNonShellWhitespace(command) {
		return nil
	}
	tokens := strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '\t'
	})
	if len(tokens) == 0 {
		return nil
	}
	if isEnvAssignment(tokens[0]) {
		return nil
	}
	return tokens
}

// RuleMatches reports whether one rule decides the given request.
func RuleMatches(rule Rule, toolName string, key string, cwd string) bool {
	if rule.Tool != "*" && rule.Tool != toolName {
		return false
	}
	if rule.MatchType == "command" {
		return commandPrefixMatches(rule.Match, key)
	}
	return plainMatchMatches(rule.Match, key, cwd)
}

// commandPrefixMatches is the anchored matcher.
//
// `match` is a whole-token PREFIX of the command: `git status` matches
// `git status -sb` but not `git push`, and a compound or env-prefixed command
// never matches. Only the prefix is anchored, so a prefix is listed only when
// every extension of it is read-only.
func commandPrefixMatches(match string, key string) bool {
	tokens := CommandTokens(key)
	if tokens == nil {
		return false
	}
	if match == "*" {
		return true
	}
	prefix := strings.Fields(strings.TrimSpace(match))
	if len(prefix) == 0 || len(prefix) > len(tokens) {
		return false
	}
	for index := range prefix {
		if tokens[index] != prefix[index] {
			return false
		}
	}
	return true
}

// plainMatchMatches is the historical matcher: `*`, `cwd`, or a substring.
func plainMatchMatches(match string, key string, cwd string) bool {
	if match == "*" {
		return true
	}
	if match == "cwd" {
		// Compare on a path BOUNDARY, not a raw string prefix: `/work/repo-2/x`
		// starts with `/work/repo` but is a sibling directory.
		if key == "" || cwd == "" {
			return false
		}
		base := cwd
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		return key == cwd || strings.HasPrefix(key, base)
	}
	return strings.Contains(key, match)
}

// harmlessRedirects are the stderr discards that change no file and run nothing.
var harmlessRedirects = map[string]struct{}{
	"2>/dev/null": {},
	"2>&1":        {},
}

// CommandSegments returns the segments of a chained command — split on `&&`,
// `||`, `;` and `|` — each with its harmless redirects removed, or nil when any
// segment is empty.
//
// Splitting ignores quoting on purpose: a separator inside quotes only yields
// extra, stranger segments, each of which must still pass the anchored match on
// its own, so mis-splitting can refuse a command but never allow one the shell
// would run differently.
func CommandSegments(command string) []string {
	raw := splitCommand(command)
	if len(raw) < 2 {
		return nil
	}
	segments := make([]string, 0, len(raw))
	for _, segment := range raw {
		tokens := strings.FieldsFunc(segment, func(r rune) bool {
			return r == ' ' || r == '\t'
		})
		kept := make([]string, 0, len(tokens))
		for _, token := range tokens {
			if _, ok := harmlessRedirects[token]; ok {
				continue
			}
			kept = append(kept, token)
		}
		joined := strings.TrimSpace(strings.Join(kept, " "))
		if joined == "" {
			return nil
		}
		segments = append(segments, joined)
	}
	return segments
}

// splitCommand splits on the four shell separators, longest first so `&&` and
// `||` are not read as two single-character separators.
func splitCommand(command string) []string {
	segments := make([]string, 0, 2)
	start := 0
	for index := 0; index < len(command); index++ {
		if strings.HasPrefix(command[index:], "&&") || strings.HasPrefix(command[index:], "||") {
			segments = append(segments, command[start:index])
			index++
			start = index + 1
			continue
		}
		if command[index] == ';' || command[index] == '|' {
			segments = append(segments, command[start:index])
			start = index + 1
		}
	}
	return append(segments, command[start:])
}

// isCatchAll reports whether the deciding rule is only the generic fallthrough.
//
// Only that verdict may be revisited segment by segment; a rule that named the
// command — a deny, or an escalate the owner wrote on purpose — always stands.
func isCatchAll(rule *Rule) bool {
	if rule == nil {
		return true
	}
	return rule.Match == "*" && rule.MatchType == "" && rule.Action == ActionEscalate
}

// allSegmentsAnchoredAllow reports whether every segment of a chained Bash
// command is allowed by an anchored rule.
//
// Substring allows never count here: they are the rules composition smuggles
// past, which is why the anchored mode exists.
func allSegmentsAnchoredAllow(rules []Rule, toolName string, key string, cwd string) bool {
	segments := CommandSegments(key)
	if segments == nil {
		return false
	}
	for _, segment := range segments {
		verdict := decideWhole(rules, toolName, segment, cwd)
		if verdict.Action != ActionAllow {
			return false
		}
		if verdict.Rule == nil || verdict.Rule.MatchType != "command" {
			return false
		}
	}
	return true
}

// decideWhole returns the first rule that matches, or an escalate verdict.
func decideWhole(rules []Rule, toolName string, key string, cwd string) Verdict {
	for index := range rules {
		if RuleMatches(rules[index], toolName, key, cwd) {
			return Verdict{Action: rules[index].Action, Key: key, Rule: &rules[index]}
		}
	}
	return Verdict{Action: ActionEscalate, Key: key}
}

// Decide evaluates one request against the ordered rule list.
//
// First match wins; the caller orders per-spawn rules before the user overlay
// before the bundled defaults, so the overlay semantics live at the call site. A
// Bash command that only the catch-all escalated gets a second look segment by
// segment.
func Decide(rules []Rule, toolName string, input map[string]any, cwd string) Verdict {
	key := InputKey(input)
	whole := decideWhole(rules, toolName, key, cwd)
	if toolName == "Bash" && whole.Action == ActionEscalate && isCatchAll(whole.Rule) &&
		allSegmentsAnchoredAllow(rules, toolName, key, cwd) {
		return Verdict{
			Action: ActionAllow,
			Key:    key,
			Rule:   &Rule{Tool: "Bash", Match: "segments", MatchType: "segments", Action: ActionAllow},
		}
	}
	return whole
}

// OverlayRules places an override ahead of a base set.
//
// Overlay rather than replace: first match wins, so a rule in the override beats
// any later one, while the base set still covers whatever the override does not
// name — a permissive override must not silently drop the `rm -rf` deny along
// with it. Full replacement stays reachable: end the override with a
// `{"tool":"*","match":"*","action":"escalate"}` catch-all.
func OverlayRules(override []Rule, base []Rule) []Rule {
	merged := make([]Rule, 0, len(override)+len(base))
	merged = append(merged, override...)
	merged = append(merged, base...)
	return merged
}
