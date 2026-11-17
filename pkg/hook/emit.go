// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"context"
	"encoding/json"

	"github.com/bborbe/errors"
)

// preToolUseOutput is the envelope Claude Code reads from a PreToolUse hook.
//
// The decision fields are omitted for a pass-through, which is the exact JSON
// the scripts emitted: {"hookSpecificOutput":{"hookEventName":"PreToolUse"}}.
type preToolUseOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// EncodeDecision renders the decision as the JSON Claude Code expects on stdout.
//
// A pass-through carries only the event name. An allow carries the reason so the
// transcript records why no prompt appeared; a deny carries the text the model
// reads.
func EncodeDecision(ctx context.Context, event string, decision Decision) ([]byte, error) {
	output := preToolUseOutput{HookSpecificOutput: hookSpecificOutput{HookEventName: event}}
	switch {
	case decision.Deny:
		output.HookSpecificOutput.PermissionDecision = "deny"
		output.HookSpecificOutput.PermissionDecisionReason = decision.Reason
	case decision.Allow:
		output.HookSpecificOutput.PermissionDecision = "allow"
		output.HookSpecificOutput.PermissionDecisionReason = decision.Reason
	}
	data, err := json.Marshal(output)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal hook decision")
	}
	return data, nil
}
