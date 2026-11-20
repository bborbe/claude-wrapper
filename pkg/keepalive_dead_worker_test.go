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

var _ = ginkgo.Describe("DeadWorkerProcedure", func() {
	ctx := context.Background()

	type actCall struct {
		verdict  string
		decision LapsedManagerDecision
	}

	// fixture wires the procedure with the three collaborators a test observes:
	// the per-task decisions the script answered, the claims it is granted and the
	// acts it actually performed.
	fixture := func(decisions ...DeadWorkerDecision) (*DeadWorkerProcedure, *[]actCall) {
		acts := &[]actCall{}
		return &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) { return decisions, nil },
			Claim:     func(context.Context, string) (bool, error) { return true, nil },
			Act: func(_ context.Context, verdict string, decision LapsedManagerDecision) error {
				*acts = append(*acts, actCall{verdict: verdict, decision: decision})
				return nil
			},
		}, acts
	}

	// ⚠️ The decision is the SCRIPT's per-task verdict, and the resume opens the
	// worker BY TASK — so the act must carry the vault and task name, not just the
	// session id.
	ginkgo.It("resumes the first task the script marks resume, opening by task", func() {
		procedure, acts := fixture(
			DeadWorkerDecision{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "jump"},
			DeadWorkerDecision{Vault: "v", Task: "t2", SessionID: "s2", Verdict: "resume"},
			DeadWorkerDecision{Vault: "v", Task: "t3", SessionID: "s3", Verdict: "resume"},
		)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultResumed,
			SessionID: "s2",
		}))
		gomega.Expect(*acts).To(gomega.Equal([]actCall{{
			verdict:  "resume",
			decision: LapsedManagerDecision{Vault: "v", Name: "t2", Branch: "", SessionID: "s2"},
		}}))
	})

	// ⚠️ One act per tick, not the whole backlog: a daemon restart leaves every
	// child exited at once, and resuming them all in one tick is the load spike the
	// 3-resumes-in-30-minutes cap exists to prevent.
	ginkgo.It("resumes at most one task per tick", func() {
		procedure, acts := fixture(
			DeadWorkerDecision{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"},
			DeadWorkerDecision{Vault: "v", Task: "t2", SessionID: "s2", Verdict: "resume"},
		)

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*acts).To(gomega.HaveLen(1))
		gomega.Expect((*acts)[0].decision.Name).To(gomega.Equal("t1"))
	})

	// ⚠️ No head-of-line blocking: a lost claim on the FIRST candidate must not stop
	// the scan — otherwise one already-claimed worker blocks every other dead one
	// forever. The second candidate (claim won) is the one acted on.
	ginkgo.It("moves past a lost claim to the next candidate", func() {
		acts := &[]actCall{}
		claims := 0
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return []DeadWorkerDecision{
					{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"},
					{Vault: "v", Task: "t2", SessionID: "s2", Verdict: "resume"},
				}, nil
			},
			Claim: func(context.Context, string) (bool, error) {
				claims++
				return claims > 1, nil // first lost, second won
			},
			Act: func(_ context.Context, verdict string, decision LapsedManagerDecision) error {
				*acts = append(*acts, actCall{verdict: verdict, decision: decision})
				return nil
			},
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{Result: KeepaliveResultResumed, SessionID: "s2"}))
		gomega.Expect((*acts)[0].decision.SessionID).To(gomega.Equal("s2"))
	})

	// ⚠️ hold → claim → act: a held candidate is skipped (reason=hold) and the scan
	// CONTINUES to the next, which is acted on.
	ginkgo.It("skips a held candidate and acts on the next", func() {
		acts := &[]actCall{}
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return []DeadWorkerDecision{
					{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"},
					{Vault: "v", Task: "t2", SessionID: "s2", Verdict: "resume"},
				}, nil
			},
			Hold: func(_ context.Context, sessionID string) (HoldState, error) {
				if sessionID == "s1" {
					return HoldHeld, nil
				}
				return HoldNotHeld, nil
			},
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Act: func(_ context.Context, verdict string, decision LapsedManagerDecision) error {
				*acts = append(*acts, actCall{verdict: verdict, decision: decision})
				return nil
			},
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{Result: KeepaliveResultResumed, SessionID: "s2"}))
		gomega.Expect((*acts)[0].decision.SessionID).To(gomega.Equal("s2"))
	})

	// A resume enqueues a later-tick record carrying the worker's Vault + Task, so the
	// drain records through `worker-keepalive.py --vault --task --record-resume`.
	ginkgo.It("enqueues a worker pending record on resume", func() {
		store := NewPendingRecordDir(ginkgo.GinkgoT().TempDir())
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return []DeadWorkerDecision{{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"}}, nil
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
		gomega.Expect(records).To(gomega.HaveLen(1))
		gomega.Expect(records[0].Vault).To(gomega.Equal("v"))
		gomega.Expect(records[0].Task).To(gomega.Equal("t1"))
		gomega.Expect(records[0].Kind).To(gomega.Equal("worker"))
	})
	ginkgo.It("reports the first hold skip when every candidate is held", func() {
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return []DeadWorkerDecision{
					{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"},
					{Vault: "v", Task: "t2", SessionID: "s2", Verdict: "resume"},
				}, nil
			},
			Hold:  func(context.Context, string) (HoldState, error) { return HoldUnknown, nil },
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Act:   func(context.Context, string, LapsedManagerDecision) error { return nil },
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result: KeepaliveResultSkipped, SessionID: "s1", Reason: "hold-unknown",
		}))
	})
	ginkgo.It("reports skipped claimed when every candidate's claim is lost", func() {
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return []DeadWorkerDecision{
					{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"},
					{Vault: "v", Task: "t2", SessionID: "s2", Verdict: "resume"},
				}, nil
			},
			Claim: func(context.Context, string) (bool, error) { return false, nil },
			Act:   func(context.Context, string, LapsedManagerDecision) error { return nil },
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result: KeepaliveResultSkipped, SessionID: "s1", Reason: "claimed",
		}))
	})

	// A resume row naming no session has nothing to resume — skip it, do not claim
	// or act on an empty id.
	ginkgo.It("skips a resume row with no session id without acting", func() {
		procedure, acts := fixture(
			DeadWorkerDecision{Vault: "v", Task: "t1", SessionID: "", Verdict: "resume"},
		)

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	// Nothing licenses an act → report the first decision's own effect, so a
	// crash-loop (capped) is visible rather than a bare none.
	ginkgo.It("reports the first decision's effect when nothing acts", func() {
		procedure, acts := fixture(
			DeadWorkerDecision{Vault: "v", Task: "t1", Verdict: "crash-loop"},
		)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultCapped))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	ginkgo.It("reports none when there are no decisions", func() {
		procedure, acts := fixture()

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
		gomega.Expect(*acts).To(gomega.BeEmpty())
	})

	// ⚠️ The double-actor guard. A procedure that resumed on a refused claim would
	// produce exactly the double resume the claim exists to stop.
	ginkgo.It("skips with reason=claimed when another writer holds the claim", func() {
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return []DeadWorkerDecision{{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"}}, nil
			},
			Claim: func(context.Context, string) (bool, error) { return false, nil },
			Act:   func(context.Context, string, LapsedManagerDecision) error { return nil },
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultSkipped,
			SessionID: "s1",
			Reason:    "claimed",
		}))
	})

	ginkgo.It("surfaces a decision read failure rather than reporting a quiet tick", func() {
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return nil, errors.New(ctx, "script crashed")
			},
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Act:   func(context.Context, string, LapsedManagerDecision) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces an act failure rather than reporting a resume", func() {
		procedure := &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) {
				return []DeadWorkerDecision{{Vault: "v", Task: "t1", SessionID: "s1", Verdict: "resume"}}, nil
			},
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Act: func(context.Context, string, LapsedManagerDecision) error {
				return errors.New(ctx, "open refused")
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
		ginkgo.Entry("no decision source", &DeadWorkerProcedure{
			Claim: func(context.Context, string) (bool, error) { return true, nil },
			Act:   func(context.Context, string, LapsedManagerDecision) error { return nil },
		}),
		ginkgo.Entry("no claim source", &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) { return nil, nil },
			Act:       func(context.Context, string, LapsedManagerDecision) error { return nil },
		}),
		ginkgo.Entry("no act", &DeadWorkerProcedure{
			Decisions: func(context.Context) ([]DeadWorkerDecision, error) { return nil, nil },
			Claim:     func(context.Context, string) (bool, error) { return true, nil },
		}),
	)
})
