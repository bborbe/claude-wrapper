// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/sweepgate"
)

var _ = ginkgo.Describe("progress stall predicate", func() {
	base := time.Date(2026, 11, 25, 22, 0, 0, 0, time.UTC)

	ginkgo.Describe("progressStallTracker", func() {
		ginkgo.It("StalledRead reports a stalled row without writing the clock", func() {
			dir := ginkgo.GinkgoT().TempDir()
			seedProgressStamp(dir, "k", "h1", base)
			path := filepath.Join(dir, progressStallFileName)
			before, err := os.ReadFile(path)
			gomega.Expect(err).To(gomega.BeNil())

			tracker := newProgressStallTracker(dir)
			gomega.Expect(tracker.StalledRead("k", "h1", base.Add(31*time.Minute))).To(gomega.BeTrue())
			// A never-seen key must not be stamped either.
			gomega.Expect(tracker.StalledRead("new", "h9", base)).To(gomega.BeFalse())

			after, err := os.ReadFile(path)
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(after).To(gomega.Equal(before))
		})
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

		ginkgo.It("re-stamps a zero Since instead of treating it as ancient", func() {
			dir := ginkgo.GinkgoT().TempDir()
			// A hand-edited or older-schema file: the stamp exists but carries no
			// instant. Reading it as "since forever" would skip a live row.
			gomega.Expect(writeProgressStamps(dir, map[string]progressStamp{
				"row": {Hash: "h1"},
			})).To(gomega.Succeed())

			tracker := newProgressStallTracker(dir)
			gomega.Expect(tracker.Stalled("row", "h1", base)).To(gomega.BeFalse())
		})

		ginkgo.It("survives a null state file without panicking", func() {
			dir := ginkgo.GinkgoT().TempDir()
			gomega.Expect(writeProgressRaw(dir, []byte("null"))).To(gomega.Succeed())

			tracker := newProgressStallTracker(dir)
			gomega.Expect(tracker.Stalled("row", "h1", base)).To(gomega.BeFalse())
			gomega.Expect(tracker.Stalled("row", "h1", base.Add(progressStallThreshold))).
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

			reason, held := tick.progressStalledHold(ctx, stalledRow, base, false)
			gomega.Expect(held).To(gomega.BeFalse())
			gomega.Expect(reason).To(gomega.BeEmpty())

			reason, held = tick.progressStalledHold(ctx, stalledRow, base.Add(31*time.Minute), false)
			gomega.Expect(held).To(gomega.BeTrue())
			gomega.Expect(reason).To(gomega.Equal(reasonProgressStalled))
		})

		// The negative cases seed the clock for the exact key and signal the row
		// would use, so deleting the clause under test makes the row stall and the
		// spec fail. A bare call at `base` would pass on first sight regardless.
		ginkgo.It("does not skip a row outside execution", func() {
			dir := ginkgo.GinkgoT().TempDir()
			seedProgressStamp(dir, stalledRow.Key(), "h1|1/3", base)

			tick := &FlaggedTick{CacheDir: dir}
			row := stalledRow
			row.Phase = "planning"
			_, held := tick.progressStalledHold(context.Background(), row, base.Add(31*time.Minute), false)
			gomega.Expect(held).To(gomega.BeFalse())
		})

		ginkgo.It("does not skip a row with no open box", func() {
			dir := ginkgo.GinkgoT().TempDir()
			seedProgressStamp(dir, stalledRow.Key(), "h1|3/3", base)

			tick := &FlaggedTick{CacheDir: dir}
			row := stalledRow
			row.Met = "3/3"
			_, held := tick.progressStalledHold(context.Background(), row, base.Add(31*time.Minute), false)
			gomega.Expect(held).To(gomega.BeFalse())
		})

		ginkgo.It("does not skip a goal or topic row", func() {
			dir := ginkgo.GinkgoT().TempDir()
			goalRow := stalledRow
			goalRow.Kind = "goal"
			seedProgressStamp(dir, goalRow.Key(), "h1|1/3", base)

			tick := &FlaggedTick{CacheDir: dir}
			_, held := tick.progressStalledHold(
				context.Background(), goalRow, base.Add(31*time.Minute), false,
			)
			gomega.Expect(held).To(gomega.BeFalse())
		})
	})

	ginkgo.Describe("pageStallFields", func() {
		ginkgo.It("reads phase, the checkbox reading and the progress hash from the body", func() {
			body := "# Success Criteria\n\n- [ ] open\n- [x] done\n\n# Progress\n\n- wrote something\n"
			page := VaultAPIPage{
				Frontmatter: map[string]any{"phase": "execution"},
				Body:        body,
			}
			hash, tail, met, phase := pageStallFields(page)
			gomega.Expect(phase).To(gomega.Equal("execution"))
			gomega.Expect(met).To(gomega.Equal("1/2"))
			gomega.Expect(hash).To(gomega.Equal(sweepgate.ProgressHash(body)))
			gomega.Expect(tail).To(gomega.Equal("- wrote something"))
		})
	})
})

// seedProgressStamp writes one row's clock into a tracker's state file, so a
// spec can start the clock without waiting the threshold.
func seedProgressStamp(dir, key, hash string, since time.Time) {
	gomega.Expect(writeProgressStamps(dir, map[string]progressStamp{
		key: {Hash: hash, Since: since},
	})).To(gomega.Succeed())
}

func writeProgressStamps(dir string, stamps map[string]progressStamp) error {
	body, err := json.Marshal(stamps)
	if err != nil {
		return err
	}
	return writeProgressRaw(dir, body)
}

func writeProgressRaw(dir string, body []byte) error {
	return os.WriteFile(filepath.Join(dir, progressStallFileName), body, 0o600)
}
