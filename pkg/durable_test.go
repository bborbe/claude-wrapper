// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// durableSuite exercises the half of the design that makes a restart survivable:
// named endpoints the child holds its own end of, a record a successor can read,
// re-adoption, and a drain that waits for a turn boundary instead of severing one.
//
// The child here is a REAL process — a shell script speaking the same JSONL
// protocol on its stdin and stdout — because the claim under test is about file
// descriptors and process lifetime, and neither is observable through a mock.
var _ = ginkgo.Describe("durable session endpoints", func() {
	var (
		ctx   context.Context
		dir   string
		store SessionStore
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
		store = NewSessionStore(filepath.Join(dir, "sessions"))
	})

	// echoChild writes a script that announces itself, then answers every line it
	// reads on stdin with a result event. The wrapper never parses a prompt, so an
	// echo is enough to drive state: init makes the session idle, a result closes
	// whatever turn was open.
	echoChild := func() string {
		path := filepath.Join(dir, "echo-child.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"m\",\"permissionMode\":\"auto\"}'\n" +
			"while IFS= read -r line; do\n" +
			"  printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\"}'\n" +
			"done\n"
		gomega.Expect(os.WriteFile(path, []byte(script), 0o700)).To(gomega.Succeed())
		return path
	}

	newSupervisor := func(bin string) *supervisor {
		return &supervisor{
			claudeBin: bin,
			now:       libtime.NewCurrentDateTime(),
			selfURL:   "http://localhost:0",
			store:     store,
			sessions:  map[string]*child{},
			pending:   NewPending(),
		}
	}

	childFor := func(s *supervisor, id string) *child {
		s.mutex.RLock()
		defer s.mutex.RUnlock()
		return s.sessions[id]
	}

	ginkgo.It("keeps the child running and reachable after the spawning side drops its handles", func() {
		sup := newSupervisor(echoChild())
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() State {
			return childFor(sup, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))

		// The spawning daemon's handles are dropped — which is what dying means for
		// a process holding a FIFO and a file. The child must not see an EOF: it
		// holds its own write end, and that is the whole reason the endpoints are
		// named rather than anonymous pipes.
		c := childFor(sup, session.ID)
		gomega.Expect(c.stdin.Close()).To(gomega.Succeed())

		gomega.Consistently(func() bool {
			return processAlive(c.pid)
		}, "1s").Should(gomega.BeTrue())

		// A successor opens the same endpoints and writes a turn. It lands, which
		// proves both that the child is still reading its stdin and that the path is
		// reachable by a process that did not start it.
		files, err := store.Open(ctx, session.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() { _ = files.Stdin.Close() }()
		_, err = files.Stdin.Write([]byte("{\"type\":\"user\"}\n"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Eventually(func() int {
			return len(childFor(sup, session.ID).snapshot().Controls)
		}, "5s").Should(gomega.BeNumerically(">", 0))
		gomega.Expect(processAlive(c.pid)).To(gomega.BeTrue())
	})

	ginkgo.It("re-adopts a surviving child from its record and rebuilds its state", func() {
		sup := newSupervisor(echoChild())
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() State {
			return childFor(sup, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))

		// A second supervisor over the same store stands in for the daemon that
		// starts after a restart: a fresh, empty in-memory map and nothing but the
		// record and the event file to go on.
		successor := newSupervisor(echoChild())
		adopted, err := successor.Adopt(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(adopted).To(gomega.Equal(1))

		recovered, err := successor.Get(ctx, session.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(recovered.ID).To(gomega.Equal(session.ID))
		gomega.Expect(recovered.Cwd).To(gomega.Equal(dir))
		// The state is rebuilt by replaying the event file, not carried over from
		// memory — so a replay that dropped the init event would leave this starting.
		gomega.Eventually(func() State {
			return childFor(successor, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))
	})

	ginkgo.It("prunes a record whose child is gone", func() {
		dead := SessionRecord{
			ID:        "11111111-2222-4333-8444-555555555555",
			ChildPID:  999999,
			Cwd:       dir,
			StartedAt: time.Now().UTC(),
		}
		files, err := store.Create(ctx, dead)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_ = files.Stdin.Close()
		_ = files.EventsWriter.Close()

		sup := newSupervisor(echoChild())
		adopted, err := sup.Adopt(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(adopted).To(gomega.Equal(0))

		records, err := store.List(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(records).To(gomega.BeEmpty())
	})

	ginkgo.It("drains at a turn boundary without recording an interruption", func() {
		sup := newSupervisor(echoChild())
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() State {
			return childFor(sup, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))

		report := sup.Drain(ctx, 5*time.Second)
		gomega.Expect(report.Sessions).To(gomega.HaveLen(1))
		gomega.Expect(report.Sessions[0].Interrupted).To(gomega.BeFalse())
		gomega.Expect(processAlive(childFor(sup, session.ID).pid)).To(gomega.BeTrue())
	})

	ginkgo.It("records an interruption when the bound expires mid-turn", func() {
		// This child announces a turn and never closes it, so the drain has to give
		// up on the bound — the case that must be recorded rather than silently
		// severed.
		path := filepath.Join(dir, "stuck-child.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"m\"}'\n" +
			"printf '%s\\n' '{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"}}'\n" +
			"while IFS= read -r line; do :; done\n"
		gomega.Expect(os.WriteFile(path, []byte(script), 0o700)).To(gomega.Succeed())

		sup := newSupervisor(path)
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() State {
			return childFor(sup, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateGenerating))

		report := sup.Drain(ctx, 300*time.Millisecond)
		gomega.Expect(report.Sessions).To(gomega.HaveLen(1))
		gomega.Expect(report.Sessions[0].Interrupted).To(gomega.BeTrue())
		gomega.Expect(report.Sessions[0].Reason).To(gomega.ContainSubstring("drain bound"))

		// Recorded in the durable record, so it survives the daemon that observed it,
		// and exposed on the session, so a reader does not need the log.
		record, err := store.Load(ctx, session.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(record.Interrupted).To(gomega.ContainSubstring("drain bound"))
		gomega.Expect(childFor(sup, session.ID).snapshot().Interrupted).
			To(gomega.ContainSubstring("drain bound"))

		// Never severed: the child is still running after the drain gave up.
		gomega.Expect(processAlive(childFor(sup, session.ID).pid)).To(gomega.BeTrue())
	})

	ginkgo.It("hands out sequence numbers a reconnect can resume from", func() {
		sup := newSupervisor(echoChild())
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() State {
			return childFor(sup, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))

		replay, firstSeq, _, cancel, err := sup.SubscribeFrom(ctx, session.ID, -1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cancel()
		gomega.Expect(firstSeq).To(gomega.Equal(0))
		gomega.Expect(replay).NotTo(gomega.BeEmpty())
		emitted := len(replay)

		// Resuming at the end returns nothing yet, and says where the next event
		// will sit — which is what a client needs to keep numbering its own view.
		resumed, resumedSeq, _, cancelResumed, err := sup.SubscribeFrom(ctx, session.ID, emitted)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cancelResumed()
		gomega.Expect(resumed).To(gomega.BeEmpty())
		gomega.Expect(resumedSeq).To(gomega.Equal(emitted))
	})

	ginkgo.It("refuses a resume point it no longer retains", func() {
		// A trimmed replay buffer is the case a client must not be handed silently:
		// the point it asks for is gone, and substituting the oldest retained event
		// would open an invisible hole in its stream.
		c := &child{
			id:          "c1",
			now:         libtime.NewCurrentDateTime(),
			session:     Session{State: StateIdle},
			subscribers: map[int]chan Event{},
			firstSeq:    10,
		}
		_, _, _, _, err := c.subscribeFrom(3)
		gomega.Expect(stderrors.Is(err, ErrStreamGap)).To(gomega.BeTrue())
	})
})
