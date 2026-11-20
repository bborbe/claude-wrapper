// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("keepalive output parsers", func() {
	ctx := context.Background()

	ginkgo.Describe("ParseTopicKeepaliveVerdict", func() {
		// The fixture lines are the script's own vocabulary, copied from its
		// docstring — a word it never prints would test a contract no run meets.
		ginkgo.DescribeTable("reads the first non-empty line as the verdict",
			func(output, want string) {
				got, err := ParseTopicKeepaliveVerdict(ctx, output)
				gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
				gomega.Expect(got).To(gomega.Equal(want))
			},
			ginkgo.Entry("resume", "resume\n", "resume"),
			ginkgo.Entry("restart", "restart\n", "restart"),
			ginkgo.Entry("crash-loop", "crash-loop\n", "crash-loop"),
			ginkgo.Entry("claim-taken", "claim-taken\n", "claim-taken"),
			ginkgo.Entry("leading blank line", "\n\nleave\n", "leave"),
		)

		ginkgo.It("refuses output with no verdict", func() {
			_, err := ParseTopicKeepaliveVerdict(ctx, "\n\n")
			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		// A two-token first line is prose, not a verdict — accepting it would make
		// the mapping fall through to an unknown word at act time.
		ginkgo.It("refuses a first line that is not one token", func() {
			_, err := ParseTopicKeepaliveVerdict(ctx, "the probe could not run\n")
			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})

	ginkgo.Describe("ParseParkNudges", func() {
		ginkgo.It("reads the session id, roster name and verbatim message", func() {
			output := "NUDGE abc123 quiet 42m (worker-a) :: still waiting on the PR\n"

			nudges, err := ParseParkNudges(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(nudges).To(gomega.Equal([]ParkNudge{{
				SessionID: "abc123",
				Name:      "worker-a",
				Message:   "still waiting on the PR",
			}}))
		})

		// ⚠️ Only the FIRST `::` separates. A message quoting one must survive
		// intact, or the operator reads a truncated instruction.
		ginkgo.It("keeps a message that contains its own ::", func() {
			output := "NUDGE abc123 quiet 42m (worker-a) :: note the key :: value pair\n"

			nudges, err := ParseParkNudges(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(nudges[0].Message).To(gomega.Equal("note the key :: value pair"))
		})

		ginkgo.It("reads an empty name for a session the roster cannot map", func() {
			output := "NUDGE abc123 quiet 42m () :: still quiet\n"

			nudges, err := ParseParkNudges(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(nudges[0].Name).To(gomega.BeEmpty())
		})

		ginkgo.It("ignores the summary line and any other output", func() {
			output := "park-nudge: 1 nudged · 0 suppressed\nNUDGE abc123 quiet 42m (worker-a) :: x\n"

			nudges, err := ParseParkNudges(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(nudges).To(gomega.HaveLen(1))
		})

		ginkgo.It("refuses a nudge line with no message separator", func() {
			_, err := ParseParkNudges(ctx, "NUDGE abc123 quiet 42m (worker-a)\n")
			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})

	ginkgo.Describe("ParseDecisionParkRows", func() {
		ginkgo.It("reads a resume row's kind and session id", func() {
			output := "resume kind=a task-one abc123 — attempt 1 of 3\n" +
				"decision-candidates=1 · decision-resumed-a=0 · decision-resumed-d=0 · decision-unread=0\n"

			decisions, err := ParseDecisionParkRows(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(decisions).To(gomega.Equal([]LapsedManagerDecision{{
				Verdict:   "resume",
				Name:      "task-one",
				SessionID: "abc123",
			}}))
		})

		// ⚠️ The script prints no sid for a crash-loop row: the cap is about the
		// TASK's retry budget, not one session. The decision carries an empty id,
		// which is why `capped` does not require one.
		ginkgo.It("reads a crash-loop row with no session id", func() {
			output := "⚠️ WORKER CRASH-LOOP: task-one — 3 resumes in 30 min; stop resuming\n"

			decisions, err := ParseDecisionParkRows(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(decisions).To(gomega.Equal([]LapsedManagerDecision{{Verdict: "crash-loop"}}))
			gomega.Expect(decisions[0].SessionID).To(gomega.BeEmpty())
		})

		ginkgo.It("ignores unread lines and the summary", func() {
			output := "unread task-one — session abc → UNKNOWN\n" +
				"decision-candidates=0 · decision-resumed-a=0 · decision-resumed-d=0 · decision-unread=1\n"

			decisions, err := ParseDecisionParkRows(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(decisions).To(gomega.BeEmpty())
		})

		ginkgo.It("refuses a resume row with too few fields", func() {
			_, err := ParseDecisionParkRows(ctx, "resume kind=a task-one\n")
			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})

	ginkgo.Describe("ParseWakeMisses", func() {
		ginkgo.It("reads the subject, result, session and live column", func() {
			output := "MISS private-personal goal-a 2026-11-19T20:00:00Z NO-PANE abc123 live=yes\n" +
				"wake-misses=1 stale=0 measured=3 unmeasured=0\n"

			misses, err := ParseWakeMisses(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(misses).To(gomega.Equal([]WakeMiss{{
				Subject:   "goal-a",
				Result:    "NO-PANE",
				SessionID: "abc123",
				Live:      "yes",
			}}))
		})

		// ⚠️ `STALE` goes to fleet-triage, not to this leg. Folding it in would
		// report a subject that may simply have stopped producing actionable ticks.
		ginkgo.It("ignores STALE lines", func() {
			output := "STALE private-personal goal-a 2026-11-17T20:00:00Z HEADLESS abc123 live=yes\n"

			misses, err := ParseWakeMisses(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(misses).To(gomega.BeEmpty())
		})

		ginkgo.It("refuses a MISS line with too few fields", func() {
			_, err := ParseWakeMisses(ctx, "MISS private-personal goal-a\n")
			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})
})
