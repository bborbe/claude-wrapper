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

var _ = ginkgo.Describe("DecisionParkedWorkerProcedure", func() {
	ctx := context.Background()

	fixture := func(decision LapsedManagerDecision) (*DecisionParkedWorkerProcedure, *[]string) {
		acts := &[]string{}
		return &DecisionParkedWorkerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) { return decision, nil },
			Act: func(_ context.Context, verdict string, decision LapsedManagerDecision) error {
				*acts = append(*acts, verdict+" "+decision.SessionID)
				return nil
			},
		}, acts
	}

	ginkgo.It("resumes on the script's resume row", func() {
		procedure, acts := fixture(LapsedManagerDecision{Verdict: "resume", SessionID: "w1"})

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultResumed))
		gomega.Expect(effect.SessionID).To(gomega.Equal("w1"))
		gomega.Expect(*acts).To(gomega.Equal([]string{"resume w1"}))
	})

	// ⚠️ The script's own cap. `crash-loop` is its spent-budget word and maps to
	// `capped`; it must never reach the act, or the cap would be spent again.
	ginkgo.It("reports capped without acting on the script's crash-loop row", func() {
		procedure, acts := fixture(LapsedManagerDecision{Verdict: "crash-loop", SessionID: "w1"})

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultCapped))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	ginkgo.It("does not act on a leave row", func() {
		procedure, acts := fixture(LapsedManagerDecision{Verdict: "leave", SessionID: "w1"})

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultKept))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a resume row that names no session", func() {
		procedure, acts := fixture(LapsedManagerDecision{Verdict: "resume"})

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a decision failure rather than reporting a quiet tick", func() {
		procedure := &DecisionParkedWorkerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) {
				return LapsedManagerDecision{}, errors.New(ctx, "script crashed")
			},
			Act: func(context.Context, string, LapsedManagerDecision) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces an act failure rather than reporting a resume", func() {
		procedure := &DecisionParkedWorkerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) {
				return LapsedManagerDecision{Verdict: "resume", SessionID: "w1"}, nil
			},
			Act: func(context.Context, string, LapsedManagerDecision) error {
				return errors.New(ctx, "spawn refused")
			},
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.DescribeTable("refuses a procedure wired without a collaborator",
		func(procedure *DecisionParkedWorkerProcedure) {
			_, err := procedure.Run(ctx)
			gomega.Expect(err).To(gomega.HaveOccurred())
		},
		ginkgo.Entry("no decision source", &DecisionParkedWorkerProcedure{
			Act: func(context.Context, string, LapsedManagerDecision) error { return nil },
		}),
		ginkgo.Entry("no act", &DecisionParkedWorkerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) {
				return LapsedManagerDecision{}, nil
			},
		}),
	)
})
