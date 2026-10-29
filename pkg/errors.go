// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import stderrors "errors"

// Sentinel errors the handlers map onto HTTP status codes.
//
// stdlib errors.New rather than libborbe's context-taking constructor: these
// are package-level values created once at init, where there is no request to
// take a context from. Call sites wrap them with libborbe's errors.Wrapf and
// the real ctx, which is where the context belongs.
var (
	// ErrSessionNotFound is returned for an id the supervisor does not hold.
	ErrSessionNotFound = stderrors.New("session not found")
	// ErrSessionExited is returned when the child has already left.
	ErrSessionExited = stderrors.New("session exited")
	// ErrAnswerUnsupported is returned when a session declares no answer control.
	ErrAnswerUnsupported = stderrors.New("answer not supported")
	// ErrAnswerShapeMismatch is returned when an answer's shape does not match
	// the pending request it is aimed at — a selection aimed at a permission
	// request, or a behavior aimed at a question. It is deliberately distinct
	// from ErrAnswerUnsupported, which stays the unknown-or-already-answered
	// case, so a caller can tell "wrong answer for this request" (422) from
	// "no such request" (409).
	ErrAnswerShapeMismatch = stderrors.New("answer shape mismatch")
	// ErrInvalidSpawnOptions is returned when the caller's spawn options cannot
	// be expressed as a child command, so the request is refused before any child
	// or temp file exists.
	ErrInvalidSpawnOptions = stderrors.New("invalid spawn options")
	// ErrUnknownMacro is returned for a macro name the wrapper does not define.
	ErrUnknownMacro = stderrors.New("unknown macro")
	// ErrConfirmationRequired is returned when a macro contains a
	// production-touching step and the caller supplied no confirmation.
	ErrConfirmationRequired = stderrors.New("confirmation required")
	// ErrSessionNotIdle is returned when a sequence is asked to start on a
	// child that is mid-turn, where the in-flight turn's close would be read as
	// the first step's.
	ErrSessionNotIdle = stderrors.New("session not idle")
)
