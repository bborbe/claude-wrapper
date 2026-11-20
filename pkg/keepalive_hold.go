// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// HoldState is the three-state answer to "is this session held".
//
// ⚠️ **It is a tri-state, not a bool.** SC2 requires the tick to report `skipped
// reason=hold` for a HELD session and `skipped reason=hold-unknown` for one whose
// heldness could not be read; a bool would collapse the two and fail SC2's second
// fixture. Both states skip the act, but the reason distinguishes them so an
// unreadable hold store is visible rather than reported as a clean hold.
type HoldState int

const (
	// HoldNotHeld — the session may be acted on.
	HoldNotHeld HoldState = iota
	// HoldHeld — the session is held; skip with `reason=hold`.
	HoldHeld
	// HoldUnknown — heldness could not be read; skip with `reason=hold-unknown`.
	HoldUnknown
)

// KeepaliveHold reports whether a session is held, and why it could not be told.
type KeepaliveHold func(ctx context.Context, sessionID string) (HoldState, error)

// NewKeepaliveHold builds the hold check over `scripts/session-holds.py is-held
// <session>`.
//
// ⚠️ **Three states, not two.** The script exits `0` when held, `1` when not held,
// and `2` when heldness is UNKNOWN (the pane list was unreadable). Exit 2 is
// HoldUnknown — the tick skips, but reports `reason=hold-unknown`, not `hold`, so
// an unreadable store is distinguishable from a real hold. The source is
// `~/.claude/state/session-holds.json` (override `SUPERVISOR_SESSION_HOLDS`), read
// by that script.
//
// The fixed order at each call site is hold → claim → act: a held (or unknown)
// session is skipped before the claim is taken, so the claim is not spent on a
// session the tick must leave alone.
func NewKeepaliveHold(run KeepaliveScriptRunner) KeepaliveHold {
	return func(ctx context.Context, sessionID string) (HoldState, error) {
		if run == nil {
			return HoldNotHeld, errors.New(ctx, "keepalive hold has no script runner")
		}
		if sessionID == "" {
			return HoldNotHeld, errors.New(ctx, "keepalive hold needs a session id")
		}
		_, code, err := run(ctx, "is-held", sessionID)
		if err != nil {
			return HoldNotHeld, errors.Wrapf(ctx, err, "hold check %s failed", sessionID)
		}
		switch code {
		case 0:
			return HoldHeld, nil
		case 1:
			return HoldNotHeld, nil
		case 2:
			return HoldUnknown, nil
		default:
			return HoldNotHeld, errors.Errorf(ctx, "hold check %s exited %d, want 0/1/2", sessionID, code)
		}
	}
}
