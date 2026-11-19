// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
)

// closeSettledCard closes the attention card that a manager's answer_permission
// settled, so the store stops showing a gate nobody can answer.
//
// Best-effort by design: the answer has already landed, and a store failure must
// not turn it into an error the manager reads as a denied permission.
//
// The closed card carries `resolved_by`, so it satisfies `answerFor`'s permission
// arm guard and the daemon's BoardDelivery may try to deliver it. The park it
// belonged to is already settled (the producer's Question is gone), so `deliver`
// finds nothing to answer. The one case `stale()`'s 5 s FreshnessSkew does not
// cover is a worker that re-parks within 5 s of this card's `created_at`; this
// change makes such a card automatic rather than operator-made, so that residual
// is stated rather than hidden.
func (s *RosterToolServer) closeSettledCard(
	ctx context.Context,
	producerID string,
	behavior string,
	resolvedBy string,
) {
	if s.Board == nil || producerID == "" {
		return
	}
	items, err := s.Board.Open(ctx)
	if err != nil {
		log.Printf("WARNING: attention card close skipped for %q: store unreadable: %v", producerID, err)
		return
	}
	cards := openPermissionCards(items, producerID)
	if len(cards) == 0 {
		// The normal case: a tab worker, or a card another arm already closed.
		return
	}
	if len(cards) > 1 {
		// The card carries no per-request field, so which card this answer settles
		// cannot be told. Leave them for attention-answer.py answer.
		log.Printf("attention card close skipped for %q: %d open permission cards (need exactly 1)", producerID, len(cards))
		return
	}
	if err := s.Board.Answer(ctx, cards[0].ItemID, "supervisor", resolvedBy, behavior); err != nil {
		log.Printf("WARNING: attention card %q close failed: %v", cards[0].ItemID, err)
		return
	}
	log.Printf("attention card %q closed by answer_permission (%s)", cards[0].ItemID, behavior)
}

// openPermissionCards returns every open permission card a producer holds.
//
// The card carries no per-request field, so the join is by producer: the caller
// closes a card only when exactly one matches.
func openPermissionCards(items []BoardItem, producerID string) []BoardItem {
	var matches []BoardItem
	for _, item := range items {
		if item.AnswerMechanism == "permission" && item.State == "open" && item.ProducerID == producerID {
			matches = append(matches, item)
		}
	}
	return matches
}
