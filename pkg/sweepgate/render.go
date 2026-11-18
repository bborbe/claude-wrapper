// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// sessionCellRe matches a cell that is exactly a `[<sid8>]` token. Only that
// shape is ever resolved to a jump link; a fleet table's Session cell carries a
// session NAME and must not be linked from its own text.
var sessionCellRe = regexp.MustCompile(`^\[([0-9a-fA-F]{6,8})\]$`)

// noLinksValues are the BOX_TABLE_NO_LINKS spellings that disable jump links.
var noLinksValues = map[string]bool{"1": true, "true": true, "yes": true, "on": true}

// SessionCell renders the Session column: `[<sid8>]` alive, `(<sid8>)` not
// alive, `—` no id ever recorded.
//
// `—` is a PROVENANCE claim, never a liveness one: it is returned only when the
// whole id set is empty. `parked` is alive (the session holds a socket waiting on
// a gate), so it takes the live-shaped cell.
func SessionCell(t Task) string {
	ids := t.SessionIDs
	if ids == nil {
		if t.Session != "" {
			ids = []string{t.Session}
		}
	}
	if len(ids) == 0 {
		return "—"
	}
	sid := t.Session
	if sid == "" {
		sid = ids[len(ids)-1]
	}
	short := shortSessionID(sid)
	if t.Liveness == LivenessLive || t.Liveness == LivenessParked {
		return "[" + short + "]"
	}
	return "(" + short + ")"
}

// shortSessionID keeps the first eight characters of a session id.
func shortSessionID(sid string) string {
	runes := []rune(sid)
	if len(runes) <= 8 {
		return sid
	}
	return string(runes[:8])
}

// Marker is the frame's first line: the clock, the subject, the row count, the
// per-bucket tally and the verdict.
func Marker(cfg Config, prepared Prepared, changed bool) string {
	now := cfg.Now.Now().Time()
	counts := map[string]int{}
	for _, task := range prepared.Tasks {
		_, key := Bucket(now, prepared.Holds, task)
		counts[key]++
	}
	parts := make([]string, 0, len(BucketOrder))
	for _, key := range BucketOrder {
		if counts[key] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[key], Glyphs[key]))
		}
	}
	verdict := "no change"
	if changed {
		verdict = "change"
	}
	return fmt.Sprintf("%s ✓ %s — %d tasks · %s · %s",
		now.Format("15:04"), cfg.Subject, len(prepared.Tasks),
		strings.Join(parts, " · "), verdict)
}

// RenderTable draws the grouped box: one row per level, carried by indent alone.
//
// The root is flush left on BOTH branches. On the topic branch the root is the
// topic, a goal sits three spaces down and a task six; on the goal branch the
// goal IS the root and its tasks sit three spaces down. The goal branch therefore
// emits no synthetic topic row — that row drew the goal name twice.
//
// A task naming several member goals is listed once, under its first member goal.
// A task matching no member goal goes under one `(declared directly — no member
// goal)` row. A goal owning no tasks is skipped entirely.
//
// `now` is read once and passed to every Bucket and StatusLabel call, so one
// render cannot bucket two rows against two clocks.
func RenderTable(ctx context.Context, cfg Config, prepared Prepared, _ bool) (string, error) {
	now := cfg.Now.Now().Time()
	branch := prepared.Snapshot.Branch
	members := prepared.Snapshot.Members

	goalDepth, taskDepth := "   ", "      "
	if branch != "topic" {
		goalDepth, taskDepth = "", "   "
	}

	rows := make([][]string, 0, len(prepared.Tasks)+2)
	if branch == "topic" {
		rows = append(rows, topicRow(cfg, now))
	}

	groups := make(map[string][]Task, len(members))
	for _, member := range members {
		groups[member] = nil
	}
	ungrouped := make([]Task, 0)
	for _, task := range prepared.Tasks {
		placed := false
		for _, goal := range task.Goals {
			if _, ok := groups[goal]; ok {
				groups[goal] = append(groups[goal], task)
				placed = true
				break
			}
		}
		if !placed {
			ungrouped = append(ungrouped, task)
		}
	}

	for _, member := range members {
		owned := groups[member]
		if len(owned) == 0 {
			continue
		}
		rows = append(rows, goalRow(cfg, now, member, goalDepth))
		for _, task := range owned {
			rows = append(rows, taskRow(now, prepared.Holds, task, taskDepth))
		}
	}

	if len(ungrouped) > 0 {
		rows = append(rows, []string{
			goalDepth + "(declared directly — no member goal)", "—", "—", "—", "—",
		})
		for _, task := range ungrouped {
			rows = append(rows, taskRow(now, prepared.Holds, task, taskDepth))
		}
	}

	return renderBoxWithURLs(FrameHeader, rows, HeaderWidths, linkResolver(ctx)), nil
}

// topicRow renders the topic root row.
func topicRow(cfg Config, now time.Time) []string {
	text := readOptional(filepath.Join(cfg.Vault, "23 Topics", cfg.Subject+".md"))
	sc := CheckboxCount(text, "Success Criteria")
	gate := CheckboxCount(text, "Completion Gate")
	return []string{
		cfg.Subject,
		"—",
		StatusLabel(now, Task{Status: "in_progress"}),
		"—",
		fmt.Sprintf("SC %s · Gate %s", sc, gate),
	}
}

// goalRow renders one member-goal row from its own page.
func goalRow(cfg Config, now time.Time, member, depth string) []string {
	text := readOptional(filepath.Join(cfg.Vault, "24 Goals", member+".md"))
	status := "in_progress"
	if fm, ok := SplitFrontmatter(text); ok {
		if value := FMScalar(fm, "status"); value != "" {
			status = value
		}
	}
	return []string{
		depth + member,
		"—",
		StatusLabel(now, Task{Status: status}),
		"—",
		CheckboxCount(text, "Success Criteria"),
	}
}

// taskRow renders one task row at the given depth.
func taskRow(now time.Time, holds map[string]Hold, task Task, depth string) []string {
	label, _ := Bucket(now, holds, task)
	phase := task.Phase
	if phase == "" {
		phase = "—"
	}
	met := task.Met
	if met == "" {
		met = "—"
	}
	return []string{depth + task.Name, SessionCell(task), label, phase, met}
}

// readOptional returns a page's text, or "" when it cannot be read. An
// unreadable topic or goal page contributes empty text, never an error.
func readOptional(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Frame assembles the marker, the box two spaces under it and the action lines
// two spaces under the box.
func Frame(ctx context.Context, cfg Config, prepared Prepared, changed bool) (string, error) {
	table, err := RenderTable(ctx, cfg, prepared, changed)
	if err != nil {
		return "", err
	}
	actions, err := ActionLines(ctx, cfg, prepared)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(actions)+2)
	lines = append(lines, Marker(cfg, prepared, changed))
	lines = append(lines, "  "+strings.ReplaceAll(table, "\n", "\n  "))
	for _, action := range actions {
		lines = append(lines, "  "+action)
	}
	return strings.Join(lines, "\n"), nil
}

// linkResolver returns the per-cell jump-link resolver for one render.
//
// It resolves each `[<sid8>]` prefix at most once, and every failure — a
// non-zero exit, a non-numeric pane, a URL that is not http — yields no link, so
// the cell renders plain. BOX_TABLE_NO_LINKS disables the whole thing.
func linkResolver(ctx context.Context) func(string) string {
	if noLinksValues[strings.ToLower(strings.TrimSpace(os.Getenv("BOX_TABLE_NO_LINKS")))] {
		return func(string) string { return "" }
	}
	runner := ExecRunner{}
	cache := map[string]string{}
	return func(cell string) string {
		match := sessionCellRe.FindStringSubmatch(strings.TrimSpace(StripLinks(cell)))
		if match == nil {
			return ""
		}
		sid := match[1]
		if url, ok := cache[sid]; ok {
			return url
		}
		url := resolveJumpURL(ctx, runner, sid)
		cache[sid] = url
		return url
	}
}

// resolveJumpURL turns a session-id prefix into a jump URL in two hops:
// who-needs-me.py --pane-for, then jump-link.py <pane>.
func resolveJumpURL(ctx context.Context, runner ExitRunner, sid string) string {
	scripts := scriptsDir()
	paneOut, paneExit, err := runner.Run(ctx,
		filepath.Join(scripts, "who-needs-me.py"), "--pane-for", sid)
	if err != nil || paneExit != 0 {
		return ""
	}
	pane := strings.TrimSpace(string(paneOut))
	if _, convErr := strconv.Atoi(pane); convErr != nil {
		return ""
	}
	linkOut, linkExit, err := runner.Run(ctx, filepath.Join(scripts, "jump-link.py"), pane)
	if err != nil || linkExit != 0 {
		return ""
	}
	url := strings.TrimSpace(string(linkOut))
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ""
	}
	return url
}
