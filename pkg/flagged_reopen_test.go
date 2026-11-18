// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("flagged reopen prompt", func() {
	ginkgo.Context("a task row", func() {
		ginkgo.It("carries the readiness gate after the work command", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "task", Name: "Some Task"})

			gomega.Expect(prompt).To(gomega.HavePrefix(`/vault-cli:work-on-task "Some Task"`))
			gomega.Expect(prompt).To(gomega.ContainSubstring("FIRST STEP — readiness gate"))
			gomega.Expect(prompt).To(gomega.ContainSubstring("READINESS:"))
			gomega.Expect(prompt).To(gomega.ContainSubstring("phase: planning"))
		})
	})

	ginkgo.Context("a non-task row", func() {
		ginkgo.It("stays the bare open command, because Gate 2 runs there", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "goal", Name: "Some Goal"})

			gomega.Expect(prompt).To(gomega.Equal(`/supervisor:open "Some Goal"`))
		})
	})
})
