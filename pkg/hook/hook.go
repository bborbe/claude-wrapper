// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package hook evaluates Claude Code hook guards in-process.
//
// One binary replaces the per-call script chain: a Bash tool call used to start
// twenty hook processes and sixty-odd children, nearly all of them jq and grep
// forks. Here each guard is a Go function over the same decoded input, so a
// call starts one process and no children.
//
// The guards are ports, not rewrites. Each keeps the decision its script made,
// and the denial texts other rules quote stay byte-identical.
package hook

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/bborbe/errors"
)

// Event names Claude Code passes in hook_event_name.
const (
	EventPreToolUse         = "PreToolUse"
	EventPostToolUse        = "PostToolUse"
	EventPostToolUseFailure = "PostToolUseFailure"
	EventPermissionRequest  = "PermissionRequest"
	EventPermissionDenied   = "PermissionDenied"
)

// Decision is what a guard returns for one input.
//
// The zero value is a pass-through: the guard has no opinion and the next one
// is consulted. Deny wins over Allow when two guards disagree, because a guard
// that refuses makes a safety claim and a guard that allows only removes a
// prompt.
type Decision struct {
	// Deny refuses the call and prints Reason to the model.
	Deny bool
	// Allow removes the permission prompt for this call.
	Allow bool
	// Reason is shown on a Deny, or recorded as the justification on an Allow.
	Reason string
}

// DenyDecision returns a refusing decision carrying reason.
func DenyDecision(reason string) Decision {
	return Decision{Deny: true, Reason: reason}
}

// AllowDecision returns an allowing decision carrying reason.
func AllowDecision(reason string) Decision {
	return Decision{Allow: true, Reason: reason}
}

// Input is the JSON Claude Code writes to a hook's stdin.
//
// Only the fields the guards read are modelled; unknown fields are ignored so a
// harness that adds one does not break the binary.
type Input struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

// ParseInput decodes the hook payload.
func ParseInput(ctx context.Context, data []byte) (Input, error) {
	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		return Input{}, errors.Wrapf(ctx, err, "unmarshal hook input")
	}
	return input, nil
}

// BashCommand returns the command of a Bash tool call, and whether this input
// is a Bash call at all.
func (i Input) BashCommand() (string, bool) {
	if i.ToolName != "Bash" {
		return "", false
	}
	if len(i.ToolInput) == 0 {
		return "", true
	}
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(i.ToolInput, &payload); err != nil {
		return "", true
	}
	return payload.Command, true
}

// FilePath returns the path argument of an Edit, Write, Read or NotebookEdit
// call, and whether the input carries one.
func (i Input) FilePath() (string, bool) {
	if len(i.ToolInput) == 0 {
		return "", false
	}
	var payload struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(i.ToolInput, &payload); err != nil {
		return "", false
	}
	if payload.FilePath != "" {
		return payload.FilePath, true
	}
	if payload.NotebookPath != "" {
		return payload.NotebookPath, true
	}
	return "", false
}

// IsBash reports whether this is a Bash tool call.
func (i Input) IsBash() bool {
	return i.ToolName == "Bash"
}

// searchArgs returns the pattern and path of a Glob or Grep call.
func (i Input) searchArgs() (pattern string, path string) {
	if len(i.ToolInput) == 0 {
		return "", ""
	}
	var payload struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(i.ToolInput, &payload); err != nil {
		return "", ""
	}
	return payload.Pattern, payload.Path
}

// Guard is one ported hook script.
type Guard interface {
	// Name is the script this guard replaces, for the decision table and logs.
	Name() string
	// Applies reports whether this guard evaluates this input at all.
	Applies(input Input) bool
	// Evaluate returns the guard's decision. A zero Decision means the guard
	// has no opinion.
	Evaluate(input Input) Decision
}

// Registry is the ordered guard chain for one event.
//
// Order matters where two guards cover the same command: the first deny wins,
// which is the order settings.json registered the scripts in.
type Registry struct {
	guards []Guard
}

// NewRegistry returns a registry over guards, in evaluation order.
func NewRegistry(guards ...Guard) *Registry {
	return &Registry{guards: guards}
}

// AllowAuthority marks a guard whose Allow the registry honours. Every other
// guard's Allow is discarded.
type AllowAuthority interface {
	MayAllow() bool
}

// Evaluate runs every applicable guard and returns the combined decision.
//
// A deny short-circuits: nothing after it can make the call safe. An allow is
// honoured only from a guard implementing AllowAuthority, and only when no guard
// in the whole chain denies — every guard still runs after it.
//
// The narrow allow is deliberate. Five review passes found nineteen bypasses and
// every one of them was in an allow path, so the binary went deny-only. That
// pushed every `git -C <root> <read-only>` call — the shape the rules prescribe —
// back to the prompt. An allow is honoured only from a guard that opts in via
// AllowAuthority — today the compound guard, the /tmp write guard and the push
// gate, each a reviewed parser whose allow fires only on a fully parsed, wholly
// allowable call. Every other allow path stays discarded.
func (r *Registry) Evaluate(input Input) Decision {
	var allow Decision
	for _, guard := range r.guards {
		if !guard.Applies(input) {
			continue
		}
		decision := guard.Evaluate(input)
		if decision.Deny {
			return decision
		}
		if decision.Allow && !allow.Allow && mayAllow(guard) {
			allow = decision
		}
	}
	return allow
}

// mayAllow reports whether guard holds allow authority.
func mayAllow(guard Guard) bool {
	authority, ok := guard.(AllowAuthority)
	return ok && authority.MayAllow()
}

// Guards returns the guards in evaluation order.
func (r *Registry) Guards() []Guard {
	return append([]Guard(nil), r.guards...)
}

// Names returns the guard names in evaluation order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.guards))
	for _, guard := range r.guards {
		names = append(names, guard.Name())
	}
	return names
}

// MatchesAny reports whether any of the patterns occurs in the command.
func MatchesAny(command string, patterns ...string) bool {
	for _, pattern := range patterns {
		if strings.Contains(command, pattern) {
			return true
		}
	}
	return false
}
