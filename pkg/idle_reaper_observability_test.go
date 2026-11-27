// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// ⚠️ The entry point is `IdleCloseReaper.Reap` driven through `ReaperJob`, NOT
// `IdleReaperJob.TickAndLog`. That function already emitted this exact shape
// before the adapter existed, so a spec written against it is green on the
// pre-fix tree and proves nothing.
var _ = ginkgo.Describe("IdleCloseReaper observability", func() {
	ctx := context.Background()

	age := func(seconds int) *int { return &seconds }

	// drive runs one real tick through the adapter and the job that renders it,
	// over a fixture with one close and three skips: one notable (headless,
	// generating, past the floor — the day-17 shape), one interactive tab, one
	// headless child under the floor.
	drive := func() []string {
		sup := &idleSupervisor{sessions: []Session{
			{ID: "idle-1", Mode: SpawnModeHeadless, State: StateIdle, LastEventAgeSeconds: age(901)},
			{ID: "gen-1", Mode: SpawnModeHeadless, State: StateGenerating, LastEventAgeSeconds: age(2000)},
			{ID: "tab-1", Mode: SpawnModeInteractive, State: StateIdle, LastEventAgeSeconds: age(3600)},
			{ID: "young-1", Mode: SpawnModeHeadless, State: StateIdle, LastEventAgeSeconds: age(60)},
		}}
		lines := []string{}
		logf := func(line string) { lines = append(lines, line) }
		idle := &IdleReaperJob{Supervisor: sup, Logf: func(string, ...any) {}}
		job := &ReaperJob{
			Reapers: []Reaper{&IdleCloseReaper{Job: idle, Logf: logf}},
			Logf:    logf,
		}

		job.TickAndLog(ctx)

		return lines
	}

	ginkgo.It("emits one tick summary with the skipped count and per-reason breakdown", func() {
		gomega.Expect(drive()).To(gomega.ContainElement(
			"job=idle-reaper action=tick closed=1 skipped=3 reasons=not-headless:1,not-idle:1,under-floor:1"))
	})

	ginkgo.It("names a skipped session with its reason, and keeps the closed result", func() {
		lines := drive()

		gomega.Expect(lines).To(gomega.ContainElement(
			"job=reaper action=idle-close result=kept id=gen-1 reason=not-idle"))
		gomega.Expect(lines).To(gomega.ContainElement(
			"job=reaper action=idle-close result=closed id=idle-1"))
	})

	// A benign skip is counted in the summary and nothing more: one line per
	// interactive tab or under-floor child every tick would drown the log.
	ginkgo.It("does not log a per-session line for a benign skip", func() {
		for _, line := range drive() {
			gomega.Expect(line).NotTo(gomega.ContainSubstring("id=tab-1"))
			gomega.Expect(line).NotTo(gomega.ContainSubstring("id=young-1"))
		}
	})
})
