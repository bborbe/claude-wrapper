// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("NewTopiclessGoalManagerProcedure", func() {
	ctx := context.Background()

	// The tier's only difference from the topic tier is the name it writes and
	// the population its decision reads. This suite pins the name and re-proves
	// the two guards that would be silently lost if someone "simplified" the
	// constructor into a second implementation.
	fixture := func(decision LapsedManagerDecision, claimGranted bool) (KeepaliveProcedure, *[]string) {
		acts := &[]string{}
		procedure := NewTopiclessGoalManagerProcedure(
			func(context.Context) (LapsedManagerDecision, error) { return decision, nil },
			func(context.Context, string) (bool, error) { return claimGranted, nil },
			func(_ context.Context, verdict, sessionID string) error {
				*acts = append(*acts, verdict+" "+sessionID)
				return nil
			},
		)
		return procedure, acts
	}

	ginkgo.It("writes the topicless action name, not the topic tier's", func() {
		procedure, _ := fixture(LapsedManagerDecision{}, true)
		gomega.Expect(procedure.Name()).To(gomega.Equal("topicless-goal-manager"))
	})

	ginkgo.It("resumes on the resume verdict", func() {
		procedure, acts := fixture(
			LapsedManagerDecision{Verdict: "resume", SessionID: "g1"}, true,
		)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultResumed))
		gomega.Expect(effect.SessionID).To(gomega.Equal("g1"))
		gomega.Expect(*acts).To(gomega.Equal([]string{"resume g1"}))
	})

	// ⚠️ The same guard as the topic tier, re-proved here: a deliberate stand-down
	// must not be restarted by the topicless leg either.
	ginkgo.It("does not act on leave", func() {
		procedure, acts := fixture(
			LapsedManagerDecision{Verdict: "leave", SessionID: "g1"}, true,
		)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultKept))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	// ⚠️ And the claim guard: the two tiers share one session population with the
	// model round, so a refused claim must not act here either.
	ginkgo.It("skips without acting when the claim is refused", func() {
		procedure, acts := fixture(
			LapsedManagerDecision{Verdict: "resume", SessionID: "g1"}, false,
		)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultSkipped))
		gomega.Expect(effect.Reason).To(gomega.Equal("claimed"))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})
})
