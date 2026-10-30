// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/claude-wrapper/pkg"
)

// Route is one session sub-action: its name, the handler that serves it, and the
// invariants every session route must satisfy.
//
// The table is the single dispatch source. Session looks a row up by action, and
// the parity test drives the same slice, so a route cannot be served without
// being asserted. Keeping the applicability of each invariant as row data rather
// than as prose makes an omission an edit to one list, visible at the point it
// happens rather than at review.
type Route struct {
	// Action is the path segment after /api/1.0/sessions/{id}/.
	Action string

	// Handle serves one request, with the session id already parsed out of the
	// path. Each row closes over the collaborators the daemon wires once.
	Handle func(
		ctx context.Context,
		resp http.ResponseWriter,
		req *http.Request,
		id string,
		requestID string,
	)

	// NeedsSequencer marks a row that cannot run without a sequencer: with a nil
	// one it must answer 503, never panic and never hang.
	NeedsSequencer bool

	// SequencerFree says why a row does not need a sequencer. Every row with
	// NeedsSequencer false carries one, so "false" is a decision rather than an
	// oversight.
	SequencerFree string

	// MultiStep marks a row that can run a multi-step macro, so a run that stops
	// early must be reported through the response's own partial and steps fields
	// rather than as a bare success.
	MultiStep bool
}

// Routes builds the session action table.
//
// It is a function rather than a package variable because each row closes over
// the collaborators the daemon wires once — the supervisor, the sequencer and
// the session locks — which differ per process, not per request.
func Routes(
	supervisor pkg.Supervisor,
	sequencer pkg.Sequencer,
	locks *pkg.SessionLocks,
) []Route {
	return []Route{
		{
			Action: "answer",
			Handle: func(
				ctx context.Context, resp http.ResponseWriter, req *http.Request,
				id string, _ string,
			) {
				handleAnswer(ctx, resp, req, supervisor, id)
			},
			// An answer resolves a request the child already registered. It writes
			// to the child's pending gate, not to a turn, so it starts no macro.
			SequencerFree: "resolves a request the child already registered; runs no turn",
		},
		{
			Action: "message",
			Handle: func(
				ctx context.Context, resp http.ResponseWriter, req *http.Request,
				id string, _ string,
			) {
				handleMessage(ctx, resp, req, supervisor, locks, id)
			},
			// One turn, serialized by the session lock. Starting a macro would need
			// the sequencer's step-by-step reporting, which a single turn has not.
			SequencerFree: "a single turn, serialized by the session lock; starts no macro",
		},
		{
			Action: "question",
			Handle: func(
				ctx context.Context, resp http.ResponseWriter, req *http.Request,
				id string, requestID string,
			) {
				handleQuestion(ctx, resp, req, supervisor, id, requestID)
			},
			// Registers, polls or forgets a pending request. None of the three runs
			// a turn on the child.
			SequencerFree: "registers, polls or forgets a pending request; runs no turn",
		},
		{
			Action: "events",
			Handle: func(
				ctx context.Context, resp http.ResponseWriter, req *http.Request,
				id string, _ string,
			) {
				handleEvents(ctx, resp, req, supervisor, id)
			},
			// The stream only subscribes: it writes to the caller and never to the
			// child, so there is nothing for a sequencer to advance.
			SequencerFree: "subscribes only; writes to the caller, never to the child",
		},
		{
			Action: "commands",
			Handle: func(
				ctx context.Context, resp http.ResponseWriter, req *http.Request,
				id string, _ string,
			) {
				handleCommands(ctx, resp, req, sequencer, id)
			},
			NeedsSequencer: true,
			MultiStep:      true,
		},
		{
			Action: "model",
			Handle: func(
				ctx context.Context, resp http.ResponseWriter, req *http.Request,
				id string, _ string,
			) {
				handleModel(ctx, resp, req, supervisor, sequencer, id)
			},
			NeedsSequencer: true,
			MultiStep:      true,
		},
		{
			Action: "permission-mode",
			Handle: func(
				ctx context.Context, resp http.ResponseWriter, req *http.Request,
				id string, _ string,
			) {
				handlePermissionMode(ctx, resp, req, supervisor, locks, id)
			},
			// The mode travels as the SDK's own control request rather than as a
			// turn, so the session lock serializes it and no macro runs.
			SequencerFree: "control request, serialized by the session lock; runs no macro",
		},
	}
}
