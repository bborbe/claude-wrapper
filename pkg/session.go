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

// Question is the permission request a session is parked on.
type Question struct {
	RequestID string         `json:"request_id"`
	ToolName  string         `json:"tool_name"`
	Input     map[string]any `json:"input"`
	Options   []string       `json:"options"`
}

// Session is the REST view of one wrapped claude child.
type Session struct {
	ID          string    `json:"id"`
	State       State     `json:"state"`
	Model       string    `json:"model"`
	ModelSource string    `json:"model_source"`
	Question    *Question `json:"question"`
	Controls    Controls  `json:"controls"`
	Cwd         string    `json:"cwd"`
	StartedAt   time.Time `json:"started_at"`
	ExitCode    *int      `json:"exit_code"`
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
}
