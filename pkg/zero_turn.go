// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
)

// zeroTurnRetryBudget bounds how many times one child's turn is re-sent after a
// close that produced nothing.
//
// One is deliberate. A second attempt covers the transient stream failure this
// exists for; a stream that fails twice is a real fault, and retrying it further
// would turn a visible failure into a spin.
const zeroTurnRetryBudget = 1

// zeroTurnGuard tracks the turn currently in flight: how many assistant messages
// it has produced, the prompt that opened it, and the re-sends it has spent.
//
// Every field is read and written under the child's mutex — apply updates the
// count from the event loop, and the stop path reads it from its own goroutine.
type zeroTurnGuard struct {
	turns   int
	text    string
	retries int
}

// begin starts a new turn. The count is per-turn, never cumulative: the question
// a close answers is whether THIS turn produced anything, and a cumulative count
// would answer a different one and never fire on a healthy child's later turns.
func (g *zeroTurnGuard) begin(text string) {
	g.turns = 0
	g.text = text
}

// recordAssistant counts one assistant message against the turn in flight.
func (g *zeroTurnGuard) recordAssistant() { g.turns++ }

// applyAssistant is the event loop's handling of one assistant message: it takes
// the model the child reports for itself, and counts the message against the turn
// in flight.
//
// A synthetic message is the child's own placeholder for a turn it did not send
// to a model, so it must not count as output — counting it would make a stream
// that died mid-flight read as a healthy turn.
func (c *child) applyAssistant(event map[string]any) {
	message, ok := event["message"].(map[string]any)
	if !ok {
		return
	}
	if model, ok := message["model"].(string); ok && model != "" && model != syntheticModel {
		c.session.Model = model
		c.session.ModelSource = "assistant"
	}
	if model, _ := message["model"].(string); model != syntheticModel {
		c.zeroTurns.recordAssistant()
	}
}

// retryZeroTurn re-sends the last prompt when the turn that just closed produced
// no assistant message, and reports whether it took that decision so the caller
// returns instead of stopping the child.
//
// The turn is genuinely lost without this: the child has consumed the prompt, and
// a headless worker stopped here leaves its task file untouched and its
// `# Results` empty — work that reads as a session which ran and produced
// nothing. Re-sending is safe precisely because the count is zero: nothing was
// produced, so nothing can be duplicated.
func (c *child) retryZeroTurn(ctx context.Context) bool {
	// ⚠️ Check, spend AND reserve in ONE critical section. closeHeadlessTurnEnd runs
	// on its own goroutine per result event, so two turn ends arriving together
	// would otherwise both read the budget unspent and both re-send the same
	// prompt.
	//
	// `retrying` is the reservation, and it is not tidiness: the write below must
	// happen outside this lock (send takes it), and a duplicate turn end that ran
	// in that gap would read the child idle and kill it before the re-send's own
	// pendingTurns++ landed — spending the budget and losing the attempt. With the
	// flag set, keepReasonLocked reports the child as having an open turn for the
	// whole gap.
	c.mutex.Lock()
	turns := c.zeroTurns.turns
	text := c.zeroTurns.text
	if c.retrying || turns > 0 || text == "" || c.zeroTurns.retries >= zeroTurnRetryBudget {
		c.mutex.Unlock()
		return false
	}
	c.zeroTurns.retries++
	spent := c.zeroTurns.retries
	c.retrying = true
	c.mutex.Unlock()
	log.Printf(
		"agent %s: turn closed after %d assistant turns — re-sending the same turn (attempt %d of %d)",
		c.id,
		turns,
		spent,
		zeroTurnRetryBudget,
	)
	// ⚠️ Clear the reservation on BOTH outcomes. A successful send leaves
	// pendingTurns>0, so the child stays kept on its own; a failed one must become
	// stoppable again, or a write error would park the worker forever.
	sendErr := c.send(ctx, text)
	c.mutex.Lock()
	c.retrying = false
	c.mutex.Unlock()
	if sendErr != nil {
		log.Printf("WARNING: agent %s: zero-turn retry failed: %v", c.id, sendErr)
		return false
	}
	return true
}
