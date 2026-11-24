// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// anchorKinds are the three page kinds a session can be anchored to, in probe
// order. It matches the kinds `kindDirs` and the vault-api's plural paths serve.
var anchorKinds = []string{"task", "goal", "topic"}

// terminalAnchorStatuses are the statuses whose page still names a session but
// offers it no next subtask. A session anchored only to one of these gets the
// same unactionable wake as an unanchored one.
var terminalAnchorStatuses = map[string]bool{
	"completed": true,
	"aborted":   true,
}

// AnchorReader answers which sessions a page anchors.
//
// The wake text says "take the next unchecked subtask on your anchored task", so
// a session nothing anchors cannot act on it. The port answers for the whole fleet
// in one read rather than per session: the pusher asks once per tick, and a
// per-session port would walk the vault three times for every live session.
type AnchorReader interface {
	// AnchoredSessions returns every session id some non-terminal task, goal or
	// topic records as its `claude_session_id`.
	AnchoredSessions(ctx context.Context) (map[string]bool, error)
}

// VaultAPIAnchorReader answers it from the local vault-api's unfiltered rows.
type VaultAPIAnchorReader struct {
	Client VaultAPIClient
}

// AnchoredSessions returns every anchored session id.
//
// It reads ListAll, not List: an anchored goal or topic is usually not itself
// eligible to open, so the filtered rows would answer "nothing anchors this" for
// every manager session — the exact opposite of the truth.
//
// ⚠️ A nil client, an empty index and a read error all return an error or nil
// rather than an empty set. An empty set is a legitimate answer ("nothing anchors
// anything"), and the caller cannot tell it from a vault-api that served zeros —
// so returning it on a failure would dark-fleet every session with no guard. The
// caller fails OPEN on error; an empty success therefore has to be refused here,
// where the row count is still visible.
func (r VaultAPIAnchorReader) AnchoredSessions(
	ctx context.Context,
) (map[string]bool, error) {
	if r.Client == nil {
		return nil, errors.New(ctx, "no vault-api client wired for the anchor check")
	}
	anchored := map[string]bool{}
	rows := 0
	for _, kind := range anchorKinds {
		items, err := r.Client.ListAll(ctx, kind)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "list %s rows for the anchor check", kind)
		}
		rows += len(items)
		for _, item := range items {
			if item.SessionID == "" || terminalAnchorStatuses[item.Status] {
				continue
			}
			anchored[item.SessionID] = true
		}
	}
	if rows == 0 {
		return nil, errors.New(ctx, "the anchor index served no rows at all")
	}
	return anchored, nil
}
