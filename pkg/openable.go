// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// Openable decision values.
const (
	OpenableDecisionOpen = "open"
	OpenableDecisionHeld = "held"
)

// OpenableEntry is one candidate row's verdict from the flagged tick's own
// decision.
//
// It is the read shape of GET /api/1.0/openable: the endpoint reports what the
// tick would act on this cycle, using the tick's own reason tokens, so a manager
// reads one deterministic verdict instead of re-deriving it from the vault and
// the wrapper log.
type OpenableEntry struct {
	Vault     string `json:"vault"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	// Decision is "open" when the tick would open the row this cycle, "held"
	// otherwise.
	Decision string `json:"decision"`
	// Reason is the tick's own skip/refusal token (empty for an open row).
	Reason string `json:"reason"`
	Phase  string `json:"phase"`
	// FlagSetBy is the page's raw `flag_set_by`.
	FlagSetBy string `json:"flag_set_by"`
}

// Openable answers, for every row the tick would consider this cycle, whether it
// would open the row and — when held — the tick's own reason token.
//
// It is read-only: it assembles exactly the inputs tickFromRows does and runs the
// same classify() gates, returning the verdict instead of opening anything. The
// tick and this endpoint therefore share ONE decision function and cannot drift.
func (t *FlaggedTick) Openable(ctx context.Context) ([]OpenableEntry, error) {
	rows, err := t.Source.Rows(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read flagged rows failed")
	}
	// Same input assembly as tickFromRows, in the same order, so the verdict is
	// the tick's and not a second reading of the fleet.
	ledger := t.ledgerRows(ctx)
	recent := recentlySpawned(ledger)
	t.mergeChildSpawnTimes(ctx, recent)
	t.mergeOpened(recent)
	dead := endedSessions(ledger)
	childLive := t.childLiveness(ctx)
	now := t.now()
	ordered := orderFlaggedRows(rows)
	out := make([]OpenableEntry, 0, len(ordered))
	for _, row := range ordered {
		reason, action, _ := t.classify(ctx, row, recent, dead, childLive, now)
		decision := OpenableDecisionOpen
		if action != rowEligible {
			decision = OpenableDecisionHeld
		}
		out = append(out, OpenableEntry{
			Vault:     row.Vault,
			Kind:      row.Kind,
			Name:      row.Name,
			SessionID: row.SessionID,
			Decision:  decision,
			Reason:    reason,
			Phase:     row.Phase,
			FlagSetBy: row.FlagSetBy,
		})
	}
	return out, nil
}
