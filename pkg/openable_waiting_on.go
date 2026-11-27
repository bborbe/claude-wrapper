// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// OpenableWaitingOn is the optional `waiting_on` object of a held row in
// GET /api/1.0/openable. Every field except Summary is omitted when the tick does
// not know it; a value is never invented.
type OpenableWaitingOn struct {
	// Summary is one human line, always set when the object is present.
	Summary string `json:"summary"`
	// Since is when the wait started (RFC3339), e.g. the stall clock's start.
	Since string `json:"since,omitempty"`
	// Until is the parked-until deadline (RFC3339).
	Until string `json:"until,omitempty"`
	// CardID is the attention card the row waits on or was escalated on.
	CardID string `json:"card_id,omitempty"`
	// PRURL is the pull request a parked-on-pr row waits on.
	PRURL string `json:"pr_url,omitempty"`
	// Blockers are the row's blocked_by names.
	Blockers []string `json:"blockers,omitempty"`
	// SessionID is the live session holding a not-absent row.
	SessionID string `json:"session_id,omitempty"`
}

// waitingOn fills what the tick already knows about why row is held. It is
// read-only: the stall clock is read with SinceRead, the park declaration without
// a probe, and the card lookup is a GET. It returns nil when there is nothing to
// say beyond the reason token.
func (t *FlaggedTick) waitingOn(
	ctx context.Context,
	row FlaggedRow,
	reason string,
	recent map[string]time.Time,
	now time.Time,
) *OpenableWaitingOn {
	switch {
	case reason == reasonBlocked:
		return &OpenableWaitingOn{
			Summary:  fmt.Sprintf("blocked by %d task(s)", len(row.BlockedBy)),
			Blockers: append([]string(nil), row.BlockedBy...),
		}
	case reason == reasonProgressStalled:
		return t.stalledWaitingOn(ctx, row, now)
	case strings.HasPrefix(reason, string(ParkedUntil)),
		strings.HasPrefix(reason, string(ParkedOnPR)),
		strings.HasPrefix(reason, string(ParkedOnCard)),
		strings.HasPrefix(reason, string(ParkedOnHost)):
		return t.parkedWaitingOn(ctx, row, reason)
	case isNotAbsentReason(reason):
		return notAbsentWaitingOn(row, reason)
	case reason == reasonRecentlySpawned:
		return recentlySpawnedWaitingOn(row, recent, now)
	case reason == reasonForeignVault:
		return &OpenableWaitingOn{
			Summary: "row belongs to a vault family this daemon does not serve",
		}
	case strings.HasPrefix(reason, RefusalProvenanceUnrecorded):
		// The refusal carries a ": no actor recorded; …" suffix, so match the token prefix.
		return &OpenableWaitingOn{
			Summary: "flag provenance is not recorded; nobody attributed the flag",
		}
	case reason == OpenReasonSpawnRefused:
		return &OpenableWaitingOn{Summary: "spawn refused"}
	}
	return nil
}

func (t *FlaggedTick) stalledWaitingOn(
	ctx context.Context,
	row FlaggedRow,
	now time.Time,
) *OpenableWaitingOn {
	waiting := &OpenableWaitingOn{Summary: "no progress"}
	since, ok := t.progressStallFor(ctx).SinceRead(row.Key(), row.ProgressHash+"|"+row.Met, now)
	if ok {
		waiting.Since = since.Format(time.RFC3339)
		waiting.Summary = "no progress since " + waiting.Since
	}
	if t.Cards != nil {
		// A failed lookup leaves the card unnamed rather than guessing one.
		cardID, err := t.Cards.ExistingCard(
			ctx, stallEscalationProducerID, stallEscalationDedupKey(row),
		)
		if err == nil && cardID != "" {
			waiting.CardID = cardID
			waiting.Summary += "; escalated on card"
		}
	}
	return waiting
}

func (t *FlaggedTick) parkedWaitingOn(
	ctx context.Context,
	row FlaggedRow,
	reason string,
) *OpenableWaitingOn {
	waiting := &OpenableWaitingOn{Summary: "parked: " + reason}
	declarer, ok := t.Parked.(ParkedDeclarer)
	if !ok {
		return waiting
	}
	declaration, found := declarer.Declaration(ctx, row)
	// The closer is re-read here; a declaration whose class no longer matches the
	// held reason is not what held the row, so its detail is not reported.
	if !found || !strings.HasPrefix(reason, string(declaration.Class)) {
		return waiting
	}
	switch declaration.Class {
	case ParkedUntil:
		if declaration.Deadline != nil {
			waiting.Until = declaration.Deadline.Format(time.RFC3339)
			waiting.Summary = "parked until " + waiting.Until
			break
		}
		// A closer naming an event, not a clock, falls through ClassifyParked to
		// parked-until with a nil Deadline; Hold has no deadline either and releases
		// the row MAX_AGE_S after first sighting. Say so — never invent an `until`.
		waiting.Summary = "parked on an event with no deadline (released " +
			MAX_AGE_S.String() + " after first sighting)"
		if text := truncateRunes(declaration.Text, parkedSummaryTextRunes); text != "" {
			waiting.Summary += ": " + text
		}
	case ParkedOnPR:
		waiting.PRURL = fmt.Sprintf(
			"https://github.com/%s/%s/pull/%d",
			declaration.PR.Owner, declaration.PR.Repo, declaration.PR.Number,
		)
		waiting.Summary = fmt.Sprintf(
			"parked on PR %s/%s#%d", declaration.PR.Owner, declaration.PR.Repo, declaration.PR.Number,
		)
	case ParkedOnCard:
		// The card probe keys on the row's session, not an item id, so the card id
		// is unknown here and stays omitted.
		waiting.Summary = "parked on an open attention card"
	case ParkedOnHost:
		waiting.Summary = fmt.Sprintf("parked on host %s:%d", declaration.Host, declaration.Port)
	}
	return waiting
}

// parkedSummaryTextRunes bounds the closer's declaration text quoted in a
// no-deadline parked-until summary, so one long closer line cannot bloat a row.
const parkedSummaryTextRunes = 160

// truncateRunes cuts text to at most limit runes, marking a cut with "…".
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// notAbsentWaitingOn names the session holding the row. For not-absent-dead the
// ledger already records the session as ended, so the line must not claim it is
// live — only that its absence is not yet proven.
func notAbsentWaitingOn(row FlaggedRow, reason string) *OpenableWaitingOn {
	if row.SessionID == "" {
		return nil
	}
	short := row.SessionID
	if len(short) > 8 {
		short = short[:8]
	}
	// Only a LIVE liveness read justifies "still live"; UNKNOWN means the read failed.
	summary := "session " + short + " liveness unknown; absence not yet proven"
	switch {
	case reason == reasonNotAbsentDead:
		summary = "session " + short + " ended per ledger; absence not yet proven"
	case row.Liveness == "LIVE":
		summary = "session " + short + " still live"
	}
	return &OpenableWaitingOn{
		Summary:   summary,
		SessionID: row.SessionID,
	}
}

// recentlySpawnedWaitingOn names the spawn time only from a ledger entry inside
// the same window the tick's decision applies, under the same keys (session id,
// then label), so a stale entry never becomes the reported start.
func recentlySpawnedWaitingOn(
	row FlaggedRow,
	recent map[string]time.Time,
	now time.Time,
) *OpenableWaitingOn {
	var at time.Time
	ok := false
	for _, key := range []string{row.SessionID, labelKey(row.Name)} {
		if key == "" {
			continue
		}
		if candidate, found := recent[key]; found && now.Sub(candidate) < recentlySpawnedWindow {
			at, ok = candidate, true
			break
		}
	}
	waiting := &OpenableWaitingOn{
		Summary: "recently spawned; held for " + recentlySpawnedWindow.String(),
	}
	if ok {
		waiting.Since = at.Format(time.RFC3339)
		waiting.Summary = "spawned " + waiting.Since + "; held for " + recentlySpawnedWindow.String()
	}
	return waiting
}
