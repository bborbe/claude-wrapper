// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The property under test is that a holder whose recorded child is provably gone
// — the pid was reused by an unrelated process — is released rather than read as
// live, while a genuinely live holder is still refused.
var _ = ginkgo.Describe("resume guard on a provably stale holder", func() {
	newSupervisor := func() *supervisor {
		s, ok := NewSupervisor(
			"claude",
			libtime.NewCurrentDateTime(),
			"http://localhost:0",
			NewSessionStore(ginkgo.GinkgoT().TempDir()),
		).(*supervisor)
		gomega.Expect(ok).To(gomega.BeTrue())
		return s
	}

	// adoptedHolder builds the shape an adopted child has: cmd == nil (no Wait),
	// a recorded pid, and the start time the record proves for that pid.
	adoptedHolder := func(state State, procStart time.Time) *child {
		return &child{
			id:          "s1",
			pid:         os.Getpid(),
			procStart:   procStart,
			now:         libtime.NewCurrentDateTime(),
			ctx:         context.Background(),
			subscribers: map[int]chan Event{},
			session:     Session{ID: "s1", State: state},
		}
	}

	ginkgo.It("releases an adopted holder whose pid was reused, so the resume proceeds", func() {
		s := newSupervisor()
		// A start time far from the live pid's real start proves the pid now
		// belongs to a different process.
		holder := adoptedHolder(StateIdle, time.Unix(0, 0))
		s.sessions = map[string]*child{"s1": holder}

		gomega.Expect(s.reserve(context.Background(), "s1")).To(gomega.Succeed())
		gomega.Expect(holder.isExited()).To(gomega.BeTrue())
	})

	ginkgo.It("still refuses a live adopted holder whose pid is the recorded child", func() {
		s := newSupervisor()
		start, ok := procStartTime(os.Getpid())
		gomega.Expect(ok).To(gomega.BeTrue())
		holder := adoptedHolder(StateIdle, start)
		s.sessions = map[string]*child{"s1": holder}

		err := s.reserve(context.Background(), "s1")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(stderrors.Is(err, ErrSessionLive)).To(gomega.BeTrue())
		gomega.Expect(holder.isExited()).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("pidReused", func() {
	base := time.Unix(1_700_000_000, 0)

	ginkgo.DescribeTable("decides whether a pid's process is provably not the recorded child",
		func(recorded, current time.Time, ok, want bool) {
			gomega.Expect(pidReused(recorded, current, ok)).To(gomega.Equal(want))
		},
		ginkgo.Entry("same instant is not reuse", base, base, true, false),
		ginkgo.Entry("within tolerance is not reuse", base, base.Add(time.Second), true, false),
		ginkgo.Entry("a later process is reuse", base, base.Add(time.Hour), true, true),
		ginkgo.Entry("an earlier process is reuse", base, base.Add(-time.Hour), true, true),
		ginkgo.Entry("unknown current never proves reuse", base, time.Time{}, false, false),
		ginkgo.Entry("unknown recorded never proves reuse", time.Time{}, base, true, false),
	)
})

var _ = ginkgo.Describe("procStartTime", func() {
	ginkgo.It("reads a start time for the current process", func() {
		start, ok := procStartTime(os.Getpid())
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(start.IsZero()).To(gomega.BeFalse())
		// The test process started within the last hour.
		gomega.Expect(start.After(time.Now().Add(-time.Hour))).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Adopt prunes a record whose pid was reused", func() {
	ginkgo.It("drops the stale record and keeps the genuine one", func() {
		ctx := context.Background()
		store := NewSessionStore(ginkgo.GinkgoT().TempDir())
		pid := os.Getpid()
		start, ok := procStartTime(pid)
		gomega.Expect(ok).To(gomega.BeTrue())

		stale := SessionRecord{
			ID:             "stale",
			ChildPID:       pid,
			StartedAt:      time.Unix(0, 0),
			ChildStartedAt: time.Unix(0, 0),
		}
		_, err := store.Create(ctx, stale)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		genuine := SessionRecord{
			ID:             "genuine",
			ChildPID:       pid,
			StartedAt:      start,
			ChildStartedAt: start,
		}
		_, err = store.Create(ctx, genuine)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		s, ok := NewSupervisor(
			"claude",
			libtime.NewCurrentDateTime(),
			"http://localhost:0",
			store,
		).(*supervisor)
		gomega.Expect(ok).To(gomega.BeTrue())
		_, err = s.Adopt(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		_, staleErr := store.Load(ctx, "stale")
		gomega.Expect(staleErr).To(gomega.HaveOccurred())
		_, genuineErr := store.Load(ctx, "genuine")
		gomega.Expect(genuineErr).NotTo(gomega.HaveOccurred())
	})
})
