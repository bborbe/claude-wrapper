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

// zeroTurn is one turn written to the child: the prompt that opened it and the
// assistant messages it has produced.
type zeroTurn struct {
	text  string
	turns int
}

// zeroTurnGuard tracks the turns in flight as a FIFO, and the re-sends spent.
//
// ⚠️ **A FIFO, not a single turn.** Two sends can be in flight — `Send` is not
// gated on `pendingTurns`, which is the same state `closedTurnLocked` documents —
// and results close turns in the order the turns were written. A guard holding
// only the most recent turn would judge the older turn's close against the newer
// turn's count and re-send the newer, already-delivered prompt: it would duplicate
// a turn AND leave the turn that actually failed unretried. Keeping the queue in
// write order makes "the turn that just closed" the head, which is the only turn a
// close can be about.
//
// Every field is read and written under the child's mutex — apply updates the
// queue from the event loop, and the stop path reads it from its own goroutine.
type zeroTurnGuard struct {
	open []zeroTurn
	// closed is the turn the most recent result popped, kept so the retry decision
	// and the exit record can read the turn that actually closed.
	closed  *zeroTurn
	retries int
}

// begin records a new turn at the tail of the queue. The count is per-turn, never
// cumulative: the question a close answers is whether THIS turn produced anything,
// and a cumulative count would answer a different one and never fire on a healthy
// child's later turns.
func (g *zeroTurnGuard) begin(text string) {
	g.open = append(g.open, zeroTurn{text: text})
}

// dropLast removes the turn a failed write never delivered, so the queue stays in
// step with pendingTurns.
func (g *zeroTurnGuard) dropLast() {
	if len(g.open) > 0 {
		g.open = g.open[:len(g.open)-1]
	}
}

// recordAssistant counts one assistant message against the turn being processed,
// which is the head of the queue: the child answers turns in the order it was
// given them.
func (g *zeroTurnGuard) recordAssistant() {
	if len(g.open) > 0 {
		g.open[0].turns++
	}
}

// close pops the turn a result just closed and keeps it as the most recent one.
// It reports whether a turn was open to close.
func (g *zeroTurnGuard) close() bool {
	if len(g.open) == 0 {
		return false
	}
	closed := g.open[0]
	g.open = g.open[1:]
	g.closed = &closed
	return true
}

// turns reports the assistant-message count of the most recent turn — the newest
// open one, or the last one closed when nothing is open. That is the value the
// exit record's `turns=` carries, and it is what separates a healthy close from a
// turn whose stream produced nothing.
func (g *zeroTurnGuard) turns() int {
	if n := len(g.open); n > 0 {
		return g.open[n-1].turns
	}
	if g.closed != nil {
		return g.closed.turns
	}
	return 0
}

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

// retryZeroTurn re-sends the prompt of the turn that just closed when that turn
// produced no assistant message, and reports whether it took that decision so the
// caller returns instead of stopping the child.
//
// ⚠️ **The prompt re-sent is the CLOSED turn's, never "the latest one written".**
// With two turns in flight the closed one is the older; re-sending the newer would
// duplicate a turn the child has already been given while leaving the failed one
// unretried. The guard's FIFO is what makes the closed turn addressable.
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
	closed := c.zeroTurns.closed
	if c.retrying || closed == nil || closed.turns > 0 || closed.text == "" ||
		c.zeroTurns.retries >= zeroTurnRetryBudget {
		c.mutex.Unlock()
		return false
	}
	turns := closed.turns
	text := closed.text
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
