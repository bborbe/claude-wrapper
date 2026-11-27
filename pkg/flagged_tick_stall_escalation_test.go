// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The stall escalation exists because `progress-stalled` was a silent terminal
// hold: the row was skipped every tick and named nowhere the operator reads, so a
// worker that ended on an uncarded gate stayed invisible until someone found it by
// hand — 25 h in the 2026-11-26 case.
var _ = ginkgo.Describe("flagged tick stall escalation", func() {
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	stalledRow := func(hash string) FlaggedRow {
		return FlaggedRow{
			Kind:         "task",
			Vault:        "private-personal",
			Name:         "A Worker Exits on an Uncarded Operator Gate",
			Phase:        "execution",
			Met:          "0/3",
			ProgressHash: hash,
			ProgressTail: "2026-11-25 10:22 — held for the operator's explicit go-ahead",
		}
	}

	harness := func() (*FlaggedTick, *recordingPoster) {
		poster := &recordingPoster{}
		return &FlaggedTick{
			CacheDir: ginkgo.GinkgoT().TempDir(),
			Poster:   poster,
			Cards:    &postedCardLookup{poster: poster},
			Logf:     func(string, ...any) {},
		}, poster
	}

	ginkgo.It("posts exactly one card for one stall episode", func() {
		tick, poster := harness()
		row := stalledRow("h1")
		ctx := context.Background()

		// First sight starts the clock and holds nothing.
		_, held := tick.progressStalledHold(ctx, row, base)
		gomega.Expect(held).To(gomega.BeFalse())
		gomega.Expect(poster.requests).To(gomega.BeEmpty())

		// Crossing the threshold posts the card.
		_, held = tick.progressStalledHold(ctx, row, base.Add(progressStallThreshold))
		gomega.Expect(held).To(gomega.BeTrue())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))

		// Every later tick of the SAME episode posts nothing more. This is the
		// assertion a per-tick poster fails: five more ticks, still one card.
		for i := 1; i <= 5; i++ {
			_, held = tick.progressStalledHold(
				ctx, row, base.Add(progressStallThreshold+time.Duration(i)*time.Minute),
			)
			gomega.Expect(held).To(gomega.BeTrue())
		}
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("posts a second card when the row moves and stalls again", func() {
		tick, poster := harness()
		ctx := context.Background()

		first := stalledRow("h1")
		gomega.Expect(mustHold(tick, ctx, first, base)).To(gomega.BeFalse())
		gomega.Expect(
			mustHold(tick, ctx, first, base.Add(progressStallThreshold)),
		).To(gomega.BeTrue())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))

		// A changed Progress hash is a new episode: the clock restarts, then stalls.
		second := stalledRow("h2")
		movedAt := base.Add(2 * time.Hour)
		gomega.Expect(mustHold(tick, ctx, second, movedAt)).To(gomega.BeFalse())
		gomega.Expect(
			mustHold(tick, ctx, second, movedAt.Add(progressStallThreshold)),
		).To(gomega.BeTrue())
		gomega.Expect(poster.requests).To(gomega.HaveLen(2))
		gomega.Expect(poster.requests[0].DedupKey).
			NotTo(gomega.Equal(poster.requests[1].DedupKey))
	})

	ginkgo.It("names the row and quotes its last progress line", func() {
		tick, poster := harness()
		ctx := context.Background()
		row := stalledRow("h1")

		gomega.Expect(mustHold(tick, ctx, row, base)).To(gomega.BeFalse())
		gomega.Expect(
			mustHold(tick, ctx, row, base.Add(progressStallThreshold)),
		).To(gomega.BeTrue())

		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
		payload := poster.requests[0].Payload
		gomega.Expect(payload).To(gomega.ContainSubstring(row.Name))
		gomega.Expect(payload).To(gomega.ContainSubstring(row.ProgressTail))
		gomega.Expect(poster.requests[0].DedupKey).To(gomega.ContainSubstring(row.ProgressHash))
	})

	ginkgo.It("asks the board once per episode, not once per tick", func() {
		// The store read sits inside the predicate's ReadTimeout budget, so a row
		// that stays stalled must not pay for it every tick. The in-process memory
		// answers the later ticks; the board is still what decides after a restart,
		// which the previous spec covers.
		poster := &recordingPoster{}
		lookup := &countingLookup{inner: &postedCardLookup{poster: poster}}
		tick := &FlaggedTick{
			CacheDir: ginkgo.GinkgoT().TempDir(),
			Poster:   poster,
			Cards:    lookup,
			Logf:     func(string, ...any) {},
		}
		row := stalledRow("h1")
		ctx := context.Background()

		gomega.Expect(mustHold(tick, ctx, row, base)).To(gomega.BeFalse())
		for i := 0; i <= 5; i++ {
			gomega.Expect(
				mustHold(tick, ctx, row, base.Add(progressStallThreshold+time.Duration(i)*time.Minute)),
			).To(gomega.BeTrue())
		}

		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
		gomega.Expect(lookup.calls).To(gomega.Equal(1))
	})

	ginkgo.It("keeps the hold but posts nothing when the escalation is unwired", func() {
		// A daemon with no attention URL, or a fixture predating this change, gets
		// the old behaviour: the row is still held, and nothing is posted. The hold
		// must never depend on the board being reachable.
		tick := &FlaggedTick{
			CacheDir: ginkgo.GinkgoT().TempDir(),
			Logf:     func(string, ...any) {},
		}
		row := stalledRow("h1")
		ctx := context.Background()

		gomega.Expect(mustHold(tick, ctx, row, base)).To(gomega.BeFalse())
		gomega.Expect(
			mustHold(tick, ctx, row, base.Add(progressStallThreshold)),
		).To(gomega.BeTrue())
	})
})

// The regression the shipped fix missed, and why the old fake could not catch it.
//
// `postedCardLookup` returns the posted id regardless of state, so it modelled an
// always-open card and never exercised the real question. The store's open route returns
// OPEN items only: once the operator answers, the card closes, the lookup finds nothing,
// and the next post after a restart is a duplicate. Measured 2026-11-26 — two cards under
// one dedup key, 23 minutes apart, the second after the daemon restarted.
var _ = ginkgo.Describe("stall escalation against card history", func() {
	base := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)

	stalledRow := func(hash string) FlaggedRow {
		return FlaggedRow{
			Kind:         "task",
			Vault:        "private-personal",
			Name:         "A Worker Exits on an Uncarded Operator Gate",
			Phase:        "execution",
			Met:          "0/3",
			ProgressHash: hash,
		}
	}

	ginkgo.It("finds a CLOSED card, so a restart cannot re-post the episode", func() {
		var sawPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawPath = r.URL.Path
			_, _ = w.Write([]byte(`{"items":[{"item_id":"closed-card","state":"closed",` +
				`"producer_id":"claude-wrapper-stalled-row","dedup_key":"k1"}]}`))
		}))
		defer server.Close()

		id, err := NewBoardCardHistory(server.URL, nil).ExistingCard(
			context.Background(), "claude-wrapper-stalled-row", "k1",
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(id).To(gomega.Equal("closed-card"))
		// The open route cannot answer this question; the history route can.
		gomega.Expect(sawPath).To(gomega.Equal(AttentionHistoryPath))
	})

	ginkgo.It("reads the store's bare-array body", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The store answers a bare JSON array, not {"items":[…]}. Decoding only
			// the envelope made every read fail with `cannot unmarshal array into Go
			// value of type struct { Items []BoardItem }`, so the stall escalation
			// logged outcome=lookup-failed and posted nothing.
			_, _ = w.Write([]byte(`[{"item_id":"bare-card","state":"closed",` +
				`"producer_id":"claude-wrapper-stalled-row","dedup_key":"k1"}]`))
		}))
		defer server.Close()

		id, err := NewBoardCardHistory(server.URL, nil).ExistingCard(
			context.Background(), "claude-wrapper-stalled-row", "k1",
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(id).To(gomega.Equal("bare-card"))
	})

	ginkgo.It("reads a bare array that holds no matching card", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[{"item_id":"other","state":"closed",` +
				`"producer_id":"someone-else","dedup_key":"k1"}]`))
		}))
		defer server.Close()

		id, err := NewBoardCardHistory(server.URL, nil).ExistingCard(
			context.Background(), "claude-wrapper-stalled-row", "k1",
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(id).To(gomega.BeEmpty())
	})

	ginkgo.It("ignores another producer's card under the same key", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"items":[{"item_id":"other","state":"closed",` +
				`"producer_id":"someone-else","dedup_key":"k1"}]}`))
		}))
		defer server.Close()

		id, err := NewBoardCardHistory(server.URL, nil).ExistingCard(
			context.Background(), "claude-wrapper-stalled-row", "k1",
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(id).To(gomega.BeEmpty())
	})

	ginkgo.It("fails closed on a store error rather than reporting no card", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		_, err := NewBoardCardHistory(server.URL, nil).ExistingCard(
			context.Background(), "claude-wrapper-stalled-row", "k1",
		)
		// An unreadable store is not "no card exists" — treating the two alike is how the
		// duplicate got posted in the first place.
		gomega.Expect(err).NotTo(gomega.BeNil())
	})

	ginkgo.It("does not post when the episode's card is already closed", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"items":[{"item_id":"closed-card","state":"closed",` +
				`"producer_id":"claude-wrapper-stalled-row","dedup_key":"` +
				stallEscalationDedupKey(stalledRow("h1")) + `"}]}`))
		}))
		defer server.Close()

		poster := &recordingPoster{}
		tick := &FlaggedTick{
			CacheDir: ginkgo.GinkgoT().TempDir(),
			Poster:   poster,
			Cards:    NewBoardCardHistory(server.URL, nil),
			Logf:     func(string, ...any) {},
		}
		row := stalledRow("h1")
		ctx := context.Background()

		gomega.Expect(mustHold(tick, ctx, row, base)).To(gomega.BeFalse())
		gomega.Expect(
			mustHold(tick, ctx, row, base.Add(progressStallThreshold)),
		).To(gomega.BeTrue())
		gomega.Expect(poster.requests).To(gomega.BeEmpty())
	})
})

// countingLookup wraps the shared fake and counts how often the board was asked.
type countingLookup struct {
	inner CardLookup
	calls int
}

func (l *countingLookup) ExistingCard(
	ctx context.Context,
	producerID string,
	dedupKey string,
) (string, error) {
	l.calls++
	return l.inner.ExistingCard(ctx, producerID, dedupKey)
}

func mustHold(tick *FlaggedTick, ctx context.Context, row FlaggedRow, now time.Time) bool {
	_, held := tick.progressStalledHold(ctx, row, now)
	return held
}
