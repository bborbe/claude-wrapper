// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
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

	ginkgo.It("posts no attention card on a read-only call", func() {
		tick, poster := harness()
		row := stalledRow("h1")
		ctx := context.Background()

		// Stamp the clock on the tick's own path (first sight posts nothing).
		gomega.Expect(mustHold(tick, ctx, row, base)).To(gomega.BeFalse())
		poster.requests = nil

		// A read past the threshold reports the hold and posts nothing.
		_, held := tick.progressStalledHold(ctx, row, base.Add(progressStallThreshold), true)
		gomega.Expect(held).To(gomega.BeTrue())
		gomega.Expect(poster.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("posts exactly one card for one stall episode", func() {
		tick, poster := harness()
		row := stalledRow("h1")
		ctx := context.Background()

		// First sight starts the clock and holds nothing.
		_, held := tick.progressStalledHold(ctx, row, base, false)
		gomega.Expect(held).To(gomega.BeFalse())
		gomega.Expect(poster.requests).To(gomega.BeEmpty())

		// Crossing the threshold posts the card.
		_, held = tick.progressStalledHold(ctx, row, base.Add(progressStallThreshold), false)
		gomega.Expect(held).To(gomega.BeTrue())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))

		// Every later tick of the SAME episode posts nothing more. This is the
		// assertion a per-tick poster fails: five more ticks, still one card.
		for i := 1; i <= 5; i++ {
			_, held = tick.progressStalledHold(
				ctx, row, base.Add(progressStallThreshold+time.Duration(i)*time.Minute), false,
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
	_, held := tick.progressStalledHold(ctx, row, now, false)
	return held
}
