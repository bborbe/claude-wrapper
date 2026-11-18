// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// blockedLogEntry is one line of the raw blocked-permission set.
type blockedLogEntry struct {
	Key     string `json:"key"`
	Tool    string `json:"tool"`
	Session string `json:"session"`
	Summary string `json:"summary"`
}

// BlockedLogGuard replaces blocked-log.sh.
//
// It tracks the permission prompts currently blocked: added on a request, removed
// when the tool ran or the prompt was denied. It maintains the raw set and a
// human-readable page, and never blocks or slows the turn.
type BlockedLogGuard struct{}

// NewBlockedLogGuard returns the blocked-permission guard.
func NewBlockedLogGuard() *BlockedLogGuard {
	return &BlockedLogGuard{}
}

// Name returns the script this guard replaces.
func (g *BlockedLogGuard) Name() string { return "blocked-log.sh" }

// Applies reports whether this event changes the blocked set.
func (g *BlockedLogGuard) Applies(input Input) bool {
	switch input.HookEventName {
	case EventPermissionRequest, EventPostToolUse, EventPermissionDenied:
		return true
	default:
		return false
	}
}

// Evaluate records the add or the removal, and never blocks.
func (g *BlockedLogGuard) Evaluate(input Input) Decision {
	tool := input.ToolName
	payload := canonicalToolInput(input.ToolInput)
	key := blockedLogKey(tool, payload)
	summary := payload
	if len([]rune(summary)) > 160 {
		summary = string([]rune(summary)[:157]) + "..."
	}
	home := filepath.Join(os.Getenv("HOME"), ".claude")
	db := filepath.Join(home, "state", "blocked-permissions.jsonl")
	page := filepath.Join(home, "blocked.md")
	if err := os.MkdirAll(filepath.Dir(db), 0o750); err != nil { //#nosec G703 -- own state dir
		return Decision{}
	}
	lines := readBlockedLines(db)
	if input.HookEventName == EventPermissionRequest {
		lines = addBlockedEntry(lines, blockedLogEntry{
			Key: key, Tool: tool, Session: input.SessionID, Summary: summary,
		})
	} else {
		lines = removeBlockedEntry(lines, key)
	}
	writeBlockedFiles(db, page, lines)
	return Decision{}
}

// canonicalToolInput returns the tool input as sorted-key JSON, which is what the
// key is derived from.
func canonicalToolInput(raw json.RawMessage) string {
	var payload any
	if len(raw) == 0 {
		return "{}"
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "{}"
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// blockedLogKey derives the tool+input identity of one prompt.
func blockedLogKey(tool string, payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return tool + ":" + hex.EncodeToString(sum[:])[:16]
}

// readBlockedLines returns the non-empty lines of the raw set.
func readBlockedLines(path string) []string {
	data, err := os.ReadFile(path) //#nosec G304 G703 -- a fixed name under the operator's state dir
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// addBlockedEntry appends the entry unless its key is already present.
func addBlockedEntry(lines []string, entry blockedLogEntry) []string {
	for _, line := range lines {
		if blockedLineKey(line) == entry.Key {
			return lines
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return lines
	}
	return append(lines, string(data))
}

// removeBlockedEntry drops every line carrying key. An unparsable line is kept:
// it carries no key, so it is not this entry.
func removeBlockedEntry(lines []string, key string) []string {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if blockedLineKey(line) != key {
			kept = append(kept, line)
		}
	}
	return kept
}

// blockedLineKey returns the key of one raw line, or the empty string.
func blockedLineKey(line string) string {
	var entry blockedLogEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return ""
	}
	return entry.Key
}

// writeBlockedFiles rewrites the raw set and the human-readable page.
func writeBlockedFiles(db string, page string, lines []string) {
	body := strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	_ = os.WriteFile(db, []byte(body), 0o600)                       //#nosec G304 G703 -- fixed names under the state dir
	_ = os.WriteFile(page, []byte(renderBlockedPage(lines)), 0o600) //#nosec G304 G703 -- fixed names
}

// renderBlockedPage renders the human-readable page.
func renderBlockedPage(lines []string) string {
	if len(lines) == 0 {
		return "# Blocked permissions\n\nNone pending.\n"
	}
	var page strings.Builder
	page.WriteString("# Blocked permissions\n\n")
	for _, line := range lines {
		var entry blockedLogEntry
		_ = json.Unmarshal([]byte(line), &entry)
		page.WriteString("- **" + entry.Session + "** · `" + entry.Tool + "` · " +
			entry.Summary + "\n")
	}
	return page.String()
}
