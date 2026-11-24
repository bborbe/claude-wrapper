// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("flagged row cache persistence", func() {
	ginkgo.Describe("save and load", func() {
		ginkgo.It("round-trips the row set and its read time", func() {
			dir := ginkgo.GinkgoT().TempDir()
			readAt := time.Date(2026, 11, 19, 13, 18, 17, 0, time.UTC)
			rows := []FlaggedRow{rowWith("agent", "headless", "Task A")}

			gomega.Expect(saveFlaggedCache(context.Background(), dir, rows, readAt)).
				To(gomega.Succeed())

			got, gotAt, ok := loadFlaggedCache(dir)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(gotAt.Equal(readAt)).To(gomega.BeTrue())
			gomega.Expect(got).To(gomega.Equal(rows))
		})

		ginkgo.It("round-trips an empty row set, which is a real answer", func() {
			dir := ginkgo.GinkgoT().TempDir()
			readAt := time.Date(2026, 11, 19, 13, 18, 17, 0, time.UTC)

			gomega.Expect(saveFlaggedCache(context.Background(), dir, []FlaggedRow{}, readAt)).
				To(gomega.Succeed())

			got, _, ok := loadFlaggedCache(dir)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(got).To(gomega.BeEmpty())
		})

		ginkgo.It("round-trips a nil row set, which is what an empty read marshals to", func() {
			dir := ginkgo.GinkgoT().TempDir()
			readAt := time.Date(2026, 11, 19, 13, 18, 17, 0, time.UTC)

			gomega.Expect(saveFlaggedCache(context.Background(), dir, nil, readAt)).
				To(gomega.Succeed())

			got, _, ok := loadFlaggedCache(dir)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(got).To(gomega.BeEmpty())
		})

		ginkgo.It("treats a missing file as no cache", func() {
			_, _, ok := loadFlaggedCache(ginkgo.GinkgoT().TempDir())
			gomega.Expect(ok).To(gomega.BeFalse())
		})

		ginkgo.It("treats a corrupt file as no cache", func() {
			dir := ginkgo.GinkgoT().TempDir()
			gomega.Expect(os.WriteFile(flaggedCachePath(dir), []byte("{"), 0o600)).
				To(gomega.Succeed())

			_, _, ok := loadFlaggedCache(dir)
			gomega.Expect(ok).To(gomega.BeFalse())
		})

		ginkgo.It("treats an empty state dir as no cache", func() {
			_, _, ok := loadFlaggedCache("")
			gomega.Expect(ok).To(gomega.BeFalse())
		})
	})

	ginkgo.Describe("seeding the tick from disk", func() {
		readAt := time.Date(2026, 11, 19, 13, 18, 17, 0, time.UTC)
		rows := []FlaggedRow{rowWith("agent", "headless", "Task A")}

		ginkgo.It("serves a persisted set inside the staleness bound", func() {
			dir := ginkgo.GinkgoT().TempDir()
			gomega.Expect(saveFlaggedCache(context.Background(), dir, rows, readAt)).
				To(gomega.Succeed())
			tick := &FlaggedTick{Now: fixedNow(readAt.Add(5 * time.Minute)), Logf: func(string, ...any) {}}
			cache := &flaggedRowCache{}

			tick.seedCacheFromDisk(cache, dir, DefaultFlaggedTickInterval)

			got, _, ok, _ := cache.snapshot()
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(got).To(gomega.Equal(rows))
			at, fromDisk := cache.persistedAge()
			gomega.Expect(fromDisk).To(gomega.BeTrue())
			gomega.Expect(at.Equal(readAt)).To(gomega.BeTrue())
		})

		ginkgo.It("refuses a persisted set older than the bound", func() {
			dir := ginkgo.GinkgoT().TempDir()
			gomega.Expect(saveFlaggedCache(context.Background(), dir, rows, readAt)).
				To(gomega.Succeed())
			var lines []string
			tick := &FlaggedTick{
				Now:  fixedNow(readAt.Add(4 * time.Hour)),
				Logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
			}
			cache := &flaggedRowCache{}

			tick.seedCacheFromDisk(cache, dir, DefaultFlaggedTickInterval)

			_, _, ok, _ := cache.snapshot()
			gomega.Expect(ok).To(gomega.BeFalse())
			gomega.Expect(lines).To(gomega.ContainElement(
				gomega.ContainSubstring("cache refused")))
		})

		ginkgo.It("does not overwrite a row set a read already landed", func() {
			dir := ginkgo.GinkgoT().TempDir()
			gomega.Expect(saveFlaggedCache(context.Background(), dir, rows, readAt)).
				To(gomega.Succeed())
			tick := &FlaggedTick{Now: fixedNow(readAt.Add(5 * time.Minute)), Logf: func(string, ...any) {}}
			fresh := []FlaggedRow{rowWith("agent", "headless", "Fresh")}
			cache := &flaggedRowCache{}
			cache.store(fresh, nil, time.Second, readAt)

			tick.seedCacheFromDisk(cache, dir, DefaultFlaggedTickInterval)

			got, _, _, _ := cache.snapshot()
			gomega.Expect(got).To(gomega.Equal(fresh))
			_, fromDisk := cache.persistedAge()
			gomega.Expect(fromDisk).To(gomega.BeFalse())
		})
	})

	ginkgo.Describe("the first tick after a start", func() {
		ginkgo.It("serves the persisted set instead of logging skipped", func() {
			readAt := time.Date(2026, 11, 19, 13, 18, 17, 0, time.UTC)
			var mutex sync.Mutex
			var lines []string
			tick := &FlaggedTick{
				Reopener: &recordingReopener{},
				Now:      fixedNow(readAt.Add(30 * time.Second)),
				Logf: func(format string, args ...any) {
					mutex.Lock()
					defer mutex.Unlock()
					lines = append(lines, fmt.Sprintf(format, args...))
				},
			}
			cache := &flaggedRowCache{}
			cache.seedFromDisk([]FlaggedRow{}, readAt)

			tick.tickAndLog(context.Background(), cache)

			mutex.Lock()
			defer mutex.Unlock()
			gomega.Expect(lines).To(gomega.ContainElement(
				gomega.ContainSubstring("served persisted rows: 0 rows read 30s ago")))
			gomega.Expect(lines).NotTo(gomega.ContainElement(
				gomega.ContainSubstring("flagged-tick skipped")))
		})
	})
})

var _ = ginkgo.Describe("flagged row served-set age", func() {
	// The defect this covers (2026-11-24): a row flagged just after a refresh
	// read was absent from the tick that followed, and only reached a tick after
	// the NEXT read — up to two intervals. read_duration could not show it,
	// because it measures the read's cost, not the served set's age.
	base := time.Date(2026, 11, 24, 13, 18, 17, 0, time.UTC)

	ginkgo.It("reports rows_age= and tracks two different read ages", func() {
		cache := &flaggedRowCache{}
		cache.store([]FlaggedRow{}, nil, 0, base)

		var first []string
		firstTick := &FlaggedTick{
			Reopener: &recordingReopener{},
			Now:      fixedNow(base.Add(10 * time.Second)),
			Logf: func(format string, args ...any) {
				first = append(first, fmt.Sprintf(format, args...))
			},
		}
		firstTick.tickAndLog(context.Background(), cache)

		var second []string
		secondTick := &FlaggedTick{
			Reopener: &recordingReopener{},
			Now:      fixedNow(base.Add(5 * time.Minute)),
			Logf: func(format string, args ...any) {
				second = append(second, fmt.Sprintf(format, args...))
			},
		}
		secondTick.tickAndLog(context.Background(), cache)

		// Two different ages, so a hardcoded constant cannot satisfy this.
		gomega.Expect(strings.Join(first, "\n")).To(gomega.ContainSubstring("rows_age=10s"))
		gomega.Expect(strings.Join(second, "\n")).To(gomega.ContainSubstring("rows_age=5m0s"))
	})

	ginkgo.It("names a served set older than the bound", func() {
		cache := &flaggedRowCache{}
		cache.store([]FlaggedRow{}, nil, 0, base)
		var lines []string
		tick := &FlaggedTick{
			Reopener: &recordingReopener{},
			Interval: DefaultFlaggedTickInterval,
			Now:      fixedNow(base.Add(DefaultFlaggedTickInterval + 2*time.Minute)),
			Logf: func(format string, args ...any) {
				lines = append(lines, fmt.Sprintf(format, args...))
			},
		}

		tick.tickAndLog(context.Background(), cache)

		gomega.Expect(strings.Join(lines, "\n")).To(gomega.ContainSubstring("stale rows"))
	})

	ginkgo.It("does not name a served set inside the bound", func() {
		cache := &flaggedRowCache{}
		cache.store([]FlaggedRow{}, nil, 0, base)
		var lines []string
		tick := &FlaggedTick{
			Reopener: &recordingReopener{},
			Now:      fixedNow(base.Add(serveGrace)),
			Logf: func(format string, args ...any) {
				lines = append(lines, fmt.Sprintf(format, args...))
			},
		}

		tick.tickAndLog(context.Background(), cache)

		gomega.Expect(strings.Join(lines, "\n")).NotTo(gomega.ContainSubstring("stale rows"))
	})
})

var _ = ginkgo.Describe("phase-locking the refresh read", func() {
	ginkgo.It("sleeps to just before the next tick", func() {
		gomega.Expect(refreshWait(DefaultFlaggedTickInterval)).
			To(gomega.Equal(DefaultFlaggedTickInterval - serveGrace))
	})

	ginkgo.It("halves a short interval instead of going negative", func() {
		gomega.Expect(refreshWait(40 * time.Second)).To(gomega.Equal(20 * time.Second))
	})
})
