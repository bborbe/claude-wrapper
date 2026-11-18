// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"regexp"
	"strings"
)

// attributionReason is the denial text of deny-attribution.sh, kept
// byte-identical because the rule it enforces is quoted in the vault.
const attributionReason = "Refused: Claude/AI attribution detected in a commit message or PR/issue body " +
	"(e.g. 'Generated with Claude Code', 'Co-Authored-By: Claude'). The No-Claude-Attribution " +
	"rule forbids it in commits AND PR bodies. Remove the attribution line and retry."

// attributionVendors is shared by both attribution forms, so a new vendor is
// added once and both pick it up.
const attributionVendors = `claude|deepseek|gpt|openai|gemini|copilot|cursor|codex|llama|` +
	`mistral|qwen|grok|anthropic`

var (
	// attributionMarkers match an attribution line naming a vendor. The vendor
	// is anchored behind "generated with" or "co-authored-by:", so a commit
	// merely *about* a cursor or a codex still passes.
	attributionMarkers = regexp.MustCompile(`(?i)generated with (\[)?(` + attributionVendors +
		`)|co-authored-by:[[:space:]]*(` + attributionVendors + `)|🤖[[:space:]]*generated|` +
		`claude\.com/claude-code`)
	// attributionBodyFile finds the file arguments whose contents join the
	// haystack.
	attributionBodyFile = regexp.MustCompile(`(--body-file|--body-path|-F)[= ]+([^ ]+)`)
)

// attributionTriggers are the commands that publish text into git history or
// GitHub. Anything else is not this guard's business.
var attributionTriggers = []string{
	"git commit",
	"gh pr create",
	"gh pr edit",
	"gh pr comment",
	"gh pr merge",
	"gh issue create",
	"gh issue comment",
	"gh release create",
}

// AttributionGuard replaces deny-attribution.sh.
//
// It enforces the No-Claude-Attribution rule mechanically, in commit messages
// and in PR/issue bodies alike. It cannot see attribution typed into an
// interactive editor, or piped from an unreadable source.
type AttributionGuard struct{}

// NewAttributionGuard returns the attribution guard.
func NewAttributionGuard() *AttributionGuard {
	return &AttributionGuard{}
}

// Name returns the script this guard replaces.
func (g *AttributionGuard) Name() string { return "deny-attribution.sh" }

// Applies reports whether this is a Bash call that publishes text.
func (g *AttributionGuard) Applies(input Input) bool {
	if !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return MatchesAny(command, attributionTriggers...)
}

// Evaluate denies a command whose text, or whose body file, carries an AI
// attribution line.
func (g *AttributionGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	haystack := command
	for _, match := range attributionBodyFile.FindAllStringSubmatch(command, -1) {
		haystack += "\n" + readBodyFile(match[2])
	}
	if attributionMarkers.MatchString(haystack) {
		return DenyDecision(attributionReason)
	}
	return Decision{}
}

// readBodyFile returns the file's contents, or the empty string when it cannot
// be read. The script treats an unreadable body file as absent rather than as an
// error, because a missing file is the harness's problem, not the command's.
func readBodyFile(path string) string {
	path = strings.Trim(path, `'"`)
	if path == "-" || path == "" {
		return ""
	}
	data, err := os.ReadFile(path) //#nosec G304 -- the path comes from the command being gated
	if err != nil {
		return ""
	}
	return string(data)
}
