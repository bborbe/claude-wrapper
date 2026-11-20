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

var _ = ginkgo.Describe("ParkNudgeProcedure", func() {
	ctx := context.Background()

	type sent struct{ sessionID, message string }

	fixture := func(nudges []ParkNudge, sendErr error) (*ParkNudgeProcedure, *[]sent) {
		sentTo := &[]sent{}
		return &ParkNudgeProcedure{
			Nudges: func(context.Context) ([]ParkNudge, error) { return nudges, nil },
			Send: func(_ context.Context, sessionID, message string) error {
				*sentTo = append(*sentTo, sent{sessionID: sessionID, message: message})
				return sendErr
			},
		}, sentTo
	}

	ginkgo.It("sends the first nudge the script selected", func() {
		procedure, sentTo := fixture([]ParkNudge{
			{SessionID: "s1", Name: "worker-a", Message: "still quiet"},
			{SessionID: "s2", Name: "worker-b", Message: "also quiet"},
		}, nil)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultNudged,
			SessionID: "s1",
		}))
		// One per tick: the script's ledger is what stops a repeat, not a batch.
		gomega.Expect(*sentTo).To(gomega.Equal([]sent{{sessionID: "s1", message: "still quiet"}}))
	})

	ginkgo.It("reports none when the script selected nothing", func() {
		procedure, sentTo := fixture(nil, nil)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
		gomega.Expect(*sentTo).To(gomega.BeEmpty())
	})

	// ⚠️ The nudge routes by SESSION ID, not roster name: a nudged watcher is a TAB
	// reached through its pane, so Send receives the session id. A session id with
	// no pane surfaces as a Send error → undelivered (covered by the next spec).
	ginkgo.It("sends to the session id, not the roster name", func() {
		procedure, sentTo := fixture([]ParkNudge{
			{SessionID: "s1", Name: "worker-a", Message: "still quiet"},
		}, nil)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNudged))
		gomega.Expect(*sentTo).To(gomega.Equal([]sent{{sessionID: "s1", message: "still quiet"}}))
	})

	ginkgo.It("reports undelivered when the send fails, rather than erroring the tick", func() {
		procedure, sentTo := fixture([]ParkNudge{
			{SessionID: "s1", Name: "worker-a", Message: "still quiet"},
		}, errors.New(ctx, "roster gone"))

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultSkipped))
		gomega.Expect(effect.Reason).To(gomega.Equal("undelivered"))
		gomega.Expect(*sentTo).To(gomega.HaveLen(1))
	})

	ginkgo.It("surfaces a nudge read failure rather than reporting a quiet tick", func() {
		procedure := &ParkNudgeProcedure{
			Nudges: func(context.Context) ([]ParkNudge, error) {
				return nil, errors.New(ctx, "instrument absent")
			},
			Send: func(context.Context, string, string) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.DescribeTable("refuses a procedure wired without a collaborator",
		func(procedure *ParkNudgeProcedure) {
			_, err := procedure.Run(ctx)
			gomega.Expect(err).To(gomega.HaveOccurred())
		},
		ginkgo.Entry("no nudge source", &ParkNudgeProcedure{
			Send: func(context.Context, string, string) error { return nil },
		}),
		ginkgo.Entry("no send action", &ParkNudgeProcedure{
			Nudges: func(context.Context) ([]ParkNudge, error) { return nil, nil },
		}),
	)
})
