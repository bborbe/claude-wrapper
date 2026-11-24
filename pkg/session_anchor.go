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

// AnchorReader answers whether any page names a session as its anchor.
//
// The wake text says "take the next unchecked subtask on your anchored task", so
// a session nothing anchors cannot act on it. This port answers that question
// through the same shared resolver every other ownership filter uses, rather than
// walking the vault a fourth time.
type AnchorReader interface {
	// Anchored reports whether a task, goal or topic records sessionID as its
	// `claude_session_id`.
	Anchored(ctx context.Context, sessionID string) (bool, error)
}

// VaultAPIAnchorReader answers it from the local vault-api's unfiltered rows.
type VaultAPIAnchorReader struct {
	Client VaultAPIClient
}

// Anchored reports whether any task, goal or topic records sessionID.
//
// It reads ListAll, not List: an anchored goal or topic is usually not itself
// eligible to open, so the filtered rows would answer "nothing anchors this" for
// every manager session — the exact opposite of the truth.
//
// A nil client answers "anchored", so a caller that wired none cannot silently
// skip every session. The first read error is returned rather than a partial
// "not anchored": the caller fails OPEN, and a partial answer must never be
// mistaken for a complete one.
func (r VaultAPIAnchorReader) Anchored(ctx context.Context, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	if r.Client == nil {
		return true, nil
	}
	for _, kind := range anchorKinds {
		rows, err := r.Client.ListAll(ctx, kind)
		if err != nil {
			return false, errors.Wrapf(ctx, err, "list %s rows for the anchor check", kind)
		}
		for _, row := range rows {
			if row.SessionID == sessionID {
				return true, nil
			}
		}
	}
	return false, nil
}
