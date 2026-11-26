// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// errTestWrite stands in for a `vault-cli` that refused the defer.
var errTestWrite = stderrors.New("vault-cli refused")

// recordingDefer is the DeferWriter the specs below assert against.
type recordingDefer struct {
	calls []deferCall
	err   error
}

type deferCall struct {
	key      string
	deadline time.Time
}

func (d *recordingDefer) WriteDefer(_ context.Context, row FlaggedRow, deadline time.Time) error {
	d.calls = append(d.calls, deferCall{key: row.Key(), deadline: deadline})
	return d.err
}

// deferGate builds the gate the production constructor builds, with the writer
// swapped for the recorder. The constructor returns the interface, so the specs
// build the struct directly — same package, same fields.
func deferGate(
	sessionsDir, parkedDir string,
	clock libtime.CurrentDateTimeGetter,
	deferWriter DeferWriter,
) *parkedGate {
	return &parkedGate{
		SessionsDir: sessionsDir,
		ParkedDir:   parkedDir,
		Store:       NewParkedStore(parkedDir),
		Cards:       CardProbe{},
		PRs:         PRProbe{},
		Hosts:       HostProbe{},
		Clocks:      ClockProbe{Now: clock},
		Now:         clock,
		Logf:        func(string, ...any) {},
		Defer:       deferWriter,
	}
}

var _ = ginkgo.Describe("parked gate — the defer write", func() {
	var (
		ctx         context.Context
		sessionsDir string
		parkedDir   string
		clock       libtime.CurrentDateTime
		row         FlaggedRow
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)))
		sessionsDir = ginkgo.GinkgoT().TempDir()
		parkedDir = ginkgo.GinkgoT().TempDir()
		row = FlaggedRow{
			Kind:      "task",
			Vault:     "private-personal",
			Name:      "Lower lib-k8s Default CPU Request to 10m",
			SessionID: parkedGateSessionID,
		}
	})

	ginkgo.It("writes the parsed deadline for a parked-until row", func() {
		writeSessionCloser(sessionsDir, parkedGateSessionID,
			"⏰ Ends: 2026-12-01T09:00:00+01:00 — the soak window closes")
		rec := &recordingDefer{}

		reason, held := deferGate(sessionsDir, parkedDir, clock, rec).Hold(ctx, row)

		gomega.Expect(held).To(gomega.BeTrue(), "a future clock still holds the row")
		gomega.Expect(reason).To(gomega.HavePrefix("parked-until"))
		gomega.Expect(rec.calls).To(gomega.HaveLen(1))
		gomega.Expect(rec.calls[0].key).To(gomega.Equal(row.Key()))
		gomega.Expect(rec.calls[0].deadline.Format(time.RFC3339)).
			To(gomega.Equal("2026-12-01T09:00:00+01:00"),
				"the full instant, never cut to a date")
	})

	// ⚠️ All three kinds, one code path. The write reads `row.Vault`/`row.Name`, so a
	// kind it does not know about would still "work" here and fail only against a real
	// vault — the fixtures exist so the three kinds are exercised rather than assumed.
	for _, kind := range []string{"task", "goal", "topic"} {
		kind := kind
		ginkgo.It("writes the same way for a "+kind+" row", func() {
			writeSessionCloser(sessionsDir, parkedGateSessionID,
				"⏰ Ends: 2026-12-01T09:00:00+01:00 — the soak window closes")
			rec := &recordingDefer{}
			kindRow := row
			kindRow.Kind = kind

			_, held := deferGate(sessionsDir, parkedDir, clock, rec).Hold(ctx, kindRow)

			gomega.Expect(held).To(gomega.BeTrue())
			gomega.Expect(rec.calls).To(gomega.HaveLen(1))
			gomega.Expect(rec.calls[0].key).To(gomega.Equal(kindRow.Key()))
		})
	}

	ginkgo.It("writes nothing once the clock has passed", func() {
		writeSessionCloser(sessionsDir, parkedGateSessionID,
			"⏰ Ends: 2026-11-26T09:00:00+01:00 — the soak window closes")
		rec := &recordingDefer{}

		reason, held := deferGate(sessionsDir, parkedDir, clock, rec).Hold(ctx, row)

		gomega.Expect(held).To(gomega.BeFalse())
		gomega.Expect(reason).To(gomega.BeEmpty())
		gomega.Expect(rec.calls).To(gomega.BeEmpty(),
			"a satisfied clock opens the row; there is nothing to defer")
	})

	ginkgo.It("writes nothing for a parked-on-card row", func() {
		writeSessionCloser(sessionsDir, parkedGateSessionID,
			"⏰ Ends: attention card 09da8ab55138f4059c7a928d6da35978 is answered")
		rec := &recordingDefer{}

		_, _ = deferGate(sessionsDir, parkedDir, clock, rec).Hold(ctx, row)

		gomega.Expect(rec.calls).To(gomega.BeEmpty(),
			"a card park is released by its probe, never by a clock")
	})

	ginkgo.It("writes nothing when the closer carries no clock", func() {
		writeSessionCloser(sessionsDir, parkedGateSessionID,
			"⏰ Ends: something this build cannot probe")
		rec := &recordingDefer{}

		_, _ = deferGate(sessionsDir, parkedDir, clock, rec).Hold(ctx, row)

		gomega.Expect(rec.calls).To(gomega.BeEmpty(),
			"a nil Deadline is the cannot-address fallback and must write nothing")
	})

	ginkgo.It("keeps the hold when the write fails", func() {
		writeSessionCloser(sessionsDir, parkedGateSessionID,
			"⏰ Ends: 2026-12-01T09:00:00+01:00 — the soak window closes")
		rec := &recordingDefer{err: errTestWrite}

		reason, held := deferGate(sessionsDir, parkedDir, clock, rec).Hold(ctx, row)

		gomega.Expect(held).To(gomega.BeTrue(), "fail-open: the hold is unchanged")
		gomega.Expect(reason).To(gomega.HavePrefix("parked-until"))
	})

	ginkgo.It("holds without panicking when no writer is configured", func() {
		writeSessionCloser(sessionsDir, parkedGateSessionID,
			"⏰ Ends: 2026-12-01T09:00:00+01:00 — the soak window closes")

		_, held := deferGate(sessionsDir, parkedDir, clock, nil).Hold(ctx, row)

		gomega.Expect(held).To(gomega.BeTrue(), "a nil Defer is the rollback path")
	})
})

var _ = ginkgo.Describe("VaultCLIDeferWriter", func() {
	ginkgo.It("shells the row's vault, name and full instant", func() {
		runner := &recordingRunner{}
		deadline := time.Date(2026, 12, 1, 9, 0, 0, 0, time.FixedZone("CET", 3600))
		row := FlaggedRow{Kind: "task", Vault: "private-personal", Name: "Row Name"}

		gomega.Expect(
			VaultCLIDeferWriter{Runner: runner}.WriteDefer(context.Background(), row, deadline),
		).To(gomega.Succeed())

		gomega.Expect(runner.argv).To(gomega.Equal([]string{
			"vault-cli", "--vault", "private-personal",
			"task", "defer", "Row Name", "2026-12-01T09:00:00+01:00",
		}))
	})

	ginkgo.It("refuses a row with no vault or name", func() {
		runner := &recordingRunner{}
		row := FlaggedRow{Kind: "task", Vault: "", Name: ""}

		gomega.Expect(
			VaultCLIDeferWriter{Runner: runner}.WriteDefer(context.Background(), row, time.Now()),
		).NotTo(gomega.Succeed())
		gomega.Expect(runner.argv).To(gomega.BeEmpty())
	})
})
