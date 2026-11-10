// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"
)

// fakeDurable supplies the durable half of the Supervisor interface to a test
// double that does not model it.
//
// Embedded rather than re-implemented per double: Adopt, Drain and SubscribeFrom
// describe a daemon's relationship to a restart, and a double that only exercises
// a sequencer or a roster has nothing to say about one. They panic rather than
// returning a zero value, because a zero value here reads as "nothing was
// adopted" or "nothing to drain" — a test double silently answering a question it
// was never built to answer.
type fakeDurable struct{}

func (fakeDurable) Adopt(context.Context) (int, error) { return 0, nil }

func (fakeDurable) Drain(context.Context, time.Duration) DrainReport { return DrainReport{} }

func (fakeDurable) EnforceStartupDeadline(context.Context) int { return 0 }

func (fakeDurable) SubscribeFrom(
	context.Context, string, int,
) ([]Event, int, <-chan Event, func(), error) {
	panic("SubscribeFrom is not modelled by this test double")
}
