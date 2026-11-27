// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"sync"
)

// stallEscalationProducerID names this producer on the attention board.
const stallEscalationProducerID = "claude-wrapper-stalled-row"

// escalateStall posts one card for a row the tick has stopped re-opening.
//
// Before this, `progress-stalled` was a silent terminal hold: the row was skipped
// every tick and named nowhere the operator reads, so a worker that ended on an
// uncarded gate stayed invisible until someone found it by hand — 25 h in the
// 2026-11-26 case this exists to close.
//
// The dedup key carries the Progress hash, so the card is per EPISODE: a row that
// stalls, is carded, then moves and stalls again gets a second card, while every
// tick of the same episode finds the first one and posts nothing. The lookup is
// what guarantees that, not the tick count — the daemon restarts on every deploy
// and the stall clock survives the restart, so a counter would re-post after each
// one.
//
// A failure to post never changes the hold: the row is still skipped, and the
// failure is logged. An unreachable store must not turn into a tick that opens a
// stalled row.
func (t *FlaggedTick) escalateStall(ctx context.Context, row FlaggedRow) {
	if t.Poster == nil || t.Cards == nil {
		return
	}
	key := stallEscalationDedupKey(row)
	// The in-process memory answers the common case without a store read: a row
	// stays stalled for many ticks, and asking the board "is there a card for this
	// key" on every one of them spends the predicate's ReadTimeout budget on a
	// question whose answer this process already knows. The store lookup still runs
	// on the first tick of an episode — and after a restart, when this map is empty
	// — so the cross-restart guarantee is unchanged.
	if t.stallEscalatedFor(ctx).Done(key) {
		return
	}
	existing, err := t.Cards.ExistingCard(ctx, stallEscalationProducerID, key)
	if err != nil {
		t.logf(
			"flagged-tick: stall-escalation row=%s outcome=lookup-failed error=%v",
			row.Name, err,
		)
		return
	}
	if existing != "" {
		t.stallEscalatedFor(ctx).Record(key)
		return
	}
	itemID, err := t.Poster.Post(ctx, AttentionPostRequest{
		ProducerID:      stallEscalationProducerID,
		ProducerKind:    "cron",
		LivenessRef:     "owner:" + stallEscalationProducerID,
		DedupKey:        key,
		InterruptClass:  "routine",
		Payload:         stallPayload(row),
		AnswerMechanism: "ack",
	})
	if err != nil {
		t.logf(
			"flagged-tick: stall-escalation row=%s outcome=post-failed error=%v",
			row.Name, err,
		)
		return
	}
	t.stallEscalatedFor(ctx).Record(key)
	t.logf("flagged-tick: stall-escalation row=%s card=%s", row.Name, itemID)
}

// stallEscalatedTracker remembers, in this process, which stall episodes have
// already been carded.
//
// ⚠️ In process only, and that is deliberate — the same trade `spawnNotLandedRetried`
// makes. It is an OPTIMISATION, never the guarantee: the board lookup is what
// makes "one card per episode" true across restarts, and a restart simply pays
// that lookup once more. Bounded by the number of distinct stall episodes this
// process ever saw; entries are never pruned, because a pruned entry would cost a
// store read on the next tick of a stall that has not moved.
type stallEscalatedTracker struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func (s *stallEscalatedTracker) Done(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.keys[key]
	return ok
}

func (s *stallEscalatedTracker) Record(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = map[string]struct{}{}
	}
	s.keys[key] = struct{}{}
}

// stallEscalatedFor returns the process-wide escalation memory.
func (t *FlaggedTick) stallEscalatedFor(_ context.Context) *stallEscalatedTracker {
	t.stallEscalatedOnce.Do(func() {
		t.stallEscalated = &stallEscalatedTracker{}
	})
	return t.stallEscalated
}

// stallEscalationDedupKey identifies one stall episode.
//
// The Progress hash is the episode: it changes only when the row moves, so the
// same hash is the same stall and a new hash is a new one. The row key supplies
// identity across restarts, when the session id may have gone.
func stallEscalationDedupKey(row FlaggedRow) string {
	return "stalled|" + row.Key() + "|" + row.ProgressHash
}

// stallPayload is the card body: which row, what it last said, and what the tick
// has been doing about it.
func stallPayload(row FlaggedRow) string {
	var b strings.Builder
	b.WriteString("Flagged row held as progress-stalled: ")
	b.WriteString(row.Name)
	b.WriteString(" (")
	b.WriteString(row.Vault)
	b.WriteString(")")
	if row.ProgressTail != "" {
		b.WriteString("\nLast progress: ")
		b.WriteString(row.ProgressTail)
	}
	b.WriteString("\nIts # Progress section has not moved for ")
	b.WriteString(progressStallThreshold.String())
	b.WriteString(
		", so the flagged tick has stopped re-opening it. Card it, or clear it — " +
			"nothing else will surface this row.",
	)
	return b.String()
}

// progressStallFor builds the stall clock once per tick, over the same state
// directory the row cache uses, so the clock survives a restart.
func (t *FlaggedTick) progressStallFor(ctx context.Context) *progressStallTracker {
	t.progressStallOnce.Do(func() {
		t.progressStall = newProgressStallTracker(t.flaggedCacheDir(ctx))
	})
	return t.progressStall
}
