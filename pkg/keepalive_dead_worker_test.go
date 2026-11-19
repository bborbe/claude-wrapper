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

var _ = ginkgo.Describe("DeadWorkerProcedure", func() {
	ctx := context.Background()

	// fixture wires the procedure with the three collaborators a test needs to
	// observe: the verdicts it reads, the claims it is granted and the sessions
	// it actually resumed.
	fixture := func(verdicts ...LivenessVerdict) (*DeadWorkerProcedure, *[]string) {
		resumed := &[]string{}
		return &DeadWorkerProcedure{
			Verdicts: func(context.Context) ([]LivenessVerdict, error) { return verdicts, nil },
			Claim:    func(context.Context, string) (bool, error) { return true, nil },
			Resume: func(_ context.Context, sessionID string) error {
				*resumed = append(*resumed, sessionID)
				return nil
			},
		}, resumed
	}

	ginkgo.It("resumes the first session the verdict reads not-live", func() {
		procedure, resumed := fixture(
			LivenessVerdict{SessionID: "live-1", Liveness: LivenessLive, Source: "registry"},
			LivenessVerdict{SessionID: "dead-1", Liveness: LivenessNotLive, Source: "child-table"},
			LivenessVerdict{SessionID: "dead-2", Liveness: LivenessNotLive, Source: "none"},
		)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultResumed,
			SessionID: "dead-1",
		}))
		gomega.Expect(*resumed).To(gomega.Equal([]string{"dead-1"}))
	})

	// ⚠️ One per tick, not the whole backlog. A daemon restart leaves every child
	// exited at once; resuming them all in one tick is the load spike the
	// 3-resumes-in-30-minutes cap exists to prevent.
	ginkgo.It("resumes at most one session per tick", func() {
		procedure, resumed := fixture(
			LivenessVerdict{SessionID: "dead-1", Liveness: LivenessNotLive, Source: "child-table"},
			LivenessVerdict{SessionID: "dead-2", Liveness: LivenessNotLive, Source: "child-table"},
		)

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*resumed).To(gomega.Equal([]string{"dead-1"}))
	})

	ginkgo.It("reports none when every session is live", func() {
		procedure, resumed := fixture(
			LivenessVerdict{SessionID: "live-1", Liveness: LivenessLive, Source: "registry"},
		)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
		gomega.Expect(*resumed).To(gomega.BeEmpty())
	})

	// ⚠️ The double-actor guard. Between this job going live and the round's rows
	// being removed, both the tick and the model round act on one population. A
	// procedure that resumed on a refused claim would produce exactly the double
	// resume the claim exists to stop.
	ginkgo.It("skips with reason=claim-taken when another writer holds the claim", func() {
		resumed := &[]string{}
		procedure := &DeadWorkerProcedure{
			Verdicts: func(context.Context) ([]LivenessVerdict, error) {
				return []LivenessVerdict{
					{SessionID: "dead-1", Liveness: LivenessNotLive, Source: "child-table"},
				}, nil
			},
			Claim: func(context.Context, string) (bool, error) { return false, nil },
			Resume: func(_ context.Context, sessionID string) error {
				*resumed = append(*resumed, sessionID)
				return nil
			},
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultSkipped,
			SessionID: "dead-1",
			Reason:    "claim-taken",
		}))
		gomega.Expect(*resumed).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a verdict read failure rather than reporting a quiet tick", func() {
		procedure := &DeadWorkerProcedure{
			Verdicts: func(context.Context) ([]LivenessVerdict, error) {
				return nil, errors.New(ctx, "registry unreadable")
			},
			Claim:  func(context.Context, string) (bool, error) { return true, nil },
			Resume: func(context.Context, string) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a resume failure rather than reporting a resume", func() {
		procedure := &DeadWorkerProcedure{
			Verdicts: func(context.Context) ([]LivenessVerdict, error) {
				return []LivenessVerdict{
					{SessionID: "dead-1", Liveness: LivenessNotLive, Source: "child-table"},
				}, nil
			},
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Resume: func(context.Context, string) error {
				return errors.New(ctx, "spawn refused")
			},
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.DescribeTable("refuses a procedure wired without a collaborator",
		func(procedure *DeadWorkerProcedure) {
			_, err := procedure.Run(ctx)
			gomega.Expect(err).To(gomega.HaveOccurred())
		},
		ginkgo.Entry("no verdict source", &DeadWorkerProcedure{
			Claim:  func(context.Context, string) (bool, error) { return true, nil },
			Resume: func(context.Context, string) error { return nil },
		}),
		ginkgo.Entry("no claim source", &DeadWorkerProcedure{
			Verdicts: func(context.Context) ([]LivenessVerdict, error) { return nil, nil },
			Resume:   func(context.Context, string) error { return nil },
		}),
		ginkgo.Entry("no resume action", &DeadWorkerProcedure{
			Verdicts: func(context.Context) ([]LivenessVerdict, error) { return nil, nil },
			Claim:    func(context.Context, string) (bool, error) { return true, nil },
		}),
	)
})
