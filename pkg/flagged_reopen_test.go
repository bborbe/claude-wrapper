// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
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

	ginkgo.Context("a goal or topic row", func() {
		ginkgo.It("has no headless worker prompt — it opens a manager tab instead", func() {
			gomega.Expect(reopenPrompt(FlaggedRow{Kind: "goal", Name: "Some Goal"})).To(gomega.BeEmpty())
			gomega.Expect(reopenPrompt(FlaggedRow{Kind: "topic", Name: "Some Topic"})).To(gomega.BeEmpty())
		})
	})
})

var _ = ginkgo.Describe("flagged reopen routes by role through the open decision", func() {
	ginkgo.It("opens an agent row headless even when its stored mode is interactive", func() {
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "T", "role: agent", "mode: interactive")
		reopener := &flaggedReopener{Executor: f.executor}

		err := reopener.Reopen(context.Background(), FlaggedRow{
			Kind: "task", Vault: "v1", Name: "T", Role: RoleAgent, Mode: SpawnModeInteractive,
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("opens a human row as a tab", func() {
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "T", "role: human")
		reopener := &flaggedReopener{Executor: f.executor}

		err := reopener.Reopen(context.Background(), FlaggedRow{
			Kind: "task", Vault: "v1", Name: "T", Role: RoleHuman, Mode: SpawnModeInteractive,
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(f.tabOpener.requests).To(gomega.HaveLen(1))
		gomega.Expect(f.tabOpener.requests[0].WindowID).To(gomega.Equal(12))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("resume-no-transcript", func() {
	cwd := "/Users/bborbe/Documents/Obsidian/private-personal"
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)

	ginkgo.It("resumes when the stamped session has a transcript", func() {
		dir := ginkgo.GinkgoT().TempDir()
		gomega.Expect(os.MkdirAll(filepath.Join(dir, slug), 0o755)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(
			filepath.Join(dir, slug, "sid-1.jsonl"), []byte("{}\n"), 0o600,
		)).To(gomega.Succeed())

		r := &flaggedReopener{ProjectsDir: dir}

		gomega.Expect(r.resumableSessionID(
			FlaggedRow{Kind: "task", Name: "T", SessionID: "sid-1"}, cwd,
		)).To(gomega.Equal("sid-1"))
	})

	ginkgo.It("re-creates when the stamped session has no transcript", func() {
		// No file written under the projects dir: the stamp points at a
		// conversation that does not exist, so the row must be created fresh.
		r := &flaggedReopener{ProjectsDir: ginkgo.GinkgoT().TempDir()}

		gomega.Expect(r.resumableSessionID(
			FlaggedRow{Kind: "task", Name: "T", SessionID: "sid-missing"}, cwd,
		)).To(gomega.BeEmpty())
	})

	ginkgo.It("keeps a row with no session id on the create path", func() {
		r := &flaggedReopener{ProjectsDir: ginkgo.GinkgoT().TempDir()}

		gomega.Expect(r.resumableSessionID(
			FlaggedRow{Kind: "task", Name: "T"}, cwd,
		)).To(gomega.BeEmpty())
	})
})
