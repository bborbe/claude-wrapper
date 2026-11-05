// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The property under test is the RECORD a resume produces, asserted through the
// public Spawn/Get/List surface on real child processes: one claude conversation
// is one record, so a reader that looks a session up by its claude id finds the
// child that is actually running.
var _ = ginkgo.Describe("Spawn resume", func() {
	var (
		resumed string
		ctx     context.Context
		dir     string
		exit    string
		hold    string
	)

	ginkgo.BeforeEach(func() {
		// Per-test id: the mcp temp files are keyed by it in the shared TempDir.
		resumed = newID()
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
		exit = filepath.Join(dir, "exit.sh")
		hold = filepath.Join(dir, "hold.sh")
		gomega.Expect(os.WriteFile(exit, []byte("#!/bin/sh\nexit 0\n"), 0o700)).To(gomega.Succeed())
		// Reads stdin until closed, so the child stays alive until stopped.
		gomega.Expect(os.WriteFile(hold, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o700)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			left, _ := filepath.Glob(filepath.Join(os.TempDir(), "claude-wrapper-"+resumed+".mcp.*"))
			for _, path := range left {
				_ = os.Remove(path)
			}
		})
	})

	newSupervisor := func(bin string) *supervisor {
		s, ok := NewSupervisor(ctx, bin, libtime.NewCurrentDateTime(), "http://localhost:0").(*supervisor)
		gomega.Expect(ok).To(gomega.BeTrue())
		return s
	}

	// record reads the map under the lock, like every production reader.
	record := func(s *supervisor, id string) *child {
		s.mutex.RLock()
		defer s.mutex.RUnlock()
		return s.sessions[id]
	}

	waitExited := func(s *supervisor, id string) {
		gomega.Eventually(func() State {
			session, err := s.Get(ctx, id)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return session.State
		}, "5s").Should(gomega.Equal(StateExited))
	}

	ginkgo.It("keys the record by the resumed id, so N resumes leave one record", func() {
		s := newSupervisor(exit)
		for i := 0; i < 3; i++ {
			session, err := s.Spawn(ctx, SpawnOptions{Cwd: dir, Resume: resumed})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(session.ID).To(gomega.Equal(resumed))
			waitExited(s, resumed)
		}
		sessions, err := s.List(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(sessions).To(gomega.HaveLen(1))
	})

	ginkgo.It("resolves the claude id to the CURRENT child after a resume", func() {
		s := newSupervisor(exit)
		_, err := s.Spawn(ctx, SpawnOptions{Cwd: dir, Resume: resumed})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		waitExited(s, resumed)
		first := record(s, resumed)

		s.claudeBin = hold
		_, err = s.Spawn(ctx, SpawnOptions{Cwd: dir, Resume: resumed})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.DeferCleanup(func() { _ = s.Stop(ctx, resumed) })

		gomega.Expect(record(s, resumed)).NotTo(gomega.BeIdenticalTo(first))
		session, err := s.Get(ctx, resumed)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(session.State).NotTo(gomega.Equal(StateExited))
	})

	ginkgo.It("refuses to resume a conversation whose child is still running", func() {
		s := newSupervisor(hold)
		_, err := s.Spawn(ctx, SpawnOptions{Cwd: dir, Resume: resumed})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.DeferCleanup(func() { _ = s.Stop(ctx, resumed) })
		live := record(s, resumed)

		_, err = s.Spawn(ctx, SpawnOptions{Cwd: dir, Resume: resumed})
		gomega.Expect(stderrors.Is(err, ErrSessionLive)).To(gomega.BeTrue())
		gomega.Expect(record(s, resumed)).To(gomega.BeIdenticalTo(live))
	})

	ginkgo.It("refuses a resume while another resume of the same id is in flight", func() {
		s := newSupervisor(exit)
		gomega.Expect(s.reserve(ctx, resumed)).To(gomega.Succeed())
		_, err := s.Spawn(ctx, SpawnOptions{Cwd: dir, Resume: resumed})
		gomega.Expect(stderrors.Is(err, ErrSessionLive)).To(gomega.BeTrue())
		s.release(resumed)
	})

	// Negative control: a fresh spawn still mints its own id, so the fix cannot
	// pass by changing the id for everyone.
	ginkgo.It("still mints a fresh id when nothing is resumed", func() {
		s := newSupervisor(exit)
		session, err := s.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.DeferCleanup(func() {
			left, _ := filepath.Glob(filepath.Join(os.TempDir(), "claude-wrapper-"+session.ID+".mcp.*"))
			for _, path := range left {
				_ = os.Remove(path)
			}
		})
		gomega.Expect(uuidPattern.MatchString(session.ID)).To(gomega.BeTrue())
		gomega.Expect(session.ID).NotTo(gomega.Equal(resumed))
	})
})
