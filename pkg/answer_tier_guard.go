// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// permissionTierRefusal runs claude-supervisor's permission-tier.py for a parked
// permission prompt and returns its refusal text, or "" when the prompt is safe
// for a manager to answer.
//
// ⚠️ THIS IS THE ANSWER PATH. Before this guard, `answer_permission` settled any
// park a manager sent it, whatever its tier, and `closeSettledCard` recorded the
// answer as `answered_by: supervisor`. A manager could therefore settle an
// operator-tier gate and the card read as answered — the SC3 safety-control
// breach. `212be5e` gated only the attention-watch arm; this is the
// claude-wrapper arm.
//
// ⚠️ It CALLS the probe; it does not re-derive the verdict. The tier, the form
// and the allowlist are `permission-tier.py`'s facts. A second implementation
// here would be the two-counters defect this repo refuses everywhere else: the
// day the probe changes, the two disagree with no error on either.
//
// Every branch refuses: an unwired probe, an unconfigured path, a prompt that
// names no tool, a probe that cannot run, a non-zero exit, and unparseable
// output. A guard that failed open would let exactly the operator-tier gate it
// exists to protect be answered by a lower tier.
func (s *RosterToolServer) permissionTierRefusal(
	ctx context.Context,
	question *Question,
) string {
	if question == nil {
		// No park to classify: there is no operator-tier gate here, and the answer
		// path decides (it fails on a missing question). Refusing would strand a
		// legitimate answer to a park this process no longer holds.
		return ""
	}
	if s.Scripts == nil {
		return "the permission-tier probe is not wired, so an operator-tier gate cannot be proven safe to answer"
	}
	if strings.TrimSpace(s.PermissionTierScript) == "" {
		return "the permission-tier probe path is not configured"
	}
	tool := strings.TrimSpace(question.ToolName)
	if tool == "" {
		return "the parked prompt names no tool, so its tier cannot be classified"
	}
	input, refusal := tierProbeInput(question, tool)
	if refusal != "" {
		return refusal
	}
	result, err := s.Scripts.RunScript(
		ctx, openPython, s.PermissionTierScript, "--tool", tool, "--input", input,
	)
	if err != nil {
		return fmt.Sprintf("the permission-tier probe could not run: %v", err)
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stdout)
		if message == "" {
			message = strings.TrimSpace(result.Stderr)
		}
		if message == "" {
			message = "exited without a reason"
		}
		return fmt.Sprintf("the permission-tier probe refused (exit %d): %s", result.ExitCode, message)
	}
	var out struct {
		Tier   string `json:"tier"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
		return "the permission-tier probe returned unparseable output"
	}
	switch out.Tier {
	case "manager", "allowlist":
		return ""
	default:
		// `operator`, and any unknown value, refuse: the allowlist is explicit, so a
		// tier this guard does not recognise is treated as the operator's.
		reason := strings.TrimSpace(out.Reason)
		if reason == "" {
			reason = "tier " + out.Tier
		}
		return fmt.Sprintf(
			"operator-tier gate (%s) — a manager must not settle it; it stays on the attention board for the operator",
			reason,
		)
	}
}

// tierProbeInput builds the --input argument the probe expects: the Bash command
// text for a Bash park, the JSON of the tool input otherwise. An input that
// cannot be encoded refuses rather than passing an empty string the probe would
// classify as a different call.
func tierProbeInput(question *Question, tool string) (string, string) {
	if tool == "Bash" {
		command, ok := question.Input["command"].(string)
		if !ok {
			return "", "the parked Bash prompt carries no command text, so its tier cannot be classified"
		}
		return command, ""
	}
	encoded, err := json.Marshal(question.Input)
	if err != nil {
		return "", "the parked prompt's input could not be encoded for the tier probe"
	}
	return string(encoded), ""
}
