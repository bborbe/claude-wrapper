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

var _ = ginkgo.Describe("progress stall predicate", func() {
	base := time.Date(2026, 11, 25, 22, 0, 0, 0, time.UTC)

	ginkgo.Describe("progressStallTracker", func() {
		ginkgo.It("does not stall on first sight", func() {
			tracker := newProgressStallTracker(ginkgo.GinkgoT().TempDir())
			gomega.Expect(tracker.Stalled("row", "h1", base)).To(gomega.BeFalse())
		})

		ginkgo.It("stalls once the hash is unchanged for the threshold", func() {
			tracker := newProgressStallTracker(ginkgo.GinkgoT().TempDir())
			gomega.Expect(tracker.Stalled("row", "h1", base)).To(gomega.BeFalse())
			gomega.Expect(tracker.Stalled("row", "h1", base.Add(progressStallThreshold))).
				To(gomega.BeTrue())
		})

		ginkgo.It("does not stall just under the threshold", func() {
			tracker := newProgressStallTracker(ginkgo.GinkgoT().TempDir())
			gomega.Expect(tracker.Stalled("row", "h1", base)).To(gomega.BeFalse())
			gomega.Expect(tracker.Stalled("row", "h1", base.Add(progressStallThreshold-time.Minute))).
				To(gomega.BeFalse())
		})

		ginkgo.It("restarts the clock when the hash changes", func() {
			tracker := newProgressStallTracker(ginkgo.GinkgoT().TempDir())
			gomega.Expect(tracker.Stalled("row", "h1", base)).To(gomega.BeFalse())
			moved := base.Add(10 * time.Minute)
			gomega.Expect(tracker.Stalled("row", "h2", moved)).To(gomega.BeFalse())
			gomega.Expect(tracker.Stalled("row", "h2", moved.Add(progressStallThreshold))).
				To(gomega.BeTrue())
		})

		ginkgo.It("resumes the clock across a restart", func() {
			dir := ginkgo.GinkgoT().TempDir()
			first := newProgressStallTracker(dir)
			gomega.Expect(first.Stalled("row", "h1", base)).To(gomega.BeFalse())

			second := newProgressStallTracker(dir)
			gomega.Expect(second.Stalled("row", "h1", base.Add(progressStallThreshold))).
				To(gomega.BeTrue())
		})
	})

	ginkgo.Describe("progressStalledHold", func() {
		stalledRow := FlaggedRow{
			Kind:         "task",
			Vault:        "private-personal",
			Name:         "A waiting task",
			Phase:        "execution",
			Met:          "1/3",
			ProgressHash: "h1",
		}

		ginkgo.It("skips a stalled execution row with open boxes", func() {
			tick := &FlaggedTick{CacheDir: ginkgo.GinkgoT().TempDir()}
			ctx := context.Background()

			reason, held := tick.progressStalledHold(ctx, stalledRow, base)
			gomega.Expect(held).To(gomega.BeFalse())
			gomega.Expect(reason).To(gomega.BeEmpty())

			reason, held = tick.progressStalledHold(ctx, stalledRow, base.Add(31*time.Minute))
			gomega.Expect(held).To(gomega.BeTrue())
			gomega.Expect(reason).To(gomega.Equal(reasonProgressStalled))
		})

		ginkgo.It("does not skip a row outside execution", func() {
			tick := &FlaggedTick{CacheDir: ginkgo.GinkgoT().TempDir()}
			row := stalledRow
			row.Phase = "planning"
			_, held := tick.progressStalledHold(context.Background(), row, base)
			gomega.Expect(held).To(gomega.BeFalse())
		})

		ginkgo.It("does not skip a row with no open box", func() {
			tick := &FlaggedTick{CacheDir: ginkgo.GinkgoT().TempDir()}
			row := stalledRow
			row.Met = "3/3"
			_, held := tick.progressStalledHold(context.Background(), row, base)
			gomega.Expect(held).To(gomega.BeFalse())
		})

		ginkgo.It("does not skip a goal or topic row", func() {
			tick := &FlaggedTick{CacheDir: ginkgo.GinkgoT().TempDir()}
			row := stalledRow
			row.Kind = "goal"
			_, held := tick.progressStalledHold(context.Background(), row, base)
			gomega.Expect(held).To(gomega.BeFalse())
		})
	})
})
