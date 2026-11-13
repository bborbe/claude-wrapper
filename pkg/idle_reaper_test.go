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
