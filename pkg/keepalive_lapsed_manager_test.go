// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("LapsedManagerProcedure", func() {
	ctx := context.Background()

	type act struct{ verdict, sessionID string }

	// fixture wires a decision and records the acts, so a test asserts both the
	// effect and that nothing acted when nothing should have.
	fixture := func(decision LapsedManagerDecision, claimGranted bool) (*LapsedManagerProcedure, *[]act) {
		acts := &[]act{}
		return &LapsedManagerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) { return decision, nil },
			Claim:  func(context.Context, string) (bool, error) { return claimGranted, nil },
			Act: func(_ context.Context, verdict string, decision LapsedManagerDecision) error {
				*acts = append(*acts, act{verdict: verdict, sessionID: decision.SessionID})
				return nil
			},
		}, acts
	}

	ginkgo.DescribeTable("acts only on the words that license an act",
		func(verdict string, want KeepaliveResult, wantActs int) {
			procedure, acts := fixture(LapsedManagerDecision{Verdict: verdict, SessionID: "s1"}, true)

			effect, err := procedure.Run(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(effect.Result).To(gomega.Equal(want))
			gomega.Expect(*acts).To(gomega.HaveLen(wantActs))
		},
		// The two that act.
		ginkgo.Entry("resume acts", "resume", KeepaliveResultResumed, 1),
		ginkgo.Entry("restart acts", "restart", KeepaliveResultRestarted, 1),
		// ⚠️ The guards. `leave` is a deliberate stand-down and `jump` is a live
		// armed loop; neither may be restarted, which is what a zero act count
		// proves.
		ginkgo.Entry("leave does not act", "leave", KeepaliveResultKept, 0),
		ginkgo.Entry("jump does not act", "jump", KeepaliveResultKept, 0),
		ginkgo.Entry("cannot-tell does not act", "cannot-tell", KeepaliveResultSkipped, 0),
		// ⚠️ The cap. A spent resume budget escalates; it does not restart again.
		ginkgo.Entry("crash-loop does not act", "crash-loop", KeepaliveResultCapped, 0),
		ginkgo.Entry("claimed does not act", "claimed", KeepaliveResultSkipped, 0),
	)

	ginkgo.It("skips without acting when another writer holds the claim", func() {
		procedure, acts := fixture(LapsedManagerDecision{Verdict: "resume", SessionID: "s1"}, false)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultSkipped,
			SessionID: "s1",
			Reason:    "claimed",
		}))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	// hold → claim → act: a held session is skipped with reason=hold, an unknown one
	// with reason=hold-unknown (distinct, per SC2), and the claim is NOT taken.
	ginkgo.DescribeTable("skips a held session before the claim",
		func(state HoldState, wantReason string) {
			claimed := false
			procedure := &LapsedManagerProcedure{
				Decide: func(context.Context) (LapsedManagerDecision, error) {
					return LapsedManagerDecision{Verdict: "resume", SessionID: "s1"}, nil
				},
				Claim: func(context.Context, string) (bool, error) {
					claimed = true
					return true, nil
				},
				Hold: func(context.Context, string) (HoldState, error) { return state, nil },
				Act:  func(context.Context, string, LapsedManagerDecision) error { return nil },
			}

			effect, err := procedure.Run(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
				Result: KeepaliveResultSkipped, SessionID: "s1", Reason: wantReason,
			}))
			gomega.Expect(claimed).To(gomega.BeFalse())
		},
		ginkgo.Entry("held", HoldHeld, "hold"),
		ginkgo.Entry("unknown", HoldUnknown, "hold-unknown"),
	)

	ginkgo.It("surfaces a hold-check failure rather than acting", func() {
		procedure := &LapsedManagerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) {
				return LapsedManagerDecision{Verdict: "resume", SessionID: "s1"}, nil
			},
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Hold: func(context.Context, string) (HoldState, error) {
				return HoldNotHeld, errors.New(ctx, "hold store unreadable")
			},
			Act: func(context.Context, string, LapsedManagerDecision) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	// ⚠️ A `resume` enqueues a later-tick record; a `restart` re-drives a live
	// session and enqueues nothing.
	ginkgo.DescribeTable("enqueues a pending record only on resume",
		func(verdict string, wantRecords int) {
			store := NewPendingRecordDir(ginkgo.GinkgoT().TempDir())
			procedure := &LapsedManagerProcedure{
				Decide: func(context.Context) (LapsedManagerDecision, error) {
					return LapsedManagerDecision{Verdict: verdict, SessionID: "s1", Vault: "v", Name: "n"}, nil
				},
				Claim:   func(context.Context, string) (bool, error) { return true, nil },
				Act:     func(context.Context, string, LapsedManagerDecision) error { return nil },
				Pending: store,
				Now:     libtime.NewCurrentDateTime(),
			}

			_, err := procedure.Run(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			records, listErr := store.List(ctx)
			gomega.Expect(listErr).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(records).To(gomega.HaveLen(wantRecords))
		},
		ginkgo.Entry("resume enqueues", "resume", 1),
		ginkgo.Entry("restart enqueues nothing", "restart", 0),
	)

	ginkgo.It("acts on claim-taken, which means this writer won the claim", func() {
		procedure, acts := fixture(LapsedManagerDecision{Verdict: "claim-taken", SessionID: "s1"}, true)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultResumed))
		gomega.Expect(*acts).To(gomega.Equal([]act{{verdict: "resume", sessionID: "s1"}}))
	})

	ginkgo.It("refuses an act verdict that names no session", func() {
		procedure, acts := fixture(LapsedManagerDecision{Verdict: "resume"}, true)

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a decision failure rather than reporting a quiet tick", func() {
		procedure := &LapsedManagerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) {
				return LapsedManagerDecision{}, errors.New(ctx, "script crashed")
			},
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Act:   func(context.Context, string, LapsedManagerDecision) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces an act failure rather than reporting a resume", func() {
		procedure := &LapsedManagerProcedure{
			Decide: func(context.Context) (LapsedManagerDecision, error) {
				return LapsedManagerDecision{Verdict: "resume", SessionID: "s1"}, nil
			},
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Act: func(context.Context, string, LapsedManagerDecision) error {
				return errors.New(ctx, "spawn refused")
			},
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("defaults its action name to the topic tier", func() {
		procedure, _ := fixture(LapsedManagerDecision{}, true)
		gomega.Expect(procedure.Name()).To(gomega.Equal("lapsed-topic-manager"))
	})
})
