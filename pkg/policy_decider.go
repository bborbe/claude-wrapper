// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"

	libtime "github.com/bborbe/time"
)

// PolicyContext is what one child's policy evaluation needs.
//
// It travels on the child rather than being re-resolved per park: the overlay a
// worker was SPAWNED under is the policy it must be judged by, and re-reading
// the files on every park would let an edit to the operator's overlay change the
// rules under a running worker mid-turn.
type PolicyContext struct {
	// Label is the agent's human name, carried into the permissions record so a
	// log line is attributable without a second lookup.
	Label string
	// Cwd is the directory the child runs in — the boundary `match: "cwd"`
	// compares against.
	Cwd string
	// PolicyPath is the per-spawn overlay path, or empty. It is recorded verbatim
	// as the record's `policy` field, the same value the node server writes.
	PolicyPath string
	// Rules is the resolved ordered rule set.
	Rules []Rule
}

// IsPermissionQuestion reports whether a park is a tool-permission prompt the
// policy can judge.
//
// An AskUserQuestion park is a selection among options, not an allow/deny — the
// policy has no vocabulary for it and must never answer it.
func IsPermissionQuestion(question Question) bool {
	if question.RequestID == "" || question.ToolName == "" {
		return false
	}
	if question.ToolName == askUserQuestionToolName {
		return false
	}
	if question.Kind != "" && question.Kind != AnswerKindPermission {
		return false
	}
	return true
}

// PermissionDecider applies the approval policy to a park and records what it
// decided.
//
// It is the wrapper's replacement for the node server's 2-second poll: the park
// is wrapper STATE, and this process holds it, so the decision needs no HTTP hop
// and adds no polling latency.
type PermissionDecider struct {
	Log PermissionLog
	Now libtime.CurrentDateTimeGetter
	// Via is the record's `via` field. It defaults to "wrapper", the node
	// server's own value for a decision taken on this path.
	Via string
}

// Decide evaluates one park and writes its permissions record.
//
// The record is written for EVERY verdict, including an escalation: a park left
// for the manager must be visible in the log, or the log reads as "the policy
// saw nothing" for the requests it deliberately declined.
func (d *PermissionDecider) Decide(
	ctx context.Context,
	agentID string,
	policy PolicyContext,
	question Question,
) Verdict {
	verdict := Decide(policy.Rules, question.ToolName, question.Input, policy.Cwd)
	record := PermissionRecord{
		Agent:       agentID,
		Label:       policy.Label,
		RequestID:   question.RequestID,
		Tool:        question.ToolName,
		Key:         verdict.Key,
		MatchedRule: verdict.MatchedRule(),
		Via:         d.via(),
	}
	if policy.PolicyPath != "" {
		path := policy.PolicyPath
		record.Policy = &path
	}
	if verdict.Action == ActionAllow || verdict.Action == ActionDeny {
		decision := string(verdict.Action)
		record.Decision = &decision
		record.DecidedBy = DecidedByPolicy
	} else {
		record.DecidedBy = DecidedByEscalated
	}
	d.write(ctx, record)
	return verdict
}

// via is the record's `via` value, defaulted.
func (d *PermissionDecider) via() string {
	if d.Via != "" {
		return d.Via
	}
	return "wrapper"
}

// write appends one record, logging rather than returning a failure.
//
// A record that cannot be written must never withhold the decision: the park is
// the worker's critical path and the log is an audit trail behind it.
func (d *PermissionDecider) write(ctx context.Context, record PermissionRecord) {
	if d.Log == nil {
		return
	}
	if d.Now != nil && record.TS == "" {
		record.TS = d.Now.Now().Time().Format("2006-01-02T15:04:05.000Z07:00")
	}
	if err := d.Log.Write(ctx, record); err != nil {
		log.Printf("WARNING: permissions record for %s could not be written: %v", record.RequestID, err)
	}
}

// RecordSettled writes the record for a park settled by something other than the
// policy — a manager answer, a board answer, or the timeout.
//
// One builder for all three keeps `decided_by` the only field that differs, so
// the log stays comparable and a reader can filter by actor.
func (d *PermissionDecider) RecordSettled(
	ctx context.Context,
	agentID string,
	policy PolicyContext,
	question Question,
	decision string,
	decidedBy string,
	message string,
	latencyMS *int,
) {
	record := PermissionRecord{
		Agent:       agentID,
		Label:       policy.Label,
		RequestID:   question.RequestID,
		Tool:        question.ToolName,
		Key:         InputKey(question.Input),
		MatchedRule: nil,
		Via:         d.via(),
		Decision:    &decision,
		DecidedBy:   decidedBy,
		Message:     message,
		LatencyMS:   latencyMS,
	}
	if policy.PolicyPath != "" {
		path := policy.PolicyPath
		record.Policy = &path
	}
	d.write(ctx, record)
}
