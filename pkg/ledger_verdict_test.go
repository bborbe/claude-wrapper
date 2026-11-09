// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// ledgerVerdictSuite pins the half of the ledger write that the Go port dropped:
// a turn that closes files a terminal verdict, keyed by the child's own session
// id, and a turn that is re-opened says so BEFORE the turn is written.
//
// The child is a real shell process speaking the same JSONL protocol, because
// what is under test is the point in the event stream where the verdict is
// derived — a mock child would pin the mock, not the stream.
var _ = ginkgo.Describe("terminal ledger verdicts", func() {
	var (
		ctx   context.Context
		dir   string
		store SessionStore
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
		store = NewSessionStore(filepath.Join(dir, "sessions"))
	})

	// answeringChild announces itself, then answers every line it reads with the
	// result event the test names. `result` is the child's own terminal event, so
	// what the wrapper writes to the ledger must be this object verbatim.
	answeringChild := func(result string) string {
		path := filepath.Join(dir, "answering-child.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"m\",\"permissionMode\":\"auto\"}'\n" +
			"while IFS= read -r line; do\n" +
			"  printf '%s\\n' '" + result + "'\n" +
			"done\n"
		gomega.Expect(os.WriteFile(path, []byte(script), 0o700)).To(gomega.Succeed())
		return path
	}

	// silentChild announces itself and then reads its stdin forever without
	// answering, which is what a worker parked inside a long tool call looks like
	// from this side. Killing it leaves the turn open.
	silentChild := func() string {
		path := filepath.Join(dir, "silent-child.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"m\",\"permissionMode\":\"auto\"}'\n" +
			"while IFS= read -r line; do\n" +
			"  :\n" +
			"done\n"
		gomega.Expect(os.WriteFile(path, []byte(script), 0o700)).To(gomega.Succeed())
		return path
	}

	newSupervisor := func(bin string, ledger LedgerReader) *supervisor {
		return &supervisor{
			ctx:       ctx,
			claudeBin: bin,
			now:       libtime.NewCurrentDateTime(),
			selfURL:   "http://localhost:0",
			store:     store,
			sessions:  map[string]*child{},
			pending:   NewPending(),
			ledger:    ledger,
		}
	}

	// patchFor returns the last recorded patch for sessionID, or nil.
	patchFor := func(ledger *fakeLedger, sessionID string) LedgerPatch {
		var found LedgerPatch
		for _, record := range ledger.patchRecords() {
			if record.sessionID == sessionID {
				found = record.patch
			}
		}
		return found
	}

	// patchStatusOf decodes a patch's status field the way the store reads it.
	patchStatusOf := func(patch LedgerPatch) string {
		if patch == nil {
			return ""
		}
		raw, ok := patch["status"]
		if !ok {
			return ""
		}
		var status string
		gomega.Expect(json.Unmarshal(raw, &status)).To(gomega.Succeed())
		return status
	}

	ginkgo.It("files a done verdict carrying the child's own result event", func() {
		ledger := &fakeLedger{}
		sup := newSupervisor(
			answeringChild(`{"type":"result","subtype":"success","is_error":false}`),
			ledger,
		)
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(sup.Send(ctx, session.ID, "do the thing")).To(gomega.Succeed())
		defer func() { _ = sup.Stop(context.Background(), session.ID) }()

		gomega.Eventually(func() string {
			return patchStatusOf(patchFor(ledger, session.ID))
		}, "5s").Should(gomega.Equal("done"))

		patch := patchFor(ledger, session.ID)
		var result map[string]any
		gomega.Expect(json.Unmarshal(patch["result"], &result)).To(gomega.Succeed())
		gomega.Expect(result["subtype"]).To(gomega.Equal("success"))
		// ended_at is what makes the verdict a point in time rather than a claim;
		// a terminal row without one cannot be ordered against anything.
		gomega.Expect(patch).To(gomega.HaveKey("ended_at"))
		gomega.Expect(string(patch["ended_at"])).NotTo(gomega.Equal(`""`))
	})

	ginkgo.It("files an error verdict when the child's result says it failed", func() {
		ledger := &fakeLedger{}
		sup := newSupervisor(
			answeringChild(`{"type":"result","subtype":"error_max_turns","is_error":true}`),
			ledger,
		)
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(sup.Send(ctx, session.ID, "do the thing")).To(gomega.Succeed())
		defer func() { _ = sup.Stop(context.Background(), session.ID) }()

		gomega.Eventually(func() string {
			return patchStatusOf(patchFor(ledger, session.ID))
		}, "5s").Should(gomega.Equal("error"))
	})

	ginkgo.It("re-opens the row before writing the turn, so a fast result is the later write", func() {
		ledger := &fakeLedger{}
		sup := newSupervisor(
			answeringChild(`{"type":"result","subtype":"success","is_error":false}`),
			ledger,
		)
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(sup.Send(ctx, session.ID, "do the thing")).To(gomega.Succeed())
		defer func() { _ = sup.Stop(context.Background(), session.ID) }()

		gomega.Eventually(func() int {
			return len(ledger.patchRecords())
		}, "5s").Should(gomega.BeNumerically(">=", 2))

		// The FIRST write for this session must be the re-open. If the child's
		// result could overtake it, a finished turn would be left reading
		// `running` — the inversion this ordering exists to prevent.
		records := ledger.patchRecords()
		gomega.Expect(records[0].sessionID).To(gomega.Equal(session.ID))
		gomega.Expect(patchStatusOf(records[0].patch)).To(gomega.Equal("running"))
		gomega.Expect(string(records[0].patch["ended_at"])).To(gomega.Equal("null"))
		gomega.Expect(string(records[0].patch["result"])).To(gomega.Equal("null"))
		gomega.Expect(patchStatusOf(records[len(records)-1].patch)).To(gomega.Equal("done"))
	})

	ginkgo.It("files an error verdict for a turn the child left open", func() {
		ledger := &fakeLedger{}
		sup := newSupervisor(silentChild(), ledger)
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(sup.Send(ctx, session.ID, "do the thing")).To(gomega.Succeed())

		// The turn is open and will never close: the child is killed mid-turn,
		// which is the ported `catch` half of the node's terminal patch.
		gomega.Expect(sup.Stop(ctx, session.ID)).To(gomega.Succeed())

		gomega.Eventually(func() string {
			return patchStatusOf(patchFor(ledger, session.ID))
		}, "5s").Should(gomega.Equal("error"))

		var result map[string]any
		gomega.Expect(json.Unmarshal(patchFor(ledger, session.ID)["result"], &result)).
			To(gomega.Succeed())
		gomega.Expect(result["is_error"]).To(gomega.Equal(true))
		// The store has no `error` column and refuses a patch naming one, so the
		// cause rides inside `result` — an error verdict that names nothing is the
		// silent row this whole change exists to remove.
		gomega.Expect(result["error"]).NotTo(gomega.BeEmpty())
	})

	ginkgo.It("runs the child without a ledger when none is wired", func() {
		sup := newSupervisor(
			answeringChild(`{"type":"result","subtype":"success","is_error":false}`),
			nil,
		)
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(sup.Send(ctx, session.ID, "do the thing")).To(gomega.Succeed())
		defer func() { _ = sup.Stop(context.Background(), session.ID) }()
		gomega.Eventually(func() State {
			sup.mutex.RLock()
			c := sup.sessions[session.ID]
			sup.mutex.RUnlock()
			return c.snapshot().State
		}, "5s").Should(gomega.Equal(StateIdle))
	})

	// ⚠️ The adopt path is the one that can write a FALSE verdict: a successor
	// re-reads the whole event file, so every historical result passes through the
	// same apply() that files one. Without the replay flag, a restart would stamp a
	// fresh ended_at on a turn that ended hours ago — and flip a live mid-turn
	// `running` row to `done`.
	ginkgo.It("files no verdict for a result it merely replays on adoption", func() {
		ledger := &fakeLedger{}
		sup := newSupervisor(
			answeringChild(`{"type":"result","subtype":"success","is_error":false}`),
			ledger,
		)
		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(sup.Send(ctx, session.ID, "do the thing")).To(gomega.Succeed())
		defer func() { _ = sup.Stop(context.Background(), session.ID) }()

		gomega.Eventually(func() string {
			return patchStatusOf(patchFor(ledger, session.ID))
		}, "5s").Should(gomega.Equal("done"))

		// A successor daemon over the same store adopts the surviving child and
		// replays its event file, which contains that turn's result.
		successorLedger := &fakeLedger{}
		successor := newSupervisor(
			answeringChild(`{"type":"result","subtype":"success","is_error":false}`),
			successorLedger,
		)
		adopted, err := successor.Adopt(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(adopted).To(gomega.Equal(1))
		defer func() { _ = successor.Stop(context.Background(), session.ID) }()

		// Nothing is written: the row already carries that turn's verdict, and a
		// second write would be a new claim about an old turn.
		gomega.Consistently(func() int {
			return len(successorLedger.patchRecords())
		}, "1s").Should(gomega.Equal(0))
	})

	// The verdict is filed off the event loop, so it can still be in flight when
	// the next turn re-opens the row. The store has no compare-and-set, so the
	// guard lives here: a write for a superseded turn is dropped.
	ginkgo.It("drops a verdict whose turn has already been superseded", func() {
		ledger := &fakeLedger{}
		c := &child{id: "s1", now: libtime.NewCurrentDateTime(), ledger: ledger, ctx: ctx}
		c.patchVerdict(2, false, ctx, NewTurnStartedLedgerPatch())
		c.patchVerdict(
			1, true, ctx,
			NewTerminalLedgerPatch(
				"done", json.RawMessage(`{"subtype":"success"}`), "2026-11-09T00:00:00Z",
			),
		)
		gomega.Expect(ledger.patchRecords()).To(gomega.HaveLen(1))
		gomega.Expect(patchStatusOf(ledger.patchRecords()[0].patch)).To(gomega.Equal("running"))
	})

	// The mirror of the supersede rule, and the one a `turn < ledgerSeq` guard alone
	// gets wrong: a re-open for a turn that ALREADY has a terminal verdict must not
	// land, or a child that died between the counter increment and the re-open
	// leaves a dead turn reading `running`.
	ginkgo.It("never re-opens a turn that already has a terminal verdict", func() {
		ledger := &fakeLedger{}
		c := &child{id: "s1", now: libtime.NewCurrentDateTime(), ledger: ledger, ctx: ctx}
		c.patchVerdict(
			3, true, ctx,
			NewTerminalLedgerPatch(
				"error", json.RawMessage(`{"subtype":"error"}`), "2026-11-09T00:00:00Z",
			),
		)
		c.patchVerdict(3, false, ctx, NewTurnStartedLedgerPatch())
		gomega.Expect(ledger.patchRecords()).To(gomega.HaveLen(1))
		gomega.Expect(patchStatusOf(ledger.patchRecords()[0].patch)).To(gomega.Equal("error"))
	})

	// The send path must never queue behind a verdict write: a turn is a caller's
	// critical path, and a slow store would otherwise serialize every turn.
	ginkgo.It("skips the re-open rather than waiting for a write in flight", func() {
		ledger := &fakeLedger{}
		c := &child{id: "s1", now: libtime.NewCurrentDateTime(), ledger: ledger, ctx: ctx}
		c.ledgerMu.Lock()
		c.tryPatchVerdict(1, false, ctx, NewTurnStartedLedgerPatch())
		c.ledgerMu.Unlock()
		gomega.Expect(ledger.patchRecords()).To(gomega.BeEmpty())
	})

	// A result closes the OLDEST open turn, not the newest one requested. Taking
	// turnSeq here is the defect that let a stale result overwrite a newer turn's
	// re-open.
	ginkgo.It("associates a result with the turn it closes, not the newest one", func() {
		c := &child{}
		c.turnSeq = 2
		c.pendingTurns = 2
		// First result: two were open, so this closes turn 1 and leaves one open.
		c.pendingTurns--
		gomega.Expect(c.closedTurnLocked()).To(gomega.Equal(uint64(1)))
		// Second result closes turn 2.
		c.pendingTurns--
		gomega.Expect(c.closedTurnLocked()).To(gomega.Equal(uint64(2)))
	})
})
