// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// A flagged headless row whose turn finished but whose child this process still
// holds reads UNKNOWN in session-liveness, which `decision` skips as not-absent.
// Measured 2026-11-18: 990b29b4 closed its turn at 09:17:08 and was skipped on
// every tick until a 16:23 restart killed the child. These cases drive the real
// reopener against a supervisor fake whose child table they set.
var _ = ginkgo.Describe("flagged tick: idle held child", func() {
	const sessionID = "990b29b4-held"
	var (
		supervisor *recordingSupervisor
		caps       *recordingCaps
		row        FlaggedRow
		tick       *FlaggedTick
	)

	ginkgo.BeforeEach(func() {
		supervisor = &recordingSupervisor{held: map[string]*Session{}}
		caps = &recordingCaps{}
		row = rowWith("agent", SpawnModeHeadless, "Fail CI When a Schema Is Defined in Go Source")
		row.SessionID = sessionID
		row.Liveness = "UNKNOWN"
		reopener := NewFlaggedReopener(
			supervisor, &recordingSpawner{}, testVaults(), &recordingLedger{},
			fixedNow(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)),
		)
		tick = newTick(staticSource{rows: []FlaggedRow{row}}, reopener, caps)
		nudger, ok := reopener.(FlaggedIdleNudger)
		gomega.Expect(ok).To(gomega.BeTrue())
		tick.Idle = nudger
	})

	hold := func(session Session) {
		session.ID = sessionID
		supervisor.held[sessionID] = &session
	}

	ginkgo.It("sends the work turn to an idle child and spawns nothing", func() {
		hold(Session{State: StateIdle})

		result := tick.tickFromRows(context.Background(), []FlaggedRow{row})

		ginkgo.GinkgoWriter.Printf("line=%s sent=%v\n", result.Line(), supervisor.sent())
		gomega.Expect(result.Nudged).To(gomega.Equal([]string{row.Key()}))
		gomega.Expect(result.Opened).To(gomega.BeEmpty())
		gomega.Expect(result.Skipped).To(gomega.BeEmpty())
		gomega.Expect(supervisor.sent()).To(gomega.Equal([]string{sessionID + ": " + reopenPrompt(row)}))
		gomega.Expect(supervisor.snapshot()).To(gomega.BeEmpty())
		gomega.Expect(caps.modes()).To(gomega.BeEmpty())
		gomega.Expect(caps.rateCalls).To(gomega.BeZero())
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("nudged=1"))
	})

	ginkgo.It("still nudges when the ledger says the session is terminal", func() {
		// The regression caught in review 2026-11-19. A finished turn writes a
		// TERMINAL ledger status, so the dead set is true for exactly the sessions
		// the idle-nudge exists for. Relabelling that skip as not-absent-dead must
		// not divert it away from the nudge — otherwise every idle held child
		// stops being nudged and stays dark until a restart, which is the bug the
		// nudge was added to fix. The pre-existing nudge specs use a nil-returning
		// ledger, so nothing else covers this.
		hold(Session{State: StateIdle})
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: sessionID, Status: "done",
			SpawnedAt: time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}}}

		result := tick.tickFromRows(context.Background(), []FlaggedRow{row})

		ginkgo.GinkgoWriter.Printf("line=%s sent=%v\n", result.Line(), supervisor.sent())
		gomega.Expect(result.Nudged).To(gomega.Equal([]string{row.Key()}))
		gomega.Expect(result.Skipped).To(gomega.BeEmpty())
	})

	ginkgo.DescribeTable("leaves a child alone that is not finished",
		func(session Session) {
			hold(session)

			result := tick.tickFromRows(context.Background(), []FlaggedRow{row})

			gomega.Expect(result.Nudged).To(gomega.BeEmpty())
			gomega.Expect(result.Skipped).To(gomega.Equal([]string{row.Key() + ": " + reasonNotAbsent}))
			gomega.Expect(supervisor.sent()).To(gomega.BeEmpty())
		},
		ginkgo.Entry("a turn is generating", Session{State: StateGenerating, PendingTurns: 1}),
		ginkgo.Entry("idle between messages with a turn still pending", Session{State: StateIdle, PendingTurns: 1}),
		ginkgo.Entry("parked on a question", Session{State: StateIdle, Question: &Question{}}),
		ginkgo.Entry("still starting", Session{State: StateStarting}),
	)

	ginkgo.It("skips a row whose child this process does not hold", func() {
		result := tick.tickFromRows(context.Background(), []FlaggedRow{row})

		gomega.Expect(result.Nudged).To(gomega.BeEmpty())
		gomega.Expect(result.Skipped).To(gomega.Equal([]string{row.Key() + ": " + reasonNotAbsent}))
		gomega.Expect(supervisor.sent()).To(gomega.BeEmpty())
	})

	ginkgo.It("does not nudge the same session again inside the window", func() {
		hold(Session{State: StateIdle})
		first := tick.tickFromRows(context.Background(), []FlaggedRow{row})
		hold(Session{State: StateIdle})

		second := tick.tickFromRows(context.Background(), []FlaggedRow{row})

		gomega.Expect(first.Nudged).To(gomega.HaveLen(1))
		gomega.Expect(second.Nudged).To(gomega.BeEmpty())
		gomega.Expect(second.Skipped).To(gomega.Equal([]string{row.Key() + ": " + reasonNotAbsent}))
		gomega.Expect(supervisor.sent()).To(gomega.HaveLen(1))
	})

	ginkgo.It("keeps the old skip when no nudger is wired", func() {
		hold(Session{State: StateIdle})
		tick.Idle = nil

		result := tick.tickFromRows(context.Background(), []FlaggedRow{row})

		gomega.Expect(result.Nudged).To(gomega.BeEmpty())
		gomega.Expect(result.Skipped).To(gomega.Equal([]string{row.Key() + ": " + reasonNotAbsent}))
	})

	ginkgo.It("records a failed send as a refusal, not a nudge", func() {
		hold(Session{State: StateIdle})
		supervisor.sendErr = ErrSessionNotFound

		result := tick.tickFromRows(context.Background(), []FlaggedRow{row})

		gomega.Expect(result.Nudged).To(gomega.BeEmpty())
		gomega.Expect(result.Refused).To(gomega.HaveLen(1))
		gomega.Expect(result.Refused[0]).To(gomega.ContainSubstring("nudge failed"))
	})
})
