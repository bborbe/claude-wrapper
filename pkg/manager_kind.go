// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"strings"
)

// Which KIND of manager a spawn is — fleet, topic, goal, or unresolved.
//
// The seven-axis cap (2026-11-10) splits managers into a topic pair and a goal
// pair, and a manager's kind is not on the wire: `spawn_agent` carries
// `role: manager` and a `label`, and nothing else says whether the session runs
// a topic or a goal. So the kind is DERIVED from the label, by the rule the node
// supervisor's fleet board already applies to a live manager: the label is the
// subject's title, and the subject is a page under `23 Topics/` (a topic
// manager) or `24 Goals/` / `23 Goals/` (a goal manager).
//
// ⚠️ ONE RULE, TWO CALLERS, and that is the point. The spawn site classifies the
// session it is about to create and the count classifies the sessions already
// live; both call this, so a manager cannot be counted under one cap and opened
// under another.
//
// ⚠️ Exact, case-insensitive matching only — no slugging and no prefix matching.
// The board's truncation rule exists because a REGISTRY name may be clipped by
// the terminal; a label handed to a spawn is not clipped. A prefix match here
// would file a manager under a cap it does not belong to, and the failure is
// invisible: the cap simply never binds for that session.
//
// A label that resolves to no page is `unresolved` rather than a guess. It still
// answers to the manager cap and the session cap; only the topic/goal pair is
// skipped, because which of the two it belongs to is exactly what could not be
// resolved.
const (
	// ManagerKindFleet is the board's root, outside every manager cap.
	ManagerKindFleet = "fleet"
	// ManagerKindTopic is a manager whose subject is a `23 Topics/` page.
	ManagerKindTopic = "topic"
	// ManagerKindGoal is a manager whose subject is a `24 Goals/` or `23 Goals/` page.
	ManagerKindGoal = "goal"
	// ManagerKindUnresolved is a manager whose subject page could not be found.
	ManagerKindUnresolved = "unresolved"
)

// ManagerKinds is every kind the classifier can answer.
var ManagerKinds = []string{
	ManagerKindFleet, ManagerKindTopic, ManagerKindGoal, ManagerKindUnresolved,
}

// FleetManagerLabel is the root's registry name, excluded from the manager caps.
const FleetManagerLabel = "Fleet Manager"

// The subject directories, in the order the classifier reads them. They mirror
// `TOPIC_SUBDIRS` / `GOAL_SUBDIRS` in the node's `scripts/fleet-board.py`; the
// two are a hand-kept pair.
var (
	topicDirs = []string{"23 Topics"}
	goalDirs  = []string{"24 Goals", "23 Goals"}
)

// FileExists is the default existence probe the classifier uses.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// managerKindCandidateNames returns the spellings a launcher produces for one
// subject: the title itself, and `<Title> Manager`.
//
// A trailing suffix is stripped only when something precedes it, so a subject
// literally named `Manager` is not reduced to the empty string.
func managerKindCandidateNames(label string) []string {
	name := strings.TrimSpace(label)
	if name == "" {
		return nil
	}
	names := []string{name}
	if stripped := trimManagerSuffix(name); stripped != "" && stripped != name {
		names = append(names, stripped)
	}
	return names
}

// trimManagerSuffix drops a trailing ` Manager`, case-insensitively.
func trimManagerSuffix(name string) string {
	const suffix = " manager"
	if len(name) <= len(suffix) || !strings.EqualFold(name[len(name)-len(suffix):], suffix) {
		return ""
	}
	return strings.TrimSpace(name[:len(name)-len(suffix)])
}

// ManagerKindForLabel answers the kind of manager `label` names, given the vault
// roots to search and an injectable existence probe.
//
// Topics are tried before goals across every root and both spellings: the two
// tables are disjoint, so the order only decides which answer a title that
// somehow sits in both would give — and a topic page is the narrower claim.
func ManagerKindForLabel(label string, roots []string, exists func(string) bool) string {
	names := managerKindCandidateNames(label)
	if len(names) == 0 {
		return ManagerKindUnresolved
	}
	for _, name := range names {
		if strings.EqualFold(name, FleetManagerLabel) {
			return ManagerKindFleet
		}
	}
	if exists == nil {
		exists = FileExists
	}
	for _, dir := range topicDirs {
		if managerPageExists(roots, dir, names, exists) {
			return ManagerKindTopic
		}
	}
	for _, dir := range goalDirs {
		if managerPageExists(roots, dir, names, exists) {
			return ManagerKindGoal
		}
	}
	return ManagerKindUnresolved
}

// managerPageExists reports whether any root holds `<dir>/<name>.md` for any of
// the candidate names.
func managerPageExists(roots []string, dir string, names []string, exists func(string) bool) bool {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		for _, name := range names {
			if exists(filepath.Join(root, dir, name+".md")) {
				return true
			}
		}
	}
	return false
}
