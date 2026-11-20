// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("stood-down candidate join", func() {
	ctx := context.Background()

	ginkgo.Describe("ParseStoodDownCandidates", func() {
		ginkgo.It("splits a slug into branch and base, dropping the age column", func() {
			// ⚠️ The REAL shape: `print(f"{int(age)}\t{s}")`. A fixture with a bare
			// or leading-tab slug is the shape the script never emits, and it hides
			// a parser that drops every row.
			output := "3600\tgoal--my-goal\n900\ttopic--my-topic\n"

			candidates, err := ParseStoodDownCandidates(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(candidates).To(gomega.Equal([]StoodDownCandidate{
				{Slug: "goal--my-goal", Branch: "goal", Base: "my-goal"},
				{Slug: "topic--my-topic", Branch: "topic", Base: "my-topic"},
			}))
		})

		// ⚠️ The guard that keeps a deliberate stand-down from becoming a close:
		// this leg closes managers, never workers, and the LIVE test downstream
		// passes on a task-- marker by construction.
		ginkgo.It("drops task-- slugs", func() {
			output := "3600\ttask--my-task\n900\tgoal--my-goal\n"

			candidates, err := ParseStoodDownCandidates(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(candidates).To(gomega.HaveLen(1))
			gomega.Expect(candidates[0].Branch).To(gomega.Equal("goal"))
		})

		ginkgo.It("ignores prose and blank lines", func() {
			output := "stood-down past 60m:\n\n900\tgoal--my-goal\n"

			candidates, err := ParseStoodDownCandidates(ctx, output)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(candidates).To(gomega.HaveLen(1))
		})

		// ⚠️ A bare slug is a LEGACY marker, not a malformed one. Measured
		// 2026-11-20: the first live tick returned `data-division-documentation`
		// with no `--`, and the old hard error failed the WHOLE leg (it reported
		// `result=error` instead of closing anything). An unknown branch is empty
		// and the join resolves the page by base alone.
		ginkgo.It("accepts a legacy slug with no -- separator as a base-only candidate", func() {
			candidates, err := ParseStoodDownCandidates(ctx, "5001797\tdata-division-documentation\n")

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(candidates).To(gomega.Equal([]StoodDownCandidate{
				{Slug: "data-division-documentation", Branch: "", Base: "data-division-documentation"},
			}))
		})
	})

	ginkgo.Describe("StoodDownSessionsToClose", func() {
		sessionFor := func(sessions map[string]string) func(context.Context, string) (string, error) {
			return func(_ context.Context, base string) (string, error) { return sessions[base], nil }
		}
		live := func(live map[string]bool) func(context.Context, string) (bool, error) {
			return func(_ context.Context, sessionID string) (bool, error) { return live[sessionID], nil }
		}

		// ⚠️ The measurement this exists for: most markers outlive their session.
		ginkgo.It("keeps only the candidate whose session is live", func() {
			candidates := []StoodDownCandidate{
				{Slug: "goal--gone", Branch: "goal", Base: "gone"},
				{Slug: "goal--alive", Branch: "goal", Base: "alive"},
				{Slug: "goal--nosession", Branch: "goal", Base: "nosession"},
			}

			toClose, err := StoodDownSessionsToClose(ctx, candidates,
				sessionFor(map[string]string{"gone": "s-gone", "alive": "s-alive"}),
				live(map[string]bool{"s-gone": false, "s-alive": true}),
			)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(toClose).To(gomega.Equal([]string{"s-alive"}))
		})

		ginkgo.It("surfaces a page read failure rather than closing on a partial join", func() {
			candidates := []StoodDownCandidate{{Slug: "goal--x", Branch: "goal", Base: "x"}}

			_, err := StoodDownSessionsToClose(ctx, candidates,
				func(context.Context, string) (string, error) { return "", errors.New(ctx, "page unreadable") },
				live(nil),
			)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("surfaces a liveness read failure rather than treating it as not-live", func() {
			candidates := []StoodDownCandidate{{Slug: "goal--x", Branch: "goal", Base: "x"}}

			_, err := StoodDownSessionsToClose(ctx, candidates,
				sessionFor(map[string]string{"x": "s-x"}),
				func(context.Context, string) (bool, error) { return false, errors.New(ctx, "probe failed") },
			)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})
})
