// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// NewStoodDownMarker adapts the candidate join to the one-effect shape a
// `KeepaliveProcedure` returns.
//
// ⚠️ **At most one close per tick, like every other procedure here.** The join
// can return several sessions; closing them all in one tick would fire a backlog
// at once after an outage, which is the load spike the other procedures avoid for
// the same reason. The next tick takes the next one.
//
// The three collaborators are the join's own: the candidate read, the page
// resolution (slug base → recorded `claude_session_id`), and the liveness check.
// ⚠️ `sessionFor` receives the BASE only — the tier is dropped — so the caller's
// closure must try the folders a manager page can live in (goal, topic), the way
// `readOpenPage` already does.
func NewStoodDownMarker(
	candidates func(ctx context.Context) ([]StoodDownCandidate, error),
	sessionFor func(ctx context.Context, base string) (string, error),
	live func(ctx context.Context, sessionID string) (bool, error),
) func(ctx context.Context) (string, bool, error) {
	return func(ctx context.Context) (string, bool, error) {
		if candidates == nil || sessionFor == nil || live == nil {
			return "", false, errors.New(ctx, "stood-down marker needs candidates, a page read and a liveness check")
		}
		found, err := candidates(ctx)
		if err != nil {
			return "", false, errors.Wrap(ctx, err, "read stood-down candidates failed")
		}
		toClose, err := StoodDownSessionsToClose(ctx, found, sessionFor, live)
		if err != nil {
			return "", false, err
		}
		if len(toClose) == 0 {
			return "", false, nil
		}
		return toClose[0], true, nil
	}
}
