// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("IdleReaperJob", func() {
	age := func(seconds int) *int { return &seconds }

	ginkgo.Describe("idleReapSkipReason", func() {
		floor := DefaultIdleFloor

		ginkgo.DescribeTable("names why a session is or is not reaped",
			func(session Session, expected string) {
				gomega.Expect(idleReapSkipReason(session, floor)).To(gomega.Equal(expected))
			},
			ginkgo.Entry("an interactive tab is never reaped",
				Session{Mode: SpawnModeInteractive, State: StateIdle, LastEventAgeSeconds: age(3600)},
				"not-headless"),
			ginkgo.Entry("a generating child is not idle",
				Session{Mode: SpawnModeHeadless, State: StateGenerating, LastEventAgeSeconds: age(3600)},
				"not-idle"),
			ginkgo.Entry("a child with a turn on its way is skipped",
				Session{Mode: SpawnModeHeadless, State: StateIdle, PendingTurns: 1, LastEventAgeSeconds: age(3600)},
				"turn-pending"),
			ginkgo.Entry("an exited child is not reaped twice",
				Session{Mode: SpawnModeHeadless, State: StateIdle, ExitCode: age(0), LastEventAgeSeconds: age(3600)},
				"exited"),
			ginkgo.Entry("a record with no age cannot be aged",
				Session{Mode: SpawnModeHeadless, State: StateIdle},
				"no-age"),
			ginkgo.Entry("an idle child under the floor is left alone",
				Session{Mode: SpawnModeHeadless, State: StateIdle, LastEventAgeSeconds: age(60)},
				"under-floor"),
			ginkgo.Entry("an idle headless child past the floor is reaped",
				Session{Mode: SpawnModeHeadless, State: StateIdle, LastEventAgeSeconds: age(901)},
				""),
		)
	})

	ginkgo.Describe("idleAge", func() {
		ginkgo.It("reports the silent age in seconds", func() {
			gomega.Expect(idleAge(Session{LastEventAgeSeconds: age(900)})).To(gomega.Equal(15 * time.Minute))
		})

		ginkgo.It("reports zero when the age is absent", func() {
			gomega.Expect(idleAge(Session{})).To(gomega.Equal(time.Duration(0)))
		})
	})
})

var _ = ginkgo.Describe("JobLine", func() {
	ginkgo.It("renders job and action with no pairs", func() {
		gomega.Expect(JobLine("idle-reaper", "tick")).To(gomega.Equal("job=idle-reaper action=tick"))
	})

	ginkgo.It("appends key/value pairs in order", func() {
		gomega.Expect(JobLine("idle-reaper", "closed", "sid", "abc", "idle", "16m0s")).
			To(gomega.Equal("job=idle-reaper action=closed sid=abc idle=16m0s"))
	})

	ginkgo.It("ignores a dangling key with no value", func() {
		gomega.Expect(JobLine("x", "y", "orphan")).To(gomega.Equal("job=x action=y"))
	})
})

var _ = ginkgo.Describe("RunTick", func() {
	ginkgo.It("runs once immediately, before the first interval elapses", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ran := make(chan struct{}, 4)
		go func() {
			_ = RunTick(ctx, time.Hour, func(context.Context) {
				select {
				case ran <- struct{}{}:
				default:
				}
				cancel()
			})
		}()
		gomega.Eventually(ran).Should(gomega.Receive())
	})

	ginkgo.It("returns the context error when cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := RunTick(ctx, time.Hour, func(context.Context) {})
		gomega.Expect(err).To(gomega.MatchError(context.Canceled))
	})
})

var _ = ginkgo.Describe("RunTickWithGap", func() {
	base := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	// The interval only paces the loop; the gap is scripted on the clock, never
	// waited for. A realistic interval here would make the test sleep for it —
	// the ticker, not the clock, is what the loop blocks on between ticks.
	interval := time.Millisecond

	// drive runs the loop once per scripted start and returns what it reported.
	// The clock is scripted rather than slept through: a gap is a wall-clock
	// fact, and a test that waited 20 minutes for one would not run.
	drive := func(starts []time.Time) []time.Duration {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		now := func() time.Time {
			if calls >= len(starts) {
				return starts[len(starts)-1]
			}
			start := starts[calls]
			calls++
			return start
		}
		reported := make([]time.Duration, 0, 1)
		ticks := 0
		_ = RunTickWithGap(ctx, interval, now,
			func(gap time.Duration, _ time.Time) { reported = append(reported, gap) },
			func(context.Context) {
				ticks++
				if ticks == len(starts) {
					cancel()
				}
			})
		return reported
	}

	ginkgo.It("does not report a tick that arrives on its interval", func() {
		gomega.Expect(drive([]time.Time{base, base.Add(interval), base.Add(2 * interval)})).To(gomega.BeEmpty())
	})

	ginkgo.It("does not report a tick exactly at the tolerance", func() {
		gomega.Expect(drive([]time.Time{base, base.Add(interval + interval/2)})).To(gomega.BeEmpty())
	})

	ginkgo.It("reports the gap when a tick arrives past the tolerance", func() {
		gomega.Expect(drive([]time.Time{base, base.Add(2 * interval)})).To(gomega.Equal([]time.Duration{2 * interval}))
	})

	ginkgo.It("reports the tick the gap is measured from", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		now := func() time.Time {
			calls++
			if calls == 1 {
				return base
			}
			return base.Add(3 * interval)
		}
		var since time.Time
		ticks := 0
		_ = RunTickWithGap(ctx, interval, now,
			func(_ time.Duration, from time.Time) { since = from },
			func(context.Context) {
				ticks++
				if ticks == 2 {
					cancel()
				}
			})
		gomega.Expect(since).To(gomega.Equal(base))
	})

	ginkgo.It("never reports the first tick", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reported := 0
		_ = RunTickWithGap(ctx, interval, func() time.Time { return base },
			func(time.Duration, time.Time) { reported++ },
			func(context.Context) { cancel() })
		gomega.Expect(reported).To(gomega.Equal(0))
	})
})

var _ = ginkgo.Describe("IdleReaperJob.reportLate", func() {
	ginkgo.It("renders the tick-late line with the measured gap", func() {
		var lines []string
		job := &IdleReaperJob{Logf: func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
		}}
		job.reportLate(20*time.Minute, time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
		gomega.Expect(lines).To(gomega.Equal([]string{
			"job=idle-reaper action=tick-late gap=20m0s since=2026-11-14T12:00:00Z",
		}))
	})
})
