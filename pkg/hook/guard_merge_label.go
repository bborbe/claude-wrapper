// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// The denial texts of block-do-not-merge-merge.sh, kept byte-identical.
const (
	mergeLabelMultiTarget = "Blocked by block-do-not-merge-merge: more than one gh pr merge " +
		"target in one command — run each merge as its own call."
	mergeLabelMultiRepo = "Blocked by block-do-not-merge-merge: more than one --repo in one " +
		"merge command."
	mergeLabelBlocked = "Blocked by block-do-not-merge-merge: PR carries the do-not-merge " +
		"label (human review pending). Remove the label after owner approval, then merge."
)

var (
	// mergeLabelInvocation anchors on the gh invocation itself, so a quoted `pr merge N`
	// or an unrelated -R earlier in the line cannot shadow the real target.
	mergeLabelInvocation = regexp.MustCompile(
		`gh([[:space:]]+(-R|--repo)[= ]?[^ ]+)?[[:space:]]+pr[[:space:]]+merge` +
			`([[:space:]]+[0-9]+)?`,
	)
	mergeLabelRepoFlag  = regexp.MustCompile(`(--repo|-R)[= ]?[^ ]+`)
	mergeLabelRepoStrip = regexp.MustCompile(`^(--repo|-R)[= ]*`)
	mergeLabelTailRepo  = regexp.MustCompile(`(^|[[:space:]])(--repo|-R)[= ]?[^ ]+`)
	mergeLabelTailStrip = regexp.MustCompile(`^[[:space:]]*(--repo|-R)[= ]*`)
	mergeLabelID        = regexp.MustCompile(`merge[[:space:]]+([0-9]+)$`)
)

// ghRunner runs one gh invocation and returns its stdout.
type ghRunner func(args ...string) (string, error)

// runGh is the real runner: one exec per query.
func runGh(args ...string) (string, error) {
	out, err := exec.Command("gh", args...).Output() //#nosec G204 -- fixed binary, arguments are parsed from the command
	return string(out), err
}

// MergeLabelGuard replaces block-do-not-merge-merge.sh.
//
// It blocks `gh pr merge` while the target PR carries the `do-not-merge` label, and
// fails open on any parse or network error: a label lookup that cannot answer must
// never block a merge the operator asked for.
type MergeLabelGuard struct {
	run ghRunner
}

// NewMergeLabelGuard returns the merge-label guard.
func NewMergeLabelGuard() *MergeLabelGuard {
	return &MergeLabelGuard{run: runGh}
}

// NewMergeLabelGuardWithRunner returns the guard over a stubbed gh runner, for tests.
func NewMergeLabelGuardWithRunner(run func(args ...string) (string, error)) *MergeLabelGuard {
	return &MergeLabelGuard{run: run}
}

// Name returns the script this guard replaces.
func (g *MergeLabelGuard) Name() string { return "block-do-not-merge-merge.sh" }

// Applies reports whether this is a Bash call naming a gh pr merge.
func (g *MergeLabelGuard) Applies(input Input) bool {
	if !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.Contains(command, "gh") && strings.Contains(command, "pr") &&
		strings.Contains(command, "merge")
}

// Evaluate blocks a merge on a PR carrying the do-not-merge label.
func (g *MergeLabelGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	// Real invocations only: a heredoc body or quoted string that merely mentions a
	// merge is not a target.
	invocations := mergeLabelTargets(command)
	if len(invocations) == 0 {
		return Decision{}
	}
	if len(invocations) > 1 {
		return DenyDecision(mergeLabelMultiTarget)
	}
	repo, id, ok := g.resolveTarget(invocations[0], command)
	if !ok {
		return DenyDecision(mergeLabelMultiRepo)
	}
	labels := g.labels(repo, id)
	for _, label := range labels {
		if label == "do-not-merge" {
			return DenyDecision(mergeLabelBlocked)
		}
	}
	return Decision{}
}

// mergeLabelTargets returns the distinct gh-pr-merge invocations in a command.
//
// The quote-stripped scan is preferred, because it drops prose that merely mentions
// a merge. When it finds nothing, the command is retried with the quote CHARACTERS
// removed rather than their spans: `gh -R "bborbe/x" pr merge 42` is a real
// invocation whose repo the span-strip would delete, and gh-merge-targets.py
// unquoted the same way.
func mergeLabelTargets(command string) []string {
	if targets := distinctInvocations(scanText(command)); len(targets) > 0 {
		return targets
	}
	return distinctInvocations(unquoteAll(command))
}

// unquoteAll removes quote characters while keeping their contents.
func unquoteAll(command string) string {
	return strings.NewReplacer(`"`, "", `'`, "").Replace(command)
}

// distinctInvocations returns the distinct invocation matches in text.
func distinctInvocations(text string) []string {
	seen := map[string]bool{}
	var targets []string
	for _, match := range mergeLabelInvocation.FindAllString(text, -1) {
		if !seen[match] {
			seen[match] = true
			targets = append(targets, match)
		}
	}
	sort.Strings(targets)
	return targets
}

// resolveTarget reads the repo and PR id out of one invocation, and reports a
// second --repo as a conflict.
func (g *MergeLabelGuard) resolveTarget(invocation string, command string) (string, string, bool) {
	repo := ""
	if match := mergeLabelRepoFlag.FindString(invocation); match != "" {
		repo = mergeLabelRepoStrip.ReplaceAllString(match, "")
	}
	if repo == "" {
		tail := ""
		if index := strings.Index(command, invocation); index >= 0 {
			tail = command[index+len(invocation):]
		}
		repos := distinct(mergeLabelTailRepo.FindAllString(tail, -1), mergeLabelTailStrip)
		if len(repos) > 1 {
			return "", "", false
		}
		if len(repos) == 1 {
			repo = repos[0]
		}
	}
	id := ""
	if match := mergeLabelID.FindStringSubmatch(invocation); match != nil {
		id = match[1]
	}
	if id == "" {
		// A flag between `merge` and the number (`gh pr merge --repo o/r 42`) defeats
		// the anchored match, and an id-less lookup would check the CURRENT branch's
		// PR instead of the one being merged.
		id = trailingNumber(command, invocation)
	}
	return repo, id, true
}

// trailingNumber returns the first bare number after the invocation, up to the next
// command.
func trailingNumber(command string, invocation string) string {
	index := strings.Index(command, invocation)
	if index < 0 {
		return ""
	}
	tail := command[index+len(invocation):]
	if next := strings.IndexAny(tail, ";&|"); next >= 0 {
		tail = tail[:next]
	}
	for _, word := range strings.Fields(tail) {
		if allDigits(word) {
			return word
		}
	}
	return ""
}

// distinct strips each match down to its value and returns the unique values.
func distinct(matches []string, strip *regexp.Regexp) []string {
	seen := map[string]bool{}
	var values []string
	for _, match := range matches {
		value := strings.TrimSpace(strip.ReplaceAllString(match, ""))
		if value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

// labels resolves the target repo and returns the PR's label names.
func (g *MergeLabelGuard) labels(repo string, id string) []string {
	if repo == "" {
		out, err := g.run("repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
		if err != nil {
			return nil
		}
		repo = strings.TrimSpace(out)
	}
	args := []string{"pr", "view"}
	if id != "" {
		args = append(args, id)
	}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	args = append(args, "--json", "labels", "--jq", ".labels[].name")
	out, err := g.run(args...)
	if err != nil {
		return nil
	}
	var labels []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			labels = append(labels, trimmed)
		}
	}
	return labels
}
