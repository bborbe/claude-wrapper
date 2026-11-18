// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"encoding/json"
	"strings"
)

// The verdicts of allow-gh-pr-merge.py, kept byte-identical.
const (
	mergeAllowAdmin = "gh pr merge --admin bypasses branch protection; this hook refuses it " +
		"for every repo, including owners no deny rule covers."
	mergeAllowReasonPrefix = "bot-APPROVED on head, mergeable, every check completed-passing " +
		"for: "
)

// mergeAllowDeclinedRepos are the operator-declined standing grants. A declined repo
// produces no decision, so the ordinary prompt stands.
var mergeAllowDeclinedRepos = map[string]bool{
	"bborbe/vault-cli":         true,
	"bborbe/claude-supervisor": true,
}

// mergeAllowPassingConclusions are the check conclusions that count as passing.
var mergeAllowPassingConclusions = map[string]bool{
	"SUCCESS": true, "NEUTRAL": true, "SKIPPED": true,
}

// mergeTarget is one parsed `gh pr merge` invocation. The empty string means the
// target could not be read, which is "cannot verify", never "safe".
type mergeTarget struct {
	Repo string
	ID   string
}

// mergeState is the subset of `gh pr view` this guard reads.
type mergeState struct {
	ReviewDecision    string `json:"reviewDecision"`
	Mergeable         string `json:"mergeable"`
	StatusCheckRollup []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

// MergeAllowGuard replaces allow-gh-pr-merge.py.
//
// It allows `gh pr merge` only when the command is non-admin and every target is
// positively green: APPROVED on head, MERGEABLE, and every check completed-passing.
// Anything it cannot verify produces no decision, so the ordinary prompt stands.
type MergeAllowGuard struct {
	run ghRunner
}

// NewMergeAllowGuard returns the green-merge guard.
func NewMergeAllowGuard() *MergeAllowGuard {
	return &MergeAllowGuard{run: runGh}
}

// NewMergeAllowGuardWithRunner returns the guard over a stubbed gh runner, for tests.
func NewMergeAllowGuardWithRunner(run func(args ...string) (string, error)) *MergeAllowGuard {
	return &MergeAllowGuard{run: run}
}

// Name returns the script this guard replaces.
func (g *MergeAllowGuard) Name() string { return "allow-gh-pr-merge.py" }

// Applies reports whether this is a Bash call naming a gh pr merge.
func (g *MergeAllowGuard) Applies(input Input) bool {
	if !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.Contains(command, "merge") && strings.Contains(command, "gh")
}

// Evaluate allows a positively green, non-admin merge and refuses --admin.
func (g *MergeAllowGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	// The one path that bypasses branch protection. Refused on the text
	// deliberately: a false deny costs a prompt, a false allow costs a ruleset.
	if strings.Contains(command, "--admin") {
		return DenyDecision(mergeAllowAdmin)
	}
	targets := mergeAllowTargets(scanText(command))
	if len(targets) == 0 {
		return Decision{}
	}
	for _, target := range targets {
		if target.Repo == "" || target.ID == "" {
			return Decision{}
		}
		if mergeAllowDeclinedRepos[target.Repo] {
			return Decision{}
		}
		state, ok := g.prState(target)
		if !ok || !isGreenMerge(state) {
			return Decision{}
		}
	}
	return AllowDecision(mergeAllowReasonPrefix + mergeAllowTargetList(targets))
}

// mergeAllowTargets parses the distinct invocations into repo/id pairs. An absent
// repo or number is the empty string, which the caller treats as unverifiable.
func mergeAllowTargets(scan string) []mergeTarget {
	seen := map[mergeTarget]bool{}
	var targets []mergeTarget
	for _, loc := range mergeLabelInvocation.FindAllStringIndex(scan, -1) {
		invocation := scan[loc[0]:loc[1]]
		target := mergeTarget{}
		if match := mergeLabelRepoFlag.FindString(invocation); match != "" {
			target.Repo = mergeLabelRepoStrip.ReplaceAllString(match, "")
		} else {
			target.Repo = trailingRepo(scan[loc[1]:])
		}
		if match := mergeLabelID.FindStringSubmatch(invocation); match != nil {
			target.ID = match[1]
		}
		if !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	return targets
}

// trailingRepo reads the `--repo o/r` that follows an invocation, up to the next
// one, which is the `... merge N --repo o/r` spelling.
func trailingRepo(rest string) string {
	if next := strings.Index(rest, "gh "); next >= 0 {
		rest = rest[:next]
	}
	repos := distinct(mergeLabelTailRepo.FindAllString(rest, -1), mergeLabelTailStrip)
	if len(repos) == 1 {
		return repos[0]
	}
	return ""
}

// mergeAllowTargetList renders the targets for the allow reason.
func mergeAllowTargetList(targets []mergeTarget) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, target.Repo+"#"+target.ID)
	}
	return strings.Join(parts, ", ")
}

// prState reads the PR fields this guard needs, and reports whether it could.
func (g *MergeAllowGuard) prState(target mergeTarget) (mergeState, bool) {
	out, err := g.run(
		"pr", "view", target.ID, "-R", target.Repo,
		"--json", "reviewDecision,mergeable,statusCheckRollup",
	)
	if err != nil {
		return mergeState{}, false
	}
	var state mergeState
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return mergeState{}, false
	}
	return state, true
}

// isGreenMerge reports APPROVED on head, positively MERGEABLE, every check
// completed-passing. UNKNOWN is what GitHub serves while it is still computing, and
// a merge attempted in that window should be asked about, not waved through.
func isGreenMerge(state mergeState) bool {
	if state.ReviewDecision != "APPROVED" || state.Mergeable != "MERGEABLE" {
		return false
	}
	for _, check := range state.StatusCheckRollup {
		if check.Status != "COMPLETED" || !mergeAllowPassingConclusions[check.Conclusion] {
			return false
		}
	}
	return true
}
