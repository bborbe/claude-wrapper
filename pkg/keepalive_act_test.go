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

var _ = ginkgo.Describe("NewKeepaliveAct", func() {
	ctx := context.Background()

	type restarted struct{ sessionID, text string }

	// act wires both seams to recorders, so a test asserts the exact call each
	// verdict makes — an OpenRow for resume, a pane send for restart.
	act := func() (
		func(context.Context, string, LapsedManagerDecision) error,
		*[]OpenRow,
		*[]restarted,
	) {
		opened := &[]OpenRow{}
		sent := &[]restarted{}
		return NewKeepaliveAct(
			func(_ context.Context, row OpenRow) error {
				*opened = append(*opened, row)
				return nil
			},
			func(_ context.Context, sessionID, text string) error {
				*sent = append(*sent, restarted{sessionID: sessionID, text: text})
				return nil
			},
			nil, // stop tripwire not observed by these specs
			func(sessionID string) string { return "/supervisor:restart " + sessionID },
		), opened, sent
	}

	// ⚠️ The no-kill row: a restart types the command into the pane and calls NOTHING
	// that stops a process. The stopper is a tripwire — zero calls after a restart.
	ginkgo.It("never calls the stop tripwire on a restart", func() {
		stopped := &[]string{}
		run := NewKeepaliveAct(
			func(context.Context, OpenRow) error { return nil },
			func(context.Context, string, string) error { return nil },
			func(_ context.Context, sessionID string) error {
				*stopped = append(*stopped, sessionID)
				return nil
			},
			func(sessionID string) string { return "/supervisor:restart " + sessionID },
		)

		_ = run(ctx, "restart", LapsedManagerDecision{SessionID: "s1"})

		gomega.Expect(*stopped).To(gomega.BeEmpty())
	})

	// ⚠️ resume and restart are DIFFERENT routes: resume opens an ABSENT session
	// through the open surface; restart types into a LIVE session's pane. The old
	// act sent both through Supervisor.Send, which reaches neither.
	ginkgo.It("opens the row for a resume verdict, deriving the kind from the branch", func() {
		run, opened, sent := act()

		err := run(ctx, "resume", LapsedManagerDecision{
			SessionID: "s1", Vault: "private-personal", Name: "my-topic", Branch: "topic",
		})

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*opened).To(gomega.Equal([]OpenRow{{
			Kind: OpenKindTopic, Vault: "private-personal", Name: "my-topic",
		}}))
		gomega.Expect(*sent).To(gomega.BeEmpty())
	})

	ginkgo.It("derives OpenKindGoal for a goal branch", func() {
		run, opened, _ := act()

		err := run(ctx, "resume", LapsedManagerDecision{
			SessionID: "s1", Vault: "v", Name: "g", Branch: "goal",
		})

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect((*opened)[0].Kind).To(gomega.Equal(OpenKindGoal))
	})

	ginkgo.It("derives OpenKindTask for a non-topic, non-goal branch", func() {
		run, opened, _ := act()

		err := run(ctx, "resume", LapsedManagerDecision{
			SessionID: "s1", Vault: "v", Name: "t", Branch: "",
		})

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect((*opened)[0].Kind).To(gomega.Equal(OpenKindTask))
	})

	ginkgo.It("types the restart command into the pane for a restart verdict", func() {
		run, opened, sent := act()

		err := run(ctx, "restart", LapsedManagerDecision{SessionID: "s1"})

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*sent).To(gomega.Equal([]restarted{{sessionID: "s1", text: "/supervisor:restart s1"}}))
		gomega.Expect(*opened).To(gomega.BeEmpty())
	})

	// A resume that dropped its subject cannot open by name — refuse rather than
	// open an empty row.
	ginkgo.It("refuses a resume with no subject without opening anything", func() {
		run, opened, _ := act()

		err := run(ctx, "resume", LapsedManagerDecision{SessionID: "s1"})

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*opened).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an unknown verdict without acting", func() {
		run, opened, sent := act()

		err := run(ctx, "leave", LapsedManagerDecision{SessionID: "s1"})

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*opened).To(gomega.BeEmpty())
		gomega.Expect(*sent).To(gomega.BeEmpty())
	})

	// ⚠️ A RESUME needs no session id — it opens by name. Measured 2026-11-20:
	// `due-park-watch` reports `id=<task name>` (SC1), and the old blanket
	// session-id guard made it fail with "keepalive act needs a session id", so
	// that procedure could never act on a real due park.
	ginkgo.It("resumes by name with no session id", func() {
		run, opened, _ := act()

		err := run(ctx, "resume", LapsedManagerDecision{Verdict: "resume", Vault: "v", Name: "a task"})

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*opened).To(gomega.Equal([]OpenRow{{Kind: OpenKindTask, Vault: "v", Name: "a task"}}))
	})

	ginkgo.It("refuses a restart with no session id", func() {
		run, _, sent := act()

		err := run(ctx, "restart", LapsedManagerDecision{Vault: "v", Name: "n"})

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*sent).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a resume with no session id and no subject", func() {
		run, opened, sent := act()

		err := run(ctx, "resume", LapsedManagerDecision{})

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*opened).To(gomega.BeEmpty())
		gomega.Expect(*sent).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a resume wired without an opener", func() {
		run := NewKeepaliveAct(nil, func(context.Context, string, string) error { return nil }, nil, func(s string) string { return s })

		err := run(ctx, "resume", LapsedManagerDecision{SessionID: "s1", Vault: "v", Name: "n"})

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses a restart wired without a pane sender", func() {
		run := NewKeepaliveAct(func(context.Context, OpenRow) error { return nil }, nil, nil, func(s string) string { return s })

		err := run(ctx, "restart", LapsedManagerDecision{SessionID: "s1"})

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces an opener failure", func() {
		run := NewKeepaliveAct(
			func(context.Context, OpenRow) error { return errors.New(ctx, "vault not configured") },
			func(context.Context, string, string) error { return nil },
			nil,
			func(s string) string { return s },
		)

		err := run(ctx, "resume", LapsedManagerDecision{SessionID: "s1", Vault: "v", Name: "n"})

		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
