// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	stderrors "errors"
	"time"
)

// State is a session's liveness verdict.
//
// It is derived from the child's own event stream, never from the age of its
// last event. A timestamp-derived state reads a session that has been
// generating for forty minutes as idle, because the last completed assistant
// message is forty minutes old — the failure this type exists to prevent.
type State string

const (
	// StateStarting is a child that has spawned but not yet produced an init event.
	StateStarting State = "starting"
	// StateGenerating is a child with an assistant turn open.
	StateGenerating State = "generating"
	// StateIdle is a child whose last turn has closed and which awaits input.
	StateIdle State = "idle"
	// StateExited is a child that has left. It accepts no control.
	StateExited State = "exited"
)

// AvailableStates is every legal State, for validation and iteration.
var AvailableStates = States{
	StateStarting,
	StateGenerating,
	StateIdle,
	StateExited,
}

// States is a collection of State values.
type States []State

// Contains reports whether the collection holds the given state.
func (s States) Contains(state State) bool {
	for _, candidate := range s {
		if candidate == state {
			return true
		}
	}
	return false
}

// Validate rejects a state outside AvailableStates.
func (s State) Validate() error {
	if !AvailableStates.Contains(s) {
		return stderrors.New("invalid state " + string(s))
	}
	return nil
}

// Control is an operation a session supports.
//
// A wrapper that reports every session as answerable invites callers to attempt
// an answer that cannot land, so the supported set is declared per session.
type Control string

const (
	// ControlAnswer means the caller may answer a pending permission request.
	ControlAnswer Control = "answer"
	// ControlMessage means the caller may send a further user turn.
	ControlMessage Control = "message"
)

// AvailableControls is every legal Control, for validation and iteration.
var AvailableControls = Controls{
	ControlAnswer,
	ControlMessage,
}

// Controls is a collection of Control values.
type Controls []Control

// Contains reports whether the collection holds the given control.
func (c Controls) Contains(control Control) bool {
	for _, candidate := range c {
		if candidate == control {
			return true
		}
	}
	return false
}

// Validate rejects a control outside AvailableControls.
func (c Control) Validate() error {
	if !AvailableControls.Contains(c) {
		return stderrors.New("invalid control " + string(c))
	}
	return nil
}

// AnswerKind discriminates the two shapes a child can be parked on.
//
// They are not interchangeable: a permission request takes allow or deny, while
// an AskUserQuestion takes a selection among the options it carries. Answering
// one with the other's shape is refused rather than coerced, because a coerced
// answer is recorded and never delivered — the failure this model exists to
// remove.
type AnswerKind string

const (
	// AnswerKindPermission is a tool call the gate refused.
	AnswerKindPermission AnswerKind = "permission"
	// AnswerKindQuestion is an AskUserQuestion awaiting a selection.
	AnswerKindQuestion AnswerKind = "question"
)

// AvailableAnswerKinds is every legal AnswerKind, for validation and iteration.
var AvailableAnswerKinds = AnswerKinds{
	AnswerKindPermission,
	AnswerKindQuestion,
}

// AnswerKinds is a collection of AnswerKind values.
type AnswerKinds []AnswerKind

// Contains reports whether the collection holds the given kind.
func (k AnswerKinds) Contains(kind AnswerKind) bool {
	for _, candidate := range k {
		if candidate == kind {
			return true
		}
	}
	return false
}

// Validate rejects a kind outside AvailableAnswerKinds.
func (k AnswerKind) Validate() error {
	if !AvailableAnswerKinds.Contains(k) {
		return stderrors.New("invalid answer kind " + string(k))
	}
	return nil
}

// QuestionOption is one labelled choice an AskUserQuestion offers.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// QuestionPrompt is one question inside an AskUserQuestion call.
//
// A single call can carry several — three were observed on 2026-10-29 — so the
// prompts are modelled as a list rather than flattened into one.
type QuestionPrompt struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multiSelect"`
}

// Question is the request a session is parked on.
//
// Kind selects which payload is populated: KindPermission fills ToolName, Input
// and ToolUseID, KindQuestion fills Questions. It replaces the earlier model,
// whose entire payload was an allow/deny string and which therefore had nowhere
// to put "option 2 of 4".
type Question struct {
	RequestID string           `json:"request_id"`
	Kind      AnswerKind       `json:"kind"`
	ToolName  string           `json:"tool_name"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	Input     map[string]any   `json:"input,omitempty"`
	Questions []QuestionPrompt `json:"questions,omitempty"`
	// RequestedAt is when this process registered the park.
	//
	// It is the join a board answer's freshness is measured against: the park is
	// joined to its card by producer, and one producer answers many cards over its
	// life, so an answer from an earlier gate must not settle this one.
	RequestedAt time.Time `json:"requested_at"`
}

// Session is the REST view of one wrapped claude child.
type Session struct {
	ID          string `json:"id"`
	State       State  `json:"state"`
	Model       string `json:"model"`
	ModelSource string `json:"model_source"`
	// PermissionMode is the mode the child reports for itself in its own
	// system/init event.
	//
	// It is the child's statement about its own configuration, never a value
	// this process remembers from spawn: a mode changed on a running child
	// shows up here on the child's next init, which is what makes it evidence
	// rather than bookkeeping.
	PermissionMode string    `json:"permission_mode"`
	Question       *Question `json:"question"`
	Controls       Controls  `json:"controls"`
	Cwd            string    `json:"cwd"`
	// Mode is "headless" or "interactive", resolved at spawn. It is the flag a
	// board answer consults before releasing a permission gate: only a headless
	// park has no pane of its own, so only its gate needs the board.
	Mode string `json:"mode"`
	// Role is the resolved role the spawn ran under, recorded so the fleet caps
	// can tell a manager from a worker without re-reading the request.
	Role      string    `json:"role,omitempty"`
	StartedAt time.Time `json:"started_at"`
	ExitCode  *int      `json:"exit_code"`
	// LastEventAgeSeconds is how long the child's stream has been silent.
	//
	// It is reported so a caller can tell a genuinely idle child from one that
	// is busy inside a tool call: the latter is silent too, but its state stays
	// generating. State is never derived from this value — that inversion is
	// the defect this field exists to make visible.
	LastEventAgeSeconds *int `json:"last_event_age_seconds"`
	// PendingTurns counts turns written to the child that have not closed yet.
	//
	// State alone cannot answer "is a turn in flight": it flips to generating
	// only when the child's own message_start arrives, so between our write and
	// that event the child still reads idle while a turn is on its way. A
	// caller that must not write mid-turn reads this instead.
	PendingTurns int `json:"pending_turns"`
	// Interrupted names the reason a drain could not reach this session's turn
	// boundary before its bound expired.
	//
	// It says nothing about the child, which keeps running: it is the drain's own
	// verdict, recorded so a restart that cut a turn short cannot afterwards read
	// the same as one that landed between turns.
	Interrupted string `json:"interrupted,omitempty"`
	// Stall names where a child that never initialised was waiting when it was
	// killed, so the next startup-deadline kill diagnoses itself from its exit
	// record instead of a post-mortem.
	//
	// It is nil for a session that emitted its init event. It is populated only
	// from events this process saw: a replayed transcript from an earlier run
	// never fills it, so an empty LastEvent on a resume is the finding that the
	// resumed child emitted nothing — not a lost record.
	Stall *Stall `json:"stall,omitempty"`
}

// Stall is the stall point of a child that rested in StateStarting until the
// startup deadline killed it.
//
// It answers the question the deadline's log line cannot: the child emitted no
// init event, but which phase was it in? A pending hook names the hook. An
// empty pending list with a last event names the phase that followed the hooks,
// which is where MCP servers are connected — init is only emitted once every
// server has been tried, and the init event itself carries the per-server
// status that proves it.
type Stall struct {
	// LastEvent is the type/subtype of the last event this process observed,
	// or empty when the child emitted nothing at all in this run.
	LastEvent string `json:"last_event,omitempty"`
	// PendingHooks are the hooks that started and never returned, in the order
	// they started.
	PendingHooks []string `json:"pending_hooks,omitempty"`
	// Phase is where the child was waiting: "hook" when a hook is pending,
	// "no-event" when it emitted nothing, and "awaiting-init" otherwise.
	//
	// Derived, not stored: the stored value on the child is empty and the
	// snapshot fills it. The exit recorder recomputes it from the same fields,
	// so the two readers cannot disagree.
	Phase string `json:"phase,omitempty"`
}

// ModelChange is what a model change reports back.
//
// ServedModel is read from the assistant event of the turn the change ran, not
// from the caller's argument and not from init: a change that echoed its own
// request would report itself rather than the child, which is the recorded-but-
// not-delivered defect this field exists to make impossible.
type ModelChange struct {
	// Requested is the model name the caller asked for.
	Requested string `json:"requested"`
	// PreviousModel is what served the session's last real turn before the
	// change, so a caller can see that something actually moved.
	PreviousModel string `json:"previous_model"`
	// ServedModel is what served the first turn after the change.
	ServedModel string `json:"served_model"`
	// Changed reports whether ServedModel differs from PreviousModel.
	//
	// A name the backend does not serve verbatim resolves to whatever it does
	// serve, so a caller that asks for an alias sees the served name here and
	// Changed=true with a different string — which is the truth, not a failure.
	Changed bool `json:"changed"`
	// Partial reports that the change command was written but the probe turn
	// that reads it back failed, so ServedModel is the last model known to have
	// answered rather than one observed after the switch.
	//
	// Without it an aborted run has to be reported as a bare error, which reads
	// as "nothing happened" for a change that was in fact applied.
	Partial bool `json:"partial,omitempty"`
	// Steps carries the run's own per-step report when Partial is set, so a
	// caller can see which step stopped.
	Steps []StepResult `json:"steps,omitempty"`
}
