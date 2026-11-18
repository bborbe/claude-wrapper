// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("flagged reopen prompt", func() {
	ginkgo.Context("a task row", func() {
		ginkgo.It("puts the readiness gate before the work command", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "task", Name: "Some Task"})

			gomega.Expect(prompt).To(gomega.HavePrefix("FIRST STEP — readiness gate"))
			gomega.Expect(prompt).To(gomega.ContainSubstring(`/vault-cli:work-on-task "Some Task"`))
			gomega.Expect(prompt).To(gomega.ContainSubstring("READINESS:"))
			// The gate must come FIRST: measured 2026-11-18, a trailing gate let the
			// chained work-on-task consume the turn, so the worker stopped at planning
			// and printed the gaps instead of posting the card the gate requires.
			gomega.Expect(strings.Index(prompt, "FIRST STEP")).
				To(gomega.BeNumerically("<", strings.Index(prompt, "/vault-cli:work-on-task")))
			gomega.Expect(prompt).To(gomega.ContainSubstring("Do NOT run the command below"))
		})
	})

	ginkgo.Context("a non-task row", func() {
		ginkgo.It("stays the bare open command, because Gate 2 runs there", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "goal", Name: "Some Goal"})

			gomega.Expect(prompt).To(gomega.Equal(`/supervisor:open "Some Goal"`))
		})
	})
})
