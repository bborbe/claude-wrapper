// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"log"
	"time"
)

// This file is the child's ledger write path: what a finished turn, an abandoned
// turn and a turn that never reached the child file on the spawn ledger. Split out
// of supervisor.go at that file's 2000-line lint limit, along the seam that matters:
// everything here answers "what did this turn end in", while supervisor.go owns the
// child's process and event stream.

// recordFailedTurn files an error verdict for a turn that never reached the child.
//
// It is synchronous, unlike the result path: the caller is already returning an
// error, so there is no event loop to protect, and leaving the write in flight
// would let it land after a retry's re-open.
func (c *child) recordFailedTurn(turn uint64, writeErr error) {
	if c.ledger == nil {
		return
	}
	//nolint:errchkjson // a map of strings and a bool cannot fail to encode
	encoded, err := json.Marshal(map[string]any{
		"subtype":  "error",
		"is_error": true,
		"error":    "the turn could not be written to the child: " + writeErr.Error(),
	})
	if err != nil {
		log.Printf(
			"WARNING: marshal failed-turn verdict for session %s failed: %v", c.id, err,
		)
		return
	}
	c.patchVerdict(turn, true, c.ctx, NewTerminalLedgerPatch("error", encoded, c.endedAt()))
}

// closedTurnLocked is the sequence number of the turn a result just closed.
//
// ⚠️ **It is NOT c.turnSeq, and taking turnSeq was a real defect.** Two sends can be
// in flight — Send is not gated on pendingTurns — and results close turns in the
// order the turns were written, so with turns 1 and 2 open the first result closes
// turn 1. The counter has already been decremented when this is called, so the
// closed turn is turnSeq minus however many turns are still open.
func (c *child) closedTurnLocked() uint64 {
	//nolint:gosec // pendingTurns is a small non-negative counter guarded by the comparison below
	open := uint64(c.pendingTurns)
	if open >= c.turnSeq {
		return c.turnSeq
	}
	return c.turnSeq - open
}

// recordTurnVerdict files the ledger's terminal verdict for a turn that just
// closed.
//
// ⚠️ **The caller holds c.mutex and this method does not wait for the store.** apply
// runs on the single goroutine that reads the child's stream, so an HTTP round trip
// under that lock would stall every later event. The write runs on its own
// goroutine, and the event is marshalled there: a published event map is read-only,
// so sharing it is safe.
//
// ⚠️ **The verdict is the child's own event, not a summary.** `result.subtype` and
// `result.is_error` are what every reader of the node ledger keys on, so carrying
// the event verbatim makes this row readable without a second translation.
func (c *child) recordTurnVerdict(event map[string]any, turn uint64) {
	if !c.canWriteVerdict() {
		return
	}
	status := "done"
	if isError, ok := event["is_error"].(bool); ok && isError {
		status = "error"
	}
	go c.fileTurnVerdict(turn, status, event)
}

// fileTurnVerdict marshals the turn's own result event and files it.
//
// It runs on its own goroutine, which is why the marshal lives here rather than in
// recordTurnVerdict: a result event carries the whole final message, and encoding
// it under c.mutex would stall the event loop for every later event.
func (c *child) fileTurnVerdict(turn uint64, status string, event map[string]any) {
	encoded, err := json.Marshal(event)
	if err != nil {
		log.Printf(
			"WARNING: marshal result event for session %s failed, no ledger verdict written: %v",
			c.id, err,
		)
		return
	}
	c.patchVerdict(turn, true, c.ctx, NewTerminalLedgerPatch(status, encoded, c.endedAt()))
}

// canWriteVerdict reports whether a verdict can be filed at all, and names the
// misconfiguration when it cannot.
//
// A child always carries both fields — startChild and Adopt copy them from the
// supervisor — so a nil here is a construction bug rather than a fleet state, and
// it is said out loud instead of falling back to a context the process never
// owned.
func (c *child) canWriteVerdict() bool {
	if c.ledger == nil {
		return false
	}
	if c.ctx == nil {
		log.Printf(
			"WARNING: session %s has no process context — its turn's ledger verdict is not written",
			c.id,
		)
		return false
	}
	return true
}

// endedAt renders the instant this verdict is filed at.
func (c *child) endedAt() string {
	return c.now.Now().Time().UTC().Format(time.RFC3339)
}

// recordAbandonedTurn files the verdict for a turn the child left open.
//
// It is the ported `catch` half of the node's terminal patch: a stream that ends
// while a turn is in flight leaves that turn with no result, and without this
// write the row would keep asserting whatever it last said — or, on a fresh
// spawn, nothing at all. The cause is carried INSIDE `result` rather than as an
// `error` field, because the store has no such field and a patch naming one is
// refused outright.
func (c *child) recordAbandonedTurn(exitCode *int) {
	if !c.canWriteVerdict() {
		return
	}
	cause := "the child left while a turn was still open"
	if exitCode != nil {
		cause = fmt.Sprintf(
			"the child exited with code %d while a turn was still open", *exitCode,
		)
	}
	//nolint:errchkjson // a two-key map of a bool and a string cannot fail to encode
	encoded, err := json.Marshal(map[string]any{
		"subtype":  "error",
		"is_error": true,
		"error":    cause,
	})
	if err != nil {
		log.Printf("WARNING: marshal abandoned-turn verdict for session %s failed: %v", c.id, err)
		return
	}
	// The turn that was open is the newest one asked for; no later turn can be
	// written once the child is gone.
	go c.fileAbandonedVerdict(c.turnSeq, encoded)
}

// fileAbandonedVerdict files the already-encoded error verdict.
//
// The encoding happened under c.mutex and is tiny, so it stays there; only the
// store round trip is moved off the lock.
func (c *child) fileAbandonedVerdict(turn uint64, encoded json.RawMessage) {
	c.patchVerdict(turn, true, c.ctx, NewTerminalLedgerPatch("error", encoded, c.endedAt()))
}

// verdictReopenTimeout bounds the one ledger write on a caller's critical path.
//
// It is deliberately much shorter than the ledger client's own 15 s: this write
// only has to precede the turn, and a store that cannot answer in three seconds
// is a store whose answer the turn should not wait for. The turn's own result
// writes the row a moment later regardless.
const verdictReopenTimeout = 3 * time.Second

// tryPatchVerdict applies a patch without WAITING for an in-flight write.
//
// It is the send path's variant: a caller writing a turn must never queue behind a
// verdict goroutine, so an occupied ledgerMu means "do it later" rather than
// "wait".
//
// ⚠️ **Later, not never — dropping it was a defect.** A skipped re-open leaves the
// row reading the PREVIOUS turn's terminal status while the new turn is live, and
// the heartbeat poster reads a terminal row as ended: a working worker would stop
// being stamped. So the write is deferred to a goroutine that waits its turn; the
// supersede and terminal guards still apply when it lands, which is what makes a
// late re-open safe — if this turn's verdict got there first, the deferred re-open
// is dropped rather than resurrecting a finished turn.
func (c *child) tryPatchVerdict(
	turn uint64,
	terminal bool,
	ctx context.Context,
	patch LedgerPatch,
) {
	if c.ledgerMu.TryLock() {
		defer c.ledgerMu.Unlock()
		bounded, cancel := context.WithTimeout(ctx, verdictReopenTimeout)
		defer cancel()
		c.applyVerdict(turn, terminal, bounded, patch)
		return
	}
	log.Printf(
		"claude-wrapper: ledger write in flight for session %s — deferring this turn's re-open rather than stalling the turn",
		c.id,
	)
	go c.patchVerdict(turn, terminal, c.ctx, patch)
}

// patchVerdict applies one ledger patch and logs what could not land.
//
// It waits for an in-flight write, unlike tryPatchVerdict: every caller is off the
// turn's critical path, so ordering matters more than latency there.
func (c *child) patchVerdict(
	turn uint64,
	terminal bool,
	ctx context.Context,
	patch LedgerPatch,
) {
	c.ledgerMu.Lock()
	defer c.ledgerMu.Unlock()
	c.applyVerdict(turn, terminal, ctx, patch)
}

// applyVerdict is the shared body: the supersede guard, then the write.
//
// The caller holds ledgerMu.
func (c *child) applyVerdict(
	turn uint64,
	terminal bool,
	ctx context.Context,
	patch LedgerPatch,
) {
	// A write for a turn that has already been superseded is DROPPED, not applied.
	//
	// ⚠️ The store has no compare-and-set, so the row cannot be guarded where it is
	// written — only here, where the turn it belongs to is still known. Dropping is
	// the safe direction: the superseding write carries a later turn, and a stale
	// verdict landing on top of it would leave a worker mid-turn reading `done`.
	if turn < c.ledgerSeq {
		log.Printf(
			"claude-wrapper: dropping a superseded ledger verdict for session %s (turn %d, row already at turn %d)",
			c.id, turn, c.ledgerSeq,
		)
		return
	}
	// ⚠️ **A re-open never overwrites its own turn's terminal verdict.** The two
	// writes can race for the SAME turn: `send` opens turn N with `running`, and if
	// the child dies before that write reaches the store, `reap` writes turn N's
	// `error` first. A `turn < ledgerSeq` guard passes the late `running` write (it
	// is the same turn), and a dead turn would then read live. The reverse order is
	// legitimate and stays allowed — a terminal verdict must be able to replace the
	// `running` re-open of the turn it closes.
	if !terminal && turn <= c.ledgerTerminalTurn {
		log.Printf(
			"claude-wrapper: dropping a re-open for session %s turn %d — that turn already has a terminal verdict",
			c.id, turn,
		)
		return
	}
	c.ledgerSeq = turn
	if terminal {
		c.ledgerTerminalTurn = turn
	}
	err := c.ledger.Patch(ctx, c.id, patch)
	if err == nil {
		return
	}
	if stderrors.Is(err, ErrLedgerRowAbsent) {
		log.Printf(
			"claude-wrapper: no ledger row for session %s — verdict not recorded: %v",
			c.id, err,
		)
		return
	}
	log.Printf("WARNING: ledger verdict for session %s failed: %v", c.id, err)
}
