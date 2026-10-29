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
	// ErrInvalidSpawnOptions is returned when the caller's spawn options cannot
	// be expressed as a child command, so the request is refused before any child
	// or temp file exists.
	ErrInvalidSpawnOptions = stderrors.New("invalid spawn options")
)
