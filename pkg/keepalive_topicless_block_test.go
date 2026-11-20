// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ParseTopiclessGoalBlock", func() {
	ctx := context.Background()

	// A real block: two summary lines wrapping tab-separated rows. The reason on
	// the resume row carries its own detail, and the jump row's command is `-`.
	const block = "selected: 3\n" +
		"goal--a\tjump\t-\tloop armed\n" +
		"goal--b\tresume\t/supervisor:open --goal \"goal b\"\tnot live\n" +
		"goal--c\tcrash-loop\t-\t3 resumes in 30 min\n" +
		"actionable: 1\n"

	ginkgo.It("reads the summaries and every row", func() {
		parsed, err := ParseTopiclessGoalBlock(ctx, block)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(parsed.Selected).To(gomega.Equal(3))
		gomega.Expect(parsed.Actionable).To(gomega.Equal(1))
		gomega.Expect(parsed.Rows).To(gomega.HaveLen(3))
		gomega.Expect(parsed.Rows[1]).To(gomega.Equal(TopiclessGoalRow{
			Goal: "goal--b", Verdict: "resume", Command: `/supervisor:open --goal "goal b"`, Reason: "not live",
		}))
	})

	// ⚠️ The wrapper lines are NOT rows. A parser that treated every non-empty line
	// as a row would invent goals named "selected:" and "actionable:".
	ginkgo.It("does not treat the summary lines as rows", func() {
		parsed, err := ParseTopiclessGoalBlock(ctx, block)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		for _, row := range parsed.Rows {
			gomega.Expect(row.Goal).NotTo(gomega.Equal("selected:"))
			gomega.Expect(row.Goal).NotTo(gomega.Equal("actionable:"))
		}
	})

	// ⚠️ REFUSED: (exit 2) is a refusal, not an empty selection — it must surface as
	// an error, never a quiet none over an unread fleet.
	ginkgo.It("errors on a REFUSED line rather than reporting an empty block", func() {
		_, err := ParseTopiclessGoalBlock(ctx, "REFUSED: goals store unreadable\n")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("errors on a row with too few fields", func() {
		_, err := ParseTopiclessGoalBlock(ctx, "selected: 1\ngoal--a\tresume\n")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("keeps a TAB inside the reason intact", func() {
		parsed, err := ParseTopiclessGoalBlock(ctx, "goal--a\tresume\tcmd\treason\twith\ttabs\n")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(parsed.Rows[0].Reason).To(gomega.Equal("reason\twith\ttabs"))
	})

	ginkgo.It("errors on a non-integer summary value", func() {
		_, err := ParseTopiclessGoalBlock(ctx, "selected: many\n")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.Describe("FirstActionable", func() {
		ginkgo.It("returns the first resume or restart row as a goal decision", func() {
			parsed, err := ParseTopiclessGoalBlock(ctx, block)
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))

			decision, ok := parsed.FirstActionable("private-personal")

			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(decision).To(gomega.Equal(LapsedManagerDecision{
				Verdict: "resume", Vault: "private-personal", Name: "goal--b", Branch: "goal",
			}))
		})

		ginkgo.It("returns false when no row licenses an act", func() {
			parsed, err := ParseTopiclessGoalBlock(ctx, "selected: 1\ngoal--a\tjump\t-\tarmed\n")
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))

			_, ok := parsed.FirstActionable("v")

			gomega.Expect(ok).To(gomega.BeFalse())
		})
	})
})
