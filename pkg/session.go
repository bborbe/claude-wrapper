// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "time"

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
	Controls    []Control `json:"controls"`
	Cwd         string    `json:"cwd"`
	StartedAt   time.Time `json:"started_at"`
	ExitCode    *int      `json:"exit_code"`
}
