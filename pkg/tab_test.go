// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("BuildTabInner", func() {
	ginkgo.It("exports before the cd so the login shell cannot overwrite them", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd:      "/work",
			Launcher: "/scripts/cc-x",
			TabName:  "⚙ t",
			Prompt:   "hello",
			Env:      map[string]string{"PERMISSION_MODE": "acceptEdits"},
		})
		gomega.Expect(inner).To(gomega.HavePrefix("export PERMISSION_MODE='acceptEdits'; "))
		gomega.Expect(inner).To(gomega.ContainSubstring("cd '/work' && { "))
	})

	ginkgo.It("wraps the launch in a brace group so a failed cd cannot still launch", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "hello",
		})
		gomega.Expect(inner).To(gomega.ContainSubstring("&& { "))
		gomega.Expect(inner).To(gomega.HaveSuffix("; }"))
	})

	ginkgo.It("never uses exec, which would replace the shell and lose the EXIT trap", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "hello",
		})
		gomega.Expect(inner).NotTo(gomega.ContainSubstring("exec "))
	})

	ginkgo.It("installs the exit hook when a record script is given", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "hello",
			ExitRecord: "/scripts/session-exit-record.py",
		})
		gomega.Expect(inner).To(gomega.ContainSubstring("trap _sx EXIT;"))
		gomega.Expect(inner).To(gomega.ContainSubstring(`"$SESSION_EXIT_RECORD" append --kind spawn`))
	})

	ginkgo.It("omits the exit hook entirely when no record script is given", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "hello",
		})
		gomega.Expect(inner).NotTo(gomega.ContainSubstring("SESSION_EXIT_RECORD"))
		gomega.Expect(inner).NotTo(gomega.ContainSubstring("trap"))
	})

	ginkgo.It("escapes a single quote in any quoted value", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "it's fine",
		})
		gomega.Expect(inner).To(gomega.ContainSubstring(`'it'\''s fine'`))
	})

	ginkgo.It("passes the tab name and prompt to the launcher as -n and an argument", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "do it",
		})
		gomega.Expect(inner).To(gomega.ContainSubstring(`'/scripts/cc-x' -n '⚙ t' 'do it'`))
	})
})

var _ = ginkgo.Describe("ComposerReady", func() {
	ginkgo.It("is ready when the last prompt line is empty", func() {
		gomega.Expect(ComposerReady("some output\n❯ \n")).To(gomega.BeTrue())
	})

	ginkgo.It("is not ready when the composer holds a placeholder", func() {
		gomega.Expect(ComposerReady("❯ Try \"fix the bug\"\n")).To(gomega.BeFalse())
	})

	ginkgo.It("uses the LAST prompt line, not the first", func() {
		gomega.Expect(ComposerReady("❯ old text\noutput\n❯ \n")).To(gomega.BeTrue())
	})

	ginkgo.It("is not ready when no prompt line is present", func() {
		gomega.Expect(ComposerReady("still booting\n")).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("SubmissionConfirmed", func() {
	ginkgo.It("confirms when a prompt line follows the message", func() {
		text := "❯ \nhello there\n❯ \n"
		gomega.Expect(SubmissionConfirmed(text, "hello there")).To(gomega.BeTrue())
	})

	ginkgo.It("does not confirm when the message is still in the composer", func() {
		// Typed but never submitted: the text sits on the prompt line itself.
		gomega.Expect(SubmissionConfirmed("❯ hello there\n", "hello there")).To(gomega.BeFalse())
	})

	ginkgo.It("does not confirm when the message never appeared", func() {
		gomega.Expect(SubmissionConfirmed("❯ \n", "hello there")).To(gomega.BeFalse())
	})

	ginkgo.It("confirms a message the pane wrapped onto a second line", func() {
		// The pane wraps, so requiring the whole message on one rendered line
		// reports a landed message as unsubmitted — and the caller then retries,
		// delivering the turn twice.
		message := "please read the whole ledger and tell me which rows are stale, in detail"
		pane := "❯ \nplease read the whole ledger and tell me which rows are stale, in\n" +
			"detail\n❯ \n"
		gomega.Expect(SubmissionConfirmed(pane, message)).To(gomega.BeTrue())
	})

	ginkgo.It("does not confirm a different message that shares no head", func() {
		gomega.Expect(SubmissionConfirmed("❯ \nunrelated output\n❯ \n", "hello there")).
			To(gomega.BeFalse())
	})

	ginkgo.It("does not confirm an empty message", func() {
		gomega.Expect(SubmissionConfirmed("❯ \nanything\n❯ \n", "  ")).To(gomega.BeFalse())
	})
})
