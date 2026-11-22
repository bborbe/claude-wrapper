// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "fmt"

// The stable codes a resume failure carries.
//
// The two causes are indistinguishable from the outside: both arrive as a failed
// `spawn_agent(resume=…)`, and before these codes existed both read as an opaque
// client timeout, so a manager could only tell them apart by probing by hand.
// Naming them in the error text lets the caller choose between "the row is gone,
// open a fresh one" and "the resume path itself is broken, fix that first".
const (
	// ResumeCodeRowUnreachable: the session row itself could not be reached or
	// started — the session is gone, or spawning its child failed.
	ResumeCodeRowUnreachable = "resume_row_unreachable"
	// ResumeCodePathBroken: the resume machinery failed before the row could be
	// touched — the configured vault list was unreadable, or a guard subprocess
	// was killed. The row may be perfectly fine.
	ResumeCodePathBroken = "resume_path_broken"
)

// ResumeError is a resume failure carrying a stable code.
type ResumeError struct {
	Code  string
	Cause error
}

func (e *ResumeError) Error() string {
	return fmt.Sprintf("resume failed [code=%s]: %v", e.Code, e.Cause)
}

// Unwrap exposes the cause so errors.Is / errors.As keep working through it.
func (e *ResumeError) Unwrap() error { return e.Cause }

// resumeFailure wraps cause with the given resume code, or returns nil when there
// is no cause, so a caller can wrap a guard's result unconditionally.
func resumeFailure(code string, cause error) error {
	if cause == nil {
		return nil
	}
	return &ResumeError{Code: code, Cause: cause}
}
