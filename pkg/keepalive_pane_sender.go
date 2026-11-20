// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// ErrKeepaliveNoPane is returned by a KeepalivePaneSender when the session has no
// reachable pane. The procedure maps it to `skipped reason=no-pane` rather than a
// resume that typed into nothing.
var ErrKeepaliveNoPane = errors.New(context.Background(), "session has no pane")

// NewKeepalivePaneSender builds the RESTART seam: it resolves a session to its
// WezTerm pane and types the restart command into it, ready-gated.
//
// ⚠️ **The pid→pane join is done ONCE per tick, by the caller**, not here — the
// caller runs `PanesForPIDs` over every manager pid once and passes a `paneFor`
// that reads that pre-computed map. A per-session join here would shell out to
// `ps` and `wezterm cli list` once per manager, which is the cost the once-per-tick
// rule exists to avoid.
//
// ⚠️ **The send is `SendWhenReady`, not raw `SendText`** — the same call
// `run_slash_command.go` makes — because a restart typed before the pane's composer
// is ready is swallowed, and a swallowed restart reads as a delivered one.
//
// The pid comes from the REGISTRY entry for the session, not the liveness row: a
// live tab manager's registry entry carries its `pid`, which the join keys on. A
// session with no registry entry, or whose pid has no pane, yields `paneFor`'s
// `false` → `ErrKeepaliveNoPane`.
func NewKeepalivePaneSender(
	tabs TabClient,
	paneFor func(sessionID string) (string, bool),
	now libtime.CurrentDateTimeGetter,
) KeepalivePaneSender {
	return func(ctx context.Context, sessionID string, text string) error {
		if tabs == nil {
			return errors.New(ctx, "pane sender has no tab client")
		}
		if paneFor == nil {
			return errors.New(ctx, "pane sender has no pane resolver")
		}
		if now == nil {
			return errors.New(ctx, "pane sender has no clock")
		}
		paneID, ok := paneFor(sessionID)
		if !ok || paneID == "" {
			return ErrKeepaliveNoPane
		}
		if err := SendWhenReady(ctx, tabs, paneID, text, now); err != nil {
			return errors.Wrapf(ctx, err, "send restart to pane %s failed", paneID)
		}
		return nil
	}
}
