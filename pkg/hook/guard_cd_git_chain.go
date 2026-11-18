// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// cdGitChainReason is the denial text of deny-cd-git-chain.py, kept
// byte-identical: the Git rules quote it.
const cdGitChainReason = "Refused: this command chains `cd <dir>` with `git`, which the rules " +
	"forbid (~/.claude/CLAUDE.md § Git — No Git -C Flag). Run `cd <dir>` in its own Bash " +
	"call, then `git …` in the next call: the cwd persists and both halves are " +
	"allowlisted on their own. If the target is outside this session's working " +
	"directories the harness undoes the `cd` (it prints `Shell cwd was reset to …`), " +
	"so use `git -C <dir> <sub>` in a single call instead."

var (
	// cdChainHeredoc matches a heredoc opener, but never a conflict marker. RE2
	// has no backreferences, so the delimiter's quotes are matched loosely — the
	// captured word is what the body scan compares against.
	cdChainHeredoc = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	// cdChainShells names the wrappers unwrapShellC understands.
	cdChainShells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true}
)

// cdChainKeywordPrefixes are peeled off a segment so a one-line loop body is
// still read as its own command.
var cdChainKeywordPrefixes = []string{"do ", "then ", "(", "{ "}

// CdGitChainGuard replaces deny-cd-git-chain.py.
//
// It denies a command whose top-level segments include a `cd <dir>` followed by a
// segment whose program is `git`. A command substitution, an interpreter script
// or a heredoc body hides the chain, and that fails open — the chain still
// reaches the normal permission flow, which is the prompt this guard exists to
// remove.
type CdGitChainGuard struct{}

// NewCdGitChainGuard returns the cd-git-chain guard.
func NewCdGitChainGuard() *CdGitChainGuard {
	return &CdGitChainGuard{}
}

// Name returns the script this guard replaces.
func (g *CdGitChainGuard) Name() string { return "deny-cd-git-chain.py" }

// Applies reports whether this is a Bash call.
func (g *CdGitChainGuard) Applies(input Input) bool { return input.IsBash() }

// Evaluate denies a cd-then-git chain, including one inside a `bash -c` payload.
func (g *CdGitChainGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	if strings.TrimSpace(command) == "" {
		return Decision{}
	}
	if !chainOffence(command) {
		inner, ok := unwrapShellC(command)
		if !ok || !chainOffence(inner) {
			return Decision{}
		}
	}
	return DenyDecision(cdGitChainReason)
}

// chainOffence reports whether a `cd` segment is followed by a `git` segment.
func chainOffence(command string) bool {
	segs, ok := splitTopLevel(stripAllHeredocBodies(command))
	if !ok {
		return false
	}
	seenCd := false
	for _, seg := range segs {
		switch program(seg) {
		case "cd":
			seenCd = true
		case "git":
			if seenCd {
				return true
			}
		}
	}
	return false
}

// splitTopLevel splits on && || |& ; | and newline, ignoring separators inside
// quotes. ok is false when a quote is left open, because the split cannot then be
// trusted and the command is left to the normal flow rather than denied on a
// guess.
func splitTopLevel(command string) ([]string, bool) {
	var segs []string
	var current strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		if quote != 0 {
			// Inside double quotes a backslash escapes the next character, so `\"`
			// does not close the string. Single quotes have no escapes in bash.
			if c == '\\' && quote == '"' && i+1 < len(command) {
				current.WriteByte(c)
				current.WriteByte(command[i+1])
				i++
				continue
			}
			current.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'':
			quote = c
			current.WriteByte(c)
		case c == '\\' && i+1 < len(command):
			current.WriteByte(c)
			current.WriteByte(command[i+1])
			i++
		case strings.HasPrefix(command[i:], "&&"), strings.HasPrefix(command[i:], "||"),
			strings.HasPrefix(command[i:], "|&"):
			segs = appendSegment(segs, current.String())
			current.Reset()
			i++
		case c == ';' || c == '|' || c == '\n':
			segs = appendSegment(segs, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, false
	}
	return appendSegment(segs, current.String()), true
}

// appendSegment adds a non-empty trimmed segment.
func appendSegment(segs []string, segment string) []string {
	if trimmed := strings.TrimSpace(segment); trimmed != "" {
		segs = append(segs, trimmed)
	}
	return segs
}

// program returns the command word of one segment, with a loop or block keyword
// peeled off.
func program(segment string) string {
	seg := strings.TrimSpace(segment)
	for changed := true; changed; {
		changed = false
		for _, keyword := range cdChainKeywordPrefixes {
			if strings.HasPrefix(seg, keyword) {
				seg = strings.TrimSpace(strings.TrimPrefix(seg, keyword))
				changed = true
			}
		}
	}
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// unwrapShellC returns the inner command of a `bash -c '…'` wrapper.
//
// The split is on ANY whitespace run, not one literal space: `bash  -c '…'` and a
// tab between the words are the same command, and a single-space split missed them
// so the chain inside was never seen.
func unwrapShellC(command string) (string, bool) {
	program, flag, inner, ok := splitFirstTwo(strings.TrimSpace(command))
	if !ok || !cdChainShells[filepath.Base(program)] || flag != "-c" {
		return "", false
	}
	inner = strings.TrimSpace(inner)
	if len(inner) >= 2 && inner[0] == inner[len(inner)-1] && (inner[0] == '"' || inner[0] == '\'') {
		inner = inner[1 : len(inner)-1]
	}
	return inner, true
}

// splitFirstTwo splits on whitespace into the first two words and the verbatim
// remainder.
func splitFirstTwo(text string) (string, string, string, bool) {
	first, rest, ok := cutWhitespace(text)
	if !ok {
		return "", "", "", false
	}
	second, remainder, ok := cutWhitespace(rest)
	if !ok {
		return "", "", "", false
	}
	return first, second, remainder, true
}

// cutWhitespace returns the text before the first whitespace run and the rest.
func cutWhitespace(text string) (string, string, bool) {
	index := strings.IndexFunc(text, unicode.IsSpace)
	if index < 0 {
		return text, "", false
	}
	return text[:index], strings.TrimLeftFunc(text[index:], unicode.IsSpace), true
}

// stripAllHeredocBodies drops every heredoc body: they are data, not shell text.
// Leaving them in place both hides a real chain behind a `<<'EOF'` delimiter and
// invents false ones from whatever the body contains.
func stripAllHeredocBodies(command string) string {
	lines := strings.Split(command, "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		kept = append(kept, lines[i])
		for _, match := range cdChainHeredoc.FindAllStringSubmatch(lines[i], -1) {
			delimiter := match[1]
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != delimiter {
				i++
			}
			if i+1 < len(lines) {
				i++
			}
		}
	}
	return strings.Join(kept, "\n")
}
