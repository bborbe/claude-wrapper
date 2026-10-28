// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// Sentinel errors the handlers map onto HTTP status codes.
var (
	// ErrSessionNotFound is returned for an id the supervisor does not hold.
	ErrSessionNotFound = errors.New(context.Background(), "session not found")
	// ErrSessionExited is returned when the child has already left.
	ErrSessionExited = errors.New(context.Background(), "session exited")
	// ErrAnswerUnsupported is returned when a session declares no answer control.
	ErrAnswerUnsupported = errors.New(context.Background(), "answer not supported")
)
