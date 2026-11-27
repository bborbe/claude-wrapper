// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Openable waiting_on", func() {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	only := func(tick *FlaggedTick) OpenableEntry {
		got, err := tick.Openable(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(got).To(gomega.HaveLen(1))
		return got[0]
	}

	ginkgo.It("names the blockers of a blocked row", func() {
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "b", Branch: "create",
			Liveness: "none", BlockedBy: []string{"[[x]]", "[[y]]"},
		}
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, nil, nil)
		tick.Now = fixedNow(now)

		entry := only(tick)

		gomega.Expect(entry.Reason).To(gomega.Equal(reasonBlocked))
		gomega.Expect(entry.WaitingOn).ToNot(gomega.BeNil())
		gomega.Expect(entry.WaitingOn.Summary).To(gomega.Equal("blocked by 2 task(s)"))
		gomega.Expect(entry.WaitingOn.Blockers).To(gomega.Equal([]string{"[[x]]", "[[y]]"}))
	})

	ginkgo.It("gives a stalled row its stall start and escalation card, read-only", func() {
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "s", Branch: "create",
			Liveness: "none", Phase: "execution", Met: "0/3", ProgressHash: "h1",
		}
		poster := &recordingPoster{}
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, nil, nil)
		tick.CacheDir = ginkgo.GinkgoT().TempDir()
		tick.Poster = poster
		tick.Cards = &postedCardLookup{poster: poster}
		tick.Now = fixedNow(now)
		// The stall clock started 40 minutes ago, and the tick carded the episode.
		started := now.Add(-40 * time.Minute)
		tick.progressStallFor(ctx).Stalled(row.Key(), "h1|0/3", started)
		poster.posted = map[string]string{stallEscalationDedupKey(row): "card-42"}

		entry := only(tick)

		gomega.Expect(entry.Reason).To(gomega.Equal(reasonProgressStalled))
		gomega.Expect(entry.WaitingOn).ToNot(gomega.BeNil())
		gomega.Expect(entry.WaitingOn.Since).To(gomega.Equal(started.Format(time.RFC3339)))
		gomega.Expect(entry.WaitingOn.CardID).To(gomega.Equal("card-42"))
		gomega.Expect(entry.WaitingOn.Summary).To(gomega.Equal(
			"no progress since " + started.Format(time.RFC3339) + "; escalated on card"))
		// The read posted nothing and left the clock untouched.
		gomega.Expect(poster.requests).To(gomega.BeEmpty())
		since, ok := tick.progressStallFor(ctx).SinceRead(row.Key(), "h1|0/3", now)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(since).To(gomega.Equal(started))
	})

	ginkgo.It("omits the card when the stall was never escalated", func() {
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "s", Branch: "create",
			Liveness: "none", Phase: "execution", Met: "0/3", ProgressHash: "h1",
		}
		poster := &recordingPoster{}
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, nil, nil)
		tick.CacheDir = ginkgo.GinkgoT().TempDir()
		tick.Cards = &postedCardLookup{poster: poster}
		tick.Now = fixedNow(now)
		tick.progressStallFor(ctx).Stalled(row.Key(), "h1|0/3", now.Add(-time.Hour))

		entry := only(tick)

		gomega.Expect(entry.WaitingOn).ToNot(gomega.BeNil())
		gomega.Expect(entry.WaitingOn.CardID).To(gomega.BeEmpty())
		gomega.Expect(entry.WaitingOn.Since).ToNot(gomega.BeEmpty())
	})

	ginkgo.It("gives a parked-until row its deadline", func() {
		sessionID := "123e4567-e89b-12d3-a456-426614174000"
		deadline := now.Add(time.Hour)
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "p", Branch: "create",
			Liveness: "ABSENT", SessionID: sessionID,
		}
		tick := newTickWithParkedGate(
			[]FlaggedRow{row},
			map[string]string{sessionID: "⏰ Ends: " + deadline.Format(time.RFC3339)},
			nil, &recordingRunner{}, fixedNow(now),
		)

		entry := only(tick)

		gomega.Expect(entry.Reason).To(gomega.Equal(string(ParkedUntil)))
		gomega.Expect(entry.WaitingOn).ToNot(gomega.BeNil())
		gomega.Expect(entry.WaitingOn.Until).To(gomega.Equal(deadline.Format(time.RFC3339)))
		gomega.Expect(entry.WaitingOn.Summary).To(gomega.Equal(
			"parked until " + deadline.Format(time.RFC3339)))
	})

	ginkgo.It("gives a parked-on-pr row its pull request url", func() {
		sessionID := "123e4567-e89b-12d3-a456-426614174001"
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "p", Branch: "create",
			Liveness: "ABSENT", SessionID: sessionID,
		}
		tick := newTickWithParkedGate(
			[]FlaggedRow{row},
			map[string]string{sessionID: "⏰ Ends: bborbe/claude-wrapper#412 merges"},
			nil,
			&recordingRunner{out: []byte(`{"headRefOid":"abc","reviewDecision":"","state":"OPEN"}`)},
			fixedNow(now),
		)

		entry := only(tick)

		gomega.Expect(entry.Reason).To(gomega.Equal(string(ParkedOnPR)))
		gomega.Expect(entry.WaitingOn).ToNot(gomega.BeNil())
		gomega.Expect(entry.WaitingOn.PRURL).To(
			gomega.Equal("https://github.com/bborbe/claude-wrapper/pull/412"))
	})

	ginkgo.It("names the live session of a not-absent row", func() {
		sessionID := "11111111-1111-1111-1111-111111111111"
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "n", Branch: "resume",
			Liveness: "LIVE", SessionID: sessionID,
		}
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, nil, nil)
		tick.Now = fixedNow(now)

		entry := only(tick)

		gomega.Expect(entry.Reason).To(gomega.Equal(reasonNotAbsent))
		gomega.Expect(entry.WaitingOn).ToNot(gomega.BeNil())
		gomega.Expect(entry.WaitingOn.SessionID).To(gomega.Equal(sessionID))
		gomega.Expect(entry.WaitingOn.Summary).To(gomega.Equal("session 11111111 still live"))
	})

	ginkgo.It("never calls an UNKNOWN-liveness not-absent session live", func() {
		row := FlaggedRow{SessionID: "33333333-3333-3333-3333-333333333333", Liveness: "UNKNOWN"}

		waiting := notAbsentWaitingOn(row, reasonNotAbsent)

		gomega.Expect(waiting).ToNot(gomega.BeNil())
		gomega.Expect(waiting.Summary).To(gomega.Equal(
			"session 33333333 liveness unknown; absence not yet proven"))
	})

	ginkgo.It("summarises a flag-provenance-unrecorded refusal despite its suffix", func() {
		tick := newTick(staticSource{}, nil, nil)
		reason := RefusalProvenanceUnrecorded + ": no actor recorded; clear it with vault-cli task set 'x' flag true --by operator"

		waiting := tick.waitingOn(ctx, FlaggedRow{Name: "x"}, reason, nil, now)

		gomega.Expect(waiting).ToNot(gomega.BeNil())
		gomega.Expect(waiting.Summary).To(gomega.Equal(
			"flag provenance is not recorded; nobody attributed the flag"))
	})

	ginkgo.It("never reports a recently-spawned start outside the hold window", func() {
		row := FlaggedRow{SessionID: "44444444-4444-4444-4444-444444444444", Name: "r"}
		recent := map[string]time.Time{row.SessionID: now.Add(-recentlySpawnedWindow - time.Minute)}

		waiting := recentlySpawnedWaitingOn(row, recent, now)

		gomega.Expect(waiting.Since).To(gomega.BeEmpty())
	})

	ginkgo.It("never calls a not-absent-dead session live", func() {
		row := FlaggedRow{SessionID: "22222222-2222-2222-2222-222222222222"}

		waiting := notAbsentWaitingOn(row, reasonNotAbsentDead)

		gomega.Expect(waiting).ToNot(gomega.BeNil())
		gomega.Expect(waiting.SessionID).To(gomega.Equal(row.SessionID))
		gomega.Expect(waiting.Summary).ToNot(gomega.ContainSubstring("live"))
		gomega.Expect(waiting.Summary).To(gomega.Equal(
			"session 22222222 ended per ledger; absence not yet proven"))
	})

	ginkgo.It("gives a foreign-vault row a summary and omits waiting_on on an open row", func() {
		foreign := FlaggedRow{
			Kind: "task", Vault: "seibert-brogrammers", Name: "f", Branch: "create", Liveness: "none",
		}
		tick := newTick(staticSource{rows: []FlaggedRow{foreign}}, nil, nil)
		tick.OwnVault = DaemonVault{Name: "private-personal"}
		tick.Now = fixedNow(now)
		entry := only(tick)
		gomega.Expect(entry.Reason).To(gomega.Equal(reasonForeignVault))
		gomega.Expect(entry.WaitingOn).ToNot(gomega.BeNil())
		gomega.Expect(entry.WaitingOn.Summary).ToNot(gomega.BeEmpty())

		openRow := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "o", Branch: "create", Liveness: "none",
		}
		openTick := newTick(staticSource{rows: []FlaggedRow{openRow}}, nil, nil)
		openTick.Now = fixedNow(now)
		openEntry := only(openTick)
		gomega.Expect(openEntry.Decision).To(gomega.Equal(OpenableDecisionOpen))
		gomega.Expect(openEntry.WaitingOn).To(gomega.BeNil())
		body, err := json.Marshal(openEntry)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(string(body)).ToNot(gomega.ContainSubstring("waiting_on"))
	})

	ginkgo.It("omits empty optional fields from the JSON", func() {
		body, err := json.Marshal(OpenableWaitingOn{Summary: "x", Until: "2026-11-27T13:00:00Z"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(string(body)).To(gomega.Equal(`{"summary":"x","until":"2026-11-27T13:00:00Z"}`))
	})
})
