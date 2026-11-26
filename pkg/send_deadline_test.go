// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// A child that stops reading its stdin must cost one bounded write, never the
// caller's goroutine.
//
// Measured 2026-11-26: the flagged tick's goroutine sat 67 minutes in
// `(*child).send` → `syscall.write` while the daemon stayed up, because the
// daemon wrote through the same descriptor it had handed the child, os/exec's
// Fd() call had made that descriptor blocking, and the write had no deadline.
// Every flagged row behind it went unopened.
//
// The child here is a REAL process that never reads stdin, because the claim is
// about a FIFO's buffer and a descriptor's blocking mode — neither is observable
// through a fake writer.
var _ = ginkgo.Describe("child stdin write deadline", func() {
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

	// deafChild announces itself, then sleeps without ever reading stdin — the
	// shape of a child hung after startup.
	deafChild := func() string {
		path := filepath.Join(dir, "deaf-child.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"m\",\"permissionMode\":\"auto\"}'\n" +
			"exec sleep 600\n"
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

	// spawn starts the child and kills its process group afterwards: it is started
	// with Setsid and would otherwise outlive the test binary.
	spawn := func(s *supervisor) *Session {
		session, err := s.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.DeferCleanup(func() {
			c := childFor(s, session.ID)
			if c == nil || c.pid <= 0 {
				return
			}
			if killErr := syscall.Kill(-c.pid, syscall.SIGKILL); killErr != nil &&
				!stderrors.Is(killErr, syscall.ESRCH) {
				gomega.Expect(killErr).NotTo(gomega.HaveOccurred())
			}
		})
		return session
	}

	// A line far larger than any FIFO buffer, so the write cannot complete
	// without a reader draining it.
	oversized := strings.Repeat("x", 4<<20)

	ginkgo.It("refuses a write the child does not read, instead of blocking the caller", func() {
		sup := newSupervisor(deafChild())
		session := spawn(sup)
		gomega.Eventually(func() State {
			return childFor(sup, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))

		c := childFor(sup, session.ID)
		c.writeTimeout = 300 * time.Millisecond

		done := make(chan error, 1)
		go func() { done <- sup.Send(ctx, session.ID, oversized) }()

		var sendErr error
		gomega.Eventually(done, "5s").Should(gomega.Receive(&sendErr))
		gomega.Expect(stderrors.Is(sendErr, ErrSendTimeout)).To(gomega.BeTrue(),
			"want ErrSendTimeout, got %v", sendErr)
	})

	ginkgo.It("refuses every later write once a write has timed out", func() {
		sup := newSupervisor(deafChild())
		session := spawn(sup)
		gomega.Eventually(func() State {
			return childFor(sup, session.ID).snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))

		c := childFor(sup, session.ID)
		c.writeTimeout = 300 * time.Millisecond
		gomega.Expect(stderrors.Is(sup.Send(ctx, session.ID, oversized), ErrSendTimeout)).To(gomega.BeTrue())

		// A small line would fit the buffer, but the stream already holds part of
		// the timed-out line, so it is refused rather than spliced onto it.
		start := time.Now()
		gomega.Expect(stderrors.Is(sup.Send(ctx, session.ID, "hello"), ErrSendTimeout)).To(gomega.BeTrue())
		gomega.Expect(time.Since(start)).To(gomega.BeNumerically("<", 100*time.Millisecond))
	})
})
