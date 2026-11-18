// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

const (
	blockListPattern    = `(?m)^%s:\s*\n((?:[ \t]+-.*\n?)*)`
	metricsBlockPattern = `(?m)^%s:\s*\n((?:[ \t]+.*\n?)*)`
)

var (
	frontmatterRe    = regexp.MustCompile(`(?s)^---\n(.*?)\n---`)
	wikilinkRe       = regexp.MustCompile(`\[\[(.+?)\]\]`)
	metricsSessionRe = regexp.MustCompile(`(?m)^[ \t]+-?\s*session_id:\s*(.*)$`)
	progressHeaderRe = regexp.MustCompile(`(?m)^# Progress\s*\n`)
	goalsHeaderRe    = regexp.MustCompile(`(?m)^## Goals\s*\n`)
	checkboxRe       = regexp.MustCompile(`^\s*-\s*\[([ xX/])\]`)
	struckRowRe      = regexp.MustCompile(`^\s*-\s*\[ \]\s*~~\[\[[^\]]+\]\]~~`)
	goalsItemRe      = regexp.MustCompile(`^\s*-\s*\[\[(.+?)\]\]`)
)

// yamlNulls are YAML's null spellings. FMScalar is a raw regex with no YAML
// decoding, so a bare `null` reaches it as the four-character string "null".
var yamlNulls = map[string]bool{"": true, "~": true, "null": true, "Null": true, "NULL": true}

// SplitFrontmatter returns the frontmatter body, or false when there is none.
func SplitFrontmatter(text string) (string, bool) {
	m := frontmatterRe.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// FMScalar reads one frontmatter scalar.
func FMScalar(fm, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(.*)$`)
	m := re.FindStringSubmatch(fm)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// FMSessionID reads a recorded session id, or "" when the field holds no id.
//
// A null-ish scalar is NO id, not the id "null". Quoted spellings collapse too:
// surrounding quotes are stripped first, so a quoted "null" is the same no-id.
func FMSessionID(fm, key string) string {
	value := strings.Trim(FMScalar(fm, key), `'"`)
	if yamlNulls[value] {
		return ""
	}
	return value
}

// FMMetricsSessionIDs returns every `session_id` under `metrics_sessions`, in file
// order.
func FMMetricsSessionIDs(fm, key string) []string {
	block := findBlock(fm, metricsBlockPattern, key)
	if block == "" {
		return []string{}
	}
	matches := metricsSessionRe.FindAllStringSubmatch(block, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		value := strings.Trim(strings.TrimSpace(m[1]), `'"`)
		if value != "" && !yamlNulls[value] {
			ids = append(ids, value)
		}
	}
	return ids
}

// SessionIDSet is the row's whole id set, in the reader's own order.
func SessionIDSet(fm string) []string {
	out := make([]string, 0, 2)
	seen := map[string]bool{}
	own := FMSessionID(fm, "claude_session_id")
	candidates := FMMetricsSessionIDs(fm, "metrics_sessions")
	if own != "" {
		candidates = append([]string{own}, candidates...)
	}
	for _, sid := range candidates {
		if !seen[sid] {
			seen[sid] = true
			out = append(out, sid)
		}
	}
	return out
}

// FMWikilinks returns every `[[...]]` under key, accepting all four shapes the
// vault writes: block list, inline flow list, quoted-wikilink scalar, and the
// bare-name scalar.
func FMWikilinks(fm, key string) []string {
	rest := findScalarRest(fm, key)
	if rest != "" && rest != "[]" {
		if found := wikilinkRe.FindAllStringSubmatch(rest, -1); len(found) > 0 {
			return collectGroups(found)
		}
		return splitScalarList(rest)
	}
	block := findBlock(fm, blockListPattern, key)
	if block == "" {
		return []string{}
	}
	out := make([]string, 0)
	for _, line := range strings.Split(block, "\n") {
		if hit := wikilinkRe.FindStringSubmatch(line); hit != nil {
			out = append(out, hit[1])
			continue
		}
		if item := bareListItem(line); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// FMList reads a frontmatter list under key, accepting both the `[[Name]]` and
// the bare `Name` shapes.
func FMList(fm, key string) []string {
	rest := findScalarRest(fm, key)
	if rest != "" && rest != "[]" {
		found := wikilinkRe.FindAllStringSubmatch(rest, -1)
		if len(found) == 0 {
			return splitScalarList(rest)
		}
		return collectGroups(found)
	}
	block := findBlock(fm, blockListPattern, key)
	if block == "" {
		return []string{}
	}
	out := make([]string, 0)
	for _, line := range strings.Split(block, "\n") {
		item := strings.TrimSpace(line)
		item = strings.TrimSpace(strings.TrimLeft(item, "-"))
		item = strings.Trim(item, `'"`)
		if hit := wikilinkRe.FindStringSubmatch(item); hit != nil {
			item = hit[1]
		}
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// ProgressHash is sha256 of the `# Progress` section body, hex, first 16 chars.
//
// An empty section or no section hashes the empty string.
func ProgressHash(text string) string {
	body, _ := cutSection(text, progressHeaderRe, []string{"\n# "})
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])[:16]
}

// CheckboxCount renders `n/m` ticked-vs-total checkboxes, optionally scoped to a
// `# <section>`.
//
// A section with no boxes, or an absent section, returns `—`. A struck row is
// excluded: its content begins with a struck wikilink and its box is `[ ]`.
func CheckboxCount(text, section string) string {
	body := text
	if section != "" {
		header := regexp.MustCompile(`(?m)^# ` + regexp.QuoteMeta(section) + `\s*\n`)
		extracted, ok := cutSection(text, header, []string{"\n# "})
		if !ok {
			return "—"
		}
		body = extracted
	}
	boxes := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		hit := checkboxRe.FindStringSubmatch(line)
		if hit == nil || struckRowRe.MatchString(line) {
			continue
		}
		boxes = append(boxes, hit[1])
	}
	if len(boxes) == 0 {
		return "—"
	}
	ticked := 0
	for _, box := range boxes {
		if box == "x" || box == "X" {
			ticked++
		}
	}
	return fmt.Sprintf("%d/%d", ticked, len(boxes))
}

// DeclaredMembers reads a topic page's `## Goals` list — declared membership,
// never inferred.
//
// Only `- [[...]]` list items count. A `###` sub-heading does NOT end the section;
// the next `##` or `#` does. No `## Goals` section is an error.
func DeclaredMembers(topicPath string) ([]string, error) {
	ctx := context.Background()
	raw, err := os.ReadFile(topicPath)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read topic page %s", topicPath)
	}
	body, ok := cutSection(string(raw), goalsHeaderRe, []string{"\n## ", "\n# "})
	if !ok {
		return nil, errors.Errorf(ctx, "no `## Goals` section in %s", topicPath)
	}
	members := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		if hit := goalsItemRe.FindStringSubmatch(line); hit != nil {
			members = append(members, strings.TrimSpace(hit[1]))
		}
	}
	return members, nil
}

// Task carries the frontmatter-derived fields `read_task` returns, plus the two
// enrichment inputs the digest reads.
type Task struct {
	Name            string
	Status          string
	Phase           string
	OperatorFlagged bool
	Role            string
	Session         string
	SessionIDs      []string
	Goals           []string
	BlockedBy       []string
	DeferDate       string
	DeclaredWait    string
	ProgressHash    string
	Met             string
	Mtime           time.Time
	// Liveness and Stuck are set by a later enrichment pass. They default to
	// "none" and false, which is what the digest uses for a caller that has not
	// enriched them (the fixture calls the digest before enrichment).
	Liveness string
	Stuck    bool

	// The render-only flags below are set by the classification pass and are NOT
	// inputs to Digest. Adding one to Digest would silently change what "unchanged"
	// means and break lockstep with the plugin's port.
	InFlight           bool
	ExternalWait       bool
	DeclaredWaitActive bool
	UnregisteredPark   bool
	FeedGate           bool
	BlockedUpstream    bool
	BlockedByUnmet     []string
}

// ParseTask parses a task page, returning false when there is no frontmatter.
func ParseTask(name, text string, mtime time.Time) (Task, bool) {
	fm, ok := SplitFrontmatter(text)
	if !ok {
		return Task{}, false
	}
	return Task{
		Name:            name,
		Status:          FMScalar(fm, "status"),
		Phase:           FMScalar(fm, "phase"),
		OperatorFlagged: operatorFlagged(fm),
		Role:            FMScalar(fm, "role"),
		Session:         FMSessionID(fm, "claude_session_id"),
		SessionIDs:      SessionIDSet(fm),
		Goals:           FMWikilinks(fm, "goals"),
		BlockedBy:       FMList(fm, "blocked_by"),
		DeferDate:       strings.Trim(FMScalar(fm, "defer_date"), `'"`),
		DeclaredWait:    strings.Trim(FMScalar(fm, "declared_wait"), `'"`),
		ProgressHash:    ProgressHash(text),
		Met:             CheckboxCount(text, ""),
		Mtime:           mtime,
	}, true
}

// ReadTask reads one task page and its mtime.
//
// The mtime comes from `fstat` on the descriptor that was just read, never a
// second stat on the path: the path could be replaced between the two calls. An
// unreadable file returns false with a nil error, matching the reference.
func ReadTask(ctx context.Context, path string) (Task, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return Task{}, false, nil
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(file)
	if err != nil {
		return Task{}, false, nil
	}
	info, err := file.Stat()
	if err != nil {
		return Task{}, false, nil
	}
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	task, ok := ParseTask(name, string(raw), info.ModTime())
	if !ok {
		return Task{}, false, nil
	}
	return task, true, nil
}

// operatorFlagged reads the operator-set flag, matching the supervisor's
// strict YAML-boolean predicate.
func operatorFlagged(fm string) bool {
	truthy := map[string]bool{
		"true": true, "True": true, "TRUE": true,
		"yes": true, "Yes": true, "YES": true,
		"on": true, "On": true, "ON": true, "y": true, "Y": true,
	}
	setBy := strings.Trim(strings.TrimSpace(FMScalar(fm, "flag_set_by")), `'"`)
	return truthy[strings.TrimSpace(FMScalar(fm, "flag"))] &&
		(setBy == "operator" || setBy == "legacy")
}

// findScalarRest returns the raw text after `key:` on its own line.
func findScalarRest(fm, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:(.*)$`)
	m := re.FindStringSubmatch(fm)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// findBlock returns the indented block under `key:`, or "".
func findBlock(fm, pattern, key string) string {
	re := regexp.MustCompile(fmt.Sprintf(pattern, regexp.QuoteMeta(key)))
	m := re.FindStringSubmatch(fm)
	if m == nil {
		return ""
	}
	return m[1]
}

// splitScalarList splits a comma list, trimming quotes from each item.
func splitScalarList(rest string) []string {
	parts := strings.Split(rest, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, strings.Trim(trimmed, `'"`))
	}
	return out
}

// collectGroups returns group 1 of every match.
func collectGroups(matches [][]string) []string {
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

// bareListItem reduces a bracketless `goals:` item to its page name.
func bareListItem(line string) string {
	value := strings.TrimSpace(line)
	previous := ""
	for previous != value {
		previous = value
		value = strings.TrimSpace(value)
		for strings.HasPrefix(value, "-") {
			value = strings.TrimSpace(value[1:])
		}
		for len(value) >= 2 && value[0] == value[len(value)-1] &&
			(value[0] == '\'' || value[0] == '"') {
			value = strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	if idx := strings.IndexByte(value, '|'); idx >= 0 {
		value = value[:idx]
	}
	if idx := strings.IndexByte(value, '#'); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

// cutSection returns the body after a header match, up to the earliest terminator.
func cutSection(text string, header *regexp.Regexp, terminators []string) (string, bool) {
	loc := header.FindStringIndex(text)
	if loc == nil {
		return "", false
	}
	body := text[loc[1]:]
	end := len(body)
	for _, terminator := range terminators {
		if idx := strings.Index(body, terminator); idx >= 0 && idx < end {
			end = idx
		}
	}
	return body[:end], true
}
