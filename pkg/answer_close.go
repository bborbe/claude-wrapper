// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"strings"
)

// closeSettledCard closes the attention card that a manager's answer_permission
// settled, so the store stops showing a gate nobody can answer.
//
// Best-effort by design: the answer has already landed, and a store failure must
// not turn it into an error the manager reads as a denied permission.
//
// ⚠️ It closes only the card that belongs to the park this answer settled. A
// producer answers many gates over its life and a card from an earlier park can
// still be open, so a close keyed on the producer alone stamped an
// operator-tier card `answered_by: supervisor` on the back of a later benign
// answer — an operator gate recorded as settled by a manager that never
// classified it. cardSettlesPark is the join that prevents it.
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
	question *Question,
	behavior string,
	resolvedBy string,
) {
	if s.Board == nil || producerID == "" || question == nil {
		return
	}
	items, err := s.Board.Open(ctx)
	if err != nil {
		log.Printf("WARNING: attention card close skipped for %q: store unreadable: %v", producerID, err)
		return
	}
	cards := settledCards(items, producerID, question)
	if len(cards) == 0 {
		// The normal case: a tab worker, or a card another arm already closed, or a
		// card from an earlier park that this answer must not stamp.
		return
	}
	if len(cards) > 1 {
		// Two cards for the same producer still match this park — the store carries
		// no finer identity, so which one this answer settles cannot be told. Leave
		// them for attention-answer.py answer.
		log.Printf("attention card close skipped for %q: %d open permission cards (need exactly 1)", producerID, len(cards))
		return
	}
	if err := s.Board.Answer(
		ctx, cards[0].ItemID, "supervisor", resolvedBy, behavior, question.RequestID,
	); err != nil {
		log.Printf("WARNING: attention card %q close failed: %v", cards[0].ItemID, err)
		return
	}
	log.Printf("attention card %q closed by answer_permission (%s, request %s)",
		cards[0].ItemID, behavior, question.RequestID)
}

// settledCards returns every open permission card a producer holds that belongs
// to the park this answer settles.
func settledCards(items []BoardItem, producerID string, question *Question) []BoardItem {
	var matches []BoardItem
	for _, item := range items {
		if item.AnswerMechanism != "permission" || item.State != "open" || item.ProducerID != producerID {
			continue
		}
		if !cardSettlesPark(item, question) {
			continue
		}
		matches = append(matches, item)
	}
	return matches
}

// cardSettlesPark reports whether a card was raised for the park this answer
// settles.
//
// Two joins, in order of strength:
//
//   - A card that names its gate is matched exactly on the request id. That is
//     the only per-request identity the store carries, so it decides alone.
//   - A card that names no gate — every permission card the child's hook
//     produces, which predates the route — can only be joined to the park it was
//     raised for: it must not predate this park (a stale card is one an earlier
//     park left open), and its payload must name this park's tool. A stale
//     operator-tier card from an earlier park fails both.
func cardSettlesPark(item BoardItem, question *Question) bool {
	if item.RequestID != "" {
		return item.RequestID == question.RequestID
	}
	if stale(item, *question) {
		return false
	}
	return cardNamesTool(item.Payload, question.ToolName)
}

// cardNamesTool reports whether a permission card's payload names the tool this
// park is about. The hook writes `<ToolName>: <detail>`, so the tool name is the
// payload's prefix.
func cardNamesTool(payload, toolName string) bool {
	if strings.TrimSpace(toolName) == "" {
		// No tool to compare against: the card cannot be excluded on this axis, and
		// the freshness join has already decided.
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(payload), toolName+":")
}
