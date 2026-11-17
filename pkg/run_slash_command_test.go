// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// slashSupervisor is a toolSupervisor whose Get can refuse, so a test can stand
// for a worker the supervisor does not hold (a tab worker) or cannot read.
type slashSupervisor struct {
	*toolSupervisor
	getErr error
}

func (s *slashSupervisor) Get(ctx context.Context, id string) (*Session, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.toolSupervisor.Get(ctx, id)
}

var _ = ginkgo.Describe("RosterToolServer.runSlashCommand", func() {
	var (
		ctx        context.Context
		tabs       *fakeTabClient
		ledger     *fakeLedger
		supervisor *slashSupervisor
		locks      *SessionLocks
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		tabs = &fakeTabClient{}
		ledger = &fakeLedger{}
		locks = NewSessionLocks()
		supervisor = &slashSupervisor{
			toolSupervisor: &toolSupervisor{live: make(chan Event, 64)},
		}
	})

	newServer := func() *RosterToolServer {
		return &RosterToolServer{
			Supervisor: supervisor,
			Tabs:       tabs,
			Roster:     &fakeRoster{},
			Ledger:     ledger,
			Locks:      locks,
		}
	}

	run := func(server *RosterToolServer, args string) (string, error) {
		return server.runSlashCommand(ctx, []byte(args))
	}

	ginkgo.DescribeTable("refuses a command outside the allowlist without touching any worker",
		func(command string) {
			_, err := run(newServer(), `{"agent_id":"s1","command":"`+command+`"}`)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(stderrors.Is(err, ErrSlashCommandNotAllowed)).To(gomega.BeTrue())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("not allowlisted"))
			gomega.Expect(supervisor.sent).To(gomega.BeEmpty())
			gomega.Expect(tabs.texts).To(gomega.BeEmpty())
		},
		ginkgo.Entry("clear", "/clear"),
		ginkgo.Entry("exit", "/exit"),
		ginkgo.Entry("an argument appended to an allowed command", "/compact now"),
		ginkgo.Entry("plain text", "hello"),
		ginkgo.Entry("empty", ""),
	)

	ginkgo.It("requires an agent id", func() {
		_, err := run(newServer(), `{"command":"/compact"}`)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("agent_id is required")))
	})

	ginkgo.DescribeTable("writes an allowlisted command to a headless worker's stdin",
		func(command string) {
			response, err := run(newServer(), `{"agent_id":"s1","command":"`+command+`"}`)
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(supervisor.sent).To(gomega.ConsistOf(command))
			gomega.Expect(tabs.texts).To(gomega.BeEmpty())
			gomega.Expect(response).To(gomega.ContainSubstring(`"channel":"stdin"`))
			gomega.Expect(response).To(gomega.ContainSubstring(`"sent":true`))
		},
		ginkgo.Entry("compact", "/compact"),
		ginkgo.Entry("reload-plugins", "/reload-plugins"),
	)

	ginkgo.It("refuses a headless worker that is running a sequence, writing nothing", func() {
		release, ok := locks.TryAcquire("s1")
		gomega.Expect(ok).To(gomega.BeTrue())
		defer release()
		_, err := run(newServer(), `{"agent_id":"s1","command":"/compact"}`)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("running a sequence")))
		gomega.Expect(supervisor.sent).To(gomega.BeEmpty())
	})

	ginkgo.It("types the command into a tab worker's pane", func() {
		supervisor.getErr = ErrSessionNotFound
		ledger.rows = []LedgerRow{{SessionID: "s1", PaneID: "pane-1", Mode: "interactive"}}
		response, err := run(newServer(), `{"agent_id":"s1","command":"/reload-plugins"}`)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.texts).To(gomega.ConsistOf("/reload-plugins"))
		gomega.Expect(supervisor.sent).To(gomega.BeEmpty())
		gomega.Expect(response).To(gomega.ContainSubstring(`"channel":"pane"`))
		gomega.Expect(response).To(gomega.ContainSubstring(`"pane_id":"pane-1"`))
	})

	ginkgo.It("refuses a headless row whose child this daemon no longer holds", func() {
		supervisor.getErr = ErrSessionNotFound
		ledger.rows = []LedgerRow{{SessionID: "s1", Mode: "headless"}}
		_, err := run(newServer(), `{"agent_id":"s1","command":"/compact"}`)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("no live child")))
		gomega.Expect(tabs.texts).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an A2A or cluster worker by name", func() {
		supervisor.getErr = ErrSessionNotFound
		ledger.rows = []LedgerRow{{SessionID: "s1", Mode: "cluster"}}
		_, err := run(newServer(), `{"agent_id":"s1","command":"/compact"}`)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("not a local tab or headless worker")))
		gomega.Expect(supervisor.sent).To(gomega.BeEmpty())
		gomega.Expect(tabs.texts).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a supervisor read failure instead of falling through to the pane", func() {
		supervisor.getErr = stderrors.New("boom")
		ledger.rows = []LedgerRow{{SessionID: "s1", PaneID: "pane-1", Mode: "interactive"}}
		_, err := run(newServer(), `{"agent_id":"s1","command":"/compact"}`)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("look up worker s1 failed")))
		gomega.Expect(tabs.texts).To(gomega.BeEmpty())
	})
})
