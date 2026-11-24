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

// PermissionTierGuard classifies a parked prompt's permission tier through
// claude-supervisor's permission-tier.py.
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
// ⚠️ It is the ONE guard behind every answer surface: `answer_permission` (the
// roster MCP tool), `answer_pending` (the MCP tool `MCPToolServer` serves) and
// the REST `answer` route. It is a plain value rather than a method so the two
// surfaces that hold no *Question of their own — `answer_pending` and the REST
// route — can reach the same classification instead of re-deriving it or
// skipping it. Before this extraction the guard was reachable only from
// `*RosterToolServer`, so those two paths settled an operator-tier park with no
// check at all.
//
// Every branch refuses: an unwired probe, an unconfigured path, a prompt that
// names no tool, a probe that cannot run, a non-zero exit, and unparseable
// output. A guard that failed open would let exactly the operator-tier gate it
// exists to protect be answered by a lower tier.
type PermissionTierGuard struct {
	// Scripts runs the probe. Nil refuses, exactly as it does on
	// *RosterToolServer: a daemon started without the runner must not answer an
	// unclassifiable gate.
	Scripts ScriptRunner
	// PermissionTierScript is claude-supervisor's permission-tier.py
	// (DefaultPermissionTierScript).
	PermissionTierScript string
}

// RefuseSettledAnswer returns a non-empty refusal when the answer a caller is
// about to post must not settle the park it names, or "" when the answer is
// safe to post.
//
// It is the shared settle every answer surface funnels through, so the three
// cannot drift in what they gate:
//
//   - no park at all passes: there is no gate to classify, and the answer path
//     decides (it fails on a missing request). Refusing there would strand a
//     legitimate answer to a park this process no longer holds;
//   - a park whose request id is not the one being answered refuses — a caller
//     that sends one park's id with another's request id would have a
//     manager-tier Question classified while an operator-tier park was answered;
//   - a non-permission park (an AskUserQuestion) passes: the tier guard
//     classifies permission gates, and gating a selection here would refuse an
//     answer no arm has ever been tier-checked for;
//   - a permission park is classified by guard, and an operator-tier verdict
//     refuses.
func RefuseSettledAnswer(
	ctx context.Context,
	guard PermissionTierGuard,
	question *Question,
	requestID string,
	sessionID string,
) string {
	if question == nil {
		// No park to classify: there is no operator-tier gate here, and the answer
		// path decides (it fails on a missing request). Refusing would strand a
		// legitimate answer to a park this process no longer holds.
		return ""
	}
	if question.RequestID != requestID {
		return fmt.Sprintf(
			"request_id %q does not match the parked prompt on session %q, so its tier cannot be verified",
			requestID, sessionID,
		)
	}
	kind := question.Kind
	if kind == "" {
		// An empty kind is a pre-`kind` registration, which only ever carried
		// permission parks — `answerKindFor` defaults to permission for the same
		// reason.
		kind = AnswerKindPermission
	}
	if kind != AnswerKindPermission {
		return ""
	}
	return guard.Refusal(ctx, question)
}

// Refusal runs the probe for a parked permission prompt and returns its refusal
// text, or "" when the prompt is safe for a manager to answer.
func (g PermissionTierGuard) Refusal(
	ctx context.Context,
	question *Question,
) string {
	if question == nil {
		// No park to classify: there is no operator-tier gate here, and the answer
		// path decides (it fails on a missing question). Refusing would strand a
		// legitimate answer to a park this process no longer holds.
		return ""
	}
	if g.Scripts == nil {
		return "the permission-tier probe is not wired, so an operator-tier gate cannot be proven safe to answer"
	}
	if strings.TrimSpace(g.PermissionTierScript) == "" {
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
	// The input goes on stdin, never argv: a non-Bash park carries file content that
	// may be a secret, and argv is visible in `ps`. The probe reads `--input -`.
	result, err := g.Scripts.RunScriptWithStdin(
		ctx, input, openPython, g.PermissionTierScript, "--tool", tool, "--input", "-",
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

// tierGuard is the guard this server's answer_permission path applies.
//
// It exists so the roster server keeps ONE source for its two probe fields
// (Scripts, PermissionTierScript) while the classification itself lives in
// PermissionTierGuard, which the surfaces that hold no *RosterToolServer can
// also reach.
func (s *RosterToolServer) tierGuard() PermissionTierGuard {
	return PermissionTierGuard{
		Scripts:              s.Scripts,
		PermissionTierScript: s.PermissionTierScript,
	}
}

// permissionTierRefusal is the answer_permission call site's entry point.
func (s *RosterToolServer) permissionTierRefusal(
	ctx context.Context,
	question *Question,
) string {
	return s.tierGuard().Refusal(ctx, question)
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
