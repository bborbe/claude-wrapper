// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"time"

	libtime "github.com/bborbe/time"
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

	ginkgo.It("drops the spawner's session identity and marks the pane a CLI session first", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "do it",
			Env:             map[string]string{"PERMISSION_MODE": "acceptEdits"},
			ResetSessionEnv: true,
		})
		const prefix = "unset CLAUDE_CODE_MESSAGING_SOCKET CLAUDE_CODE_MESSAGING_TOKEN " +
			"CLAUDE_CODE_SESSION_ID CLAUDE_CODE_CHILD_SESSION; " +
			"export CLAUDE_CODE_ENTRYPOINT='cli'; "
		gomega.Expect(inner).To(gomega.HavePrefix(prefix))
		gomega.Expect(strings.Index(inner, "unset ")).To(gomega.BeNumerically("<",
			strings.Index(inner, "export PERMISSION_MODE")))
	})

	ginkgo.It("renders --resume between the launcher and -n", func() {
		const id = "1b6a7c3e-0000-4000-8000-000000000000"
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t", Prompt: "do it",
			Resume: id,
		})
		gomega.Expect(inner).To(gomega.ContainSubstring(
			`'/scripts/cc-x' --resume '` + id + `' -n '⚙ t' 'do it'`))
	})

	ginkgo.It("omits the trailing prompt word when the prompt is empty", func() {
		inner := BuildTabInner(TabSpawnOptions{
			Cwd: "/work", Launcher: "/scripts/cc-x", TabName: "⚙ t",
		})
		gomega.Expect(inner).To(gomega.HaveSuffix(`'/scripts/cc-x' -n '⚙ t'; }`))
	})
})

// notReadyTabClient never shows an empty composer, so a ready-gated send times
// out without typing anything.
type notReadyTabClient struct {
	spawned []TabSpawnOptions
	texts   []string
	onSpawn func()
}

func (f *notReadyTabClient) Spawn(_ context.Context, options TabSpawnOptions) (string, error) {
	f.spawned = append(f.spawned, options)
	if f.onSpawn != nil {
		f.onSpawn()
	}
	return "pane-1", nil
}

func (f *notReadyTabClient) Activate(context.Context, string) error { return nil }

func (f *notReadyTabClient) SendText(_ context.Context, _ string, text string) error {
	f.texts = append(f.texts, text)
	return nil
}

func (f *notReadyTabClient) Text(context.Context, string) (string, error) {
	return "still booting\n", nil
}

// steppingClockEvery advances step per read, so a poll loop's deadline is
// reached without sleeping.
func steppingClockEvery(step time.Duration) libtime.CurrentDateTimeGetter {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		now = now.Add(step)
		return libtime.DateTime(now)
	})
}

var _ = ginkgo.Describe("SendWhenReadyWithin", func() {
	ginkgo.It("sends the text once when the composer is ready", func() {
		tabs := &fakeTabClient{}
		err := SendWhenReadyWithin(
			context.Background(), tabs, "pane-1", "hello", steppingClock(), time.Minute,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{"hello"}))
	})

	ginkgo.It("returns an error and sends nothing when the composer never becomes ready", func() {
		tabs := &notReadyTabClient{}
		err := SendWhenReadyWithin(
			context.Background(),
			tabs,
			"pane-1",
			"hello",
			steppingClockEvery(30*time.Second),
			time.Minute,
		)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(tabs.texts).To(gomega.BeEmpty())
	})

	ginkgo.It("confirms a start command on an escaped prompt line", func() {
		// The open-endpoint path reads the pane with `--escapes`; the prompt line
		// carries a leading SGR, so raw SubmissionConfirmed reports an unsubmitted
		// send that DID land — measured 2026-11-26, "pane 308 never showed the
		// message as submitted". SendWhenReadyWithin now uses
		// SubmissionConfirmedVisible, so it confirms.
		tabs := &escapedConfirmTabClient{fakeTabClient: &fakeTabClient{}}
		message := `/vault-cli:work-on-goal "The Strategy Prototype Agent"`
		err := SendWhenReadyWithin(
			context.Background(), tabs, "pane-1", message, steppingClock(), time.Minute,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.texts).To(gomega.ConsistOf(message))
	})
})

// escapedConfirmTabClient returns an escaped prompt line after the send — the
// shape a `--escapes` pane read produces (a leading SGR before the glyph), where
// raw SubmissionConfirmed misses the fresh prompt and SubmissionConfirmedVisible
// (stripping ANSI) confirms it.
type escapedConfirmTabClient struct {
	*fakeTabClient
}

func (f *escapedConfirmTabClient) Text(context.Context, string) (string, error) {
	if len(f.texts) == 0 {
		return promptGlyph + " \n", nil
	}
	return "❯ \n" + f.texts[len(f.texts)-1] + "\n\x1b[38;2::153:153:153m❯ \n", nil
}

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

var _ = ginkgo.Describe("SubmissionConfirmedVisible", func() {
	// A long-idle pane read with `--escapes`. The prompt glyph line carries a
	// leading SGR colour (`\x1b[38;2;…m❯ `), measured on a real pane 2026-11-26.
	// SubmissionConfirmed's HasPrefix(TrimSpace(line), "❯") never matches an escaped
	// prompt line, so a submit that DID land reads as unconfirmed; the visible
	// variant strips the escapes first.
	escaped := "❯ \n\x1b[36m/reload-plugins\x1b[0m\nReloaded: 3 plugins\n\x1b[38;2::153:153:153m❯ \n"

	ginkgo.It("raw SubmissionConfirmed misses a prompt line that carries a leading SGR", func() {
		gomega.Expect(SubmissionConfirmed(escaped, "/reload-plugins")).To(gomega.BeFalse())
	})

	ginkgo.It("confirms the same escaped screen after stripping ANSI", func() {
		gomega.Expect(SubmissionConfirmedVisible(escaped, "/reload-plugins")).To(gomega.BeTrue())
	})

	ginkgo.It("still refuses when the message never appeared", func() {
		gomega.Expect(SubmissionConfirmedVisible("❯ \n\x1b[36m/other\x1b[0m\n❯ \n", "/reload-plugins")).
			To(gomega.BeFalse())
	})
})
