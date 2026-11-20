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

var _ = ginkgo.Describe("WakeMissProcedure", func() {
	ctx := context.Background()

	fixture := func(misses []WakeMiss) *WakeMissProcedure {
		return &WakeMissProcedure{
			Misses: func(context.Context) ([]WakeMiss, error) { return misses, nil },
		}
	}

	// ⚠️ Report-only. `agents/fleet-keepalive-actor.md` § 4c says "report only", and
	// the step's keep-alive column is a `wake-miss:` line — so this procedure must
	// never report an act.
	ginkgo.It("reports the first miss as kept, naming the session", func() {
		procedure := fixture([]WakeMiss{
			{Subject: "goal-a", SessionID: "s1", Result: "NO-PANE", Live: "yes"},
			{Subject: "goal-b", SessionID: "s2", Result: "HEADLESS", Live: "yes"},
		})

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultKept))
		gomega.Expect(effect.SessionID).To(gomega.Equal("s1"))
		gomega.Expect(effect.Reason).To(gomega.Equal("wake=NO-PANE live=yes"))
	})

	ginkgo.It("reports none when no wake missed", func() {
		effect, err := fixture(nil).Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
	})

	// A subject with no manager at all has no session to name, and that is still a
	// miss — the reason carries the subject instead of dropping the finding.
	ginkgo.It("reports a miss that names no session, carrying the subject", func() {
		procedure := fixture([]WakeMiss{
			{Subject: "goal-a", Result: "NO-MANAGER", Live: "no"},
		})

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultKept))
		gomega.Expect(effect.SessionID).To(gomega.BeEmpty())
		gomega.Expect(effect.Reason).To(gomega.Equal("subject=goal-a"))
	})

	ginkgo.It("surfaces a read failure rather than reporting a quiet tick", func() {
		procedure := &WakeMissProcedure{
			Misses: func(context.Context) ([]WakeMiss, error) {
				return nil, errors.New(ctx, "state dir unreadable")
			},
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses a procedure wired without a finding source", func() {
		_, err := (&WakeMissProcedure{}).Run(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
