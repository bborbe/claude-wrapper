// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// SupervisorOption adjusts a supervisor at construction.
//
// Options exist so a dependency the supervisor can work without — the liveness
// probe today — does not widen the constructor for every caller. A supervisor
// built without one keeps its documented degradation rather than refusing to
// build.
type SupervisorOption func(*supervisor)

// WithLiveness gives the supervisor a liveness probe, so a resume of a session
// that is live but not held by this daemon is refused instead of starting a
// second writer on one conversation.
//
// Omitted, the foreign-live check in reserve is skipped: the supervisor then
// knows only its own children, which is exactly the gap this option closes.
func WithLiveness(liveness Liveness) SupervisorOption {
	return func(s *supervisor) {
		s.liveness = liveness
	}
}
