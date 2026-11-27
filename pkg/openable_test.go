// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Openable", func() {
	// PR 1 shape test. The full F1–F5 fixture table lives in PR 2: F3 (expects
	// `open`) and F5 (expects reason `indeterminate`) encode the classifier-input
	// swap that PR 2 introduces, so they cannot pass on PR 1's branch. Here we pin
	// only what the current tick deterministically yields.
	ginkgo.It("reports the tick's decision per row", func() {
		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		rows := []FlaggedRow{
			{Kind: "task", Vault: "private-personal", Name: "f1-never-opened", Branch: "create", Liveness: "none", Phase: "planning", FlagSetBy: "operator"},
			{Kind: "task", Vault: "private-personal", Name: "f2-fresh", Branch: "resume", Liveness: "LIVE", SessionID: "11111111-1111-1111-1111-111111111111", Phase: "execution", FlagSetBy: "operator"},
			{Kind: "task", Vault: "private-personal", Name: "f4-blocked", Branch: "create", Liveness: "none", BlockedBy: []string{"[[x]]"}, FlagSetBy: "operator"},
		}
		tick := newTick(staticSource{rows: rows}, nil, nil)
		tick.Now = fixedNow(now)

		got, err := tick.Openable(context.Background())
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(got).To(gomega.HaveLen(3))

		byName := map[string]OpenableEntry{}
		for _, entry := range got {
			byName[entry.Name] = entry
			gomega.Expect(entry.Decision).To(
				gomega.BeElementOf(OpenableDecisionOpen, OpenableDecisionHeld))
			if entry.Decision == OpenableDecisionHeld {
				gomega.Expect(entry.Reason).ToNot(gomega.BeEmpty())
			}
		}
		gomega.Expect(byName["f1-never-opened"].Decision).To(gomega.Equal(OpenableDecisionOpen))
		gomega.Expect(byName["f2-fresh"].Decision).To(gomega.Equal(OpenableDecisionHeld))
		gomega.Expect(byName["f2-fresh"].Reason).To(gomega.Equal(reasonNotAbsent))
		gomega.Expect(byName["f4-blocked"].Decision).To(gomega.Equal(OpenableDecisionHeld))
		gomega.Expect(byName["f4-blocked"].Reason).To(gomega.Equal(reasonBlocked))
	})
})

// countingOpenExecutor counts the rows the tick actually hands to OpenTick.
type countingOpenExecutor struct{ opened int }

func (c *countingOpenExecutor) OpenRequest(
	context.Context, []OpenRow, string,
) []OpenVerdict {
	return nil
}

func (c *countingOpenExecutor) OpenTick(_ context.Context, rows []OpenRow) []OpenVerdict {
	c.opened += len(rows)
	return nil
}

var _ = ginkgo.Describe("Openable negative probe", func() {
	// One full tick cycle: F1 must open (positive control), F2 and F4 must not.
	ginkgo.It("opens only F1, never F2 or F4", func() {
		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		rows := []FlaggedRow{
			{Kind: "task", Vault: "private-personal", Name: "f1", Branch: "create", Liveness: "none", Phase: "planning"},
			{Kind: "task", Vault: "private-personal", Name: "f2", Branch: "resume", Liveness: "LIVE", SessionID: "11111111-1111-1111-1111-111111111111", Phase: "execution"},
			{Kind: "task", Vault: "private-personal", Name: "f4", Branch: "create", Liveness: "none", BlockedBy: []string{"[[x]]"}},
		}
		opener := &countingOpenExecutor{}
		tick := newTick(staticSource{rows: rows}, nil, &recordingCaps{})
		tick.Open = opener
		tick.Now = fixedNow(now)

		tick.tickFromRows(context.Background(), rows)

		gomega.Expect(opener.opened).To(gomega.Equal(1))
	})
})

// recordingParkedHold records every Hold call, so a test can prove a read never
// takes the mutating path.
type recordingParkedHold struct {
	key    string
	writes int
}

func (h *recordingParkedHold) Hold(ctx context.Context, row FlaggedRow) (string, bool) {
	h.writes++
	return h.Evaluate(ctx, row)
}

func (h *recordingParkedHold) Evaluate(_ context.Context, row FlaggedRow) (string, bool) {
	if row.Key() == h.key {
		return "parked-until", true
	}
	return "", false
}

func (h *recordingParkedHold) Opened(context.Context, FlaggedRow) {}

var _ = ginkgo.Describe("Openable gate parity", func() {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	ginkgo.It("holds the row past the per-cycle row budget with reasonRowBudget", func() {
		rows := make([]FlaggedRow, maxRowsPerTick+1)
		for i := range rows {
			rows[i] = FlaggedRow{
				Kind: "task", Vault: "private-personal", Name: fmt.Sprintf("r%d", i),
				Branch: "create", Liveness: "none",
			}
		}
		tick := newTick(staticSource{rows: rows}, nil, &recordingCaps{})
		tick.Now = fixedNow(now)

		got, err := tick.Openable(context.Background())
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(got).To(gomega.HaveLen(maxRowsPerTick + 1))
		opens, held := 0, 0
		for _, e := range got {
			if e.Decision == OpenableDecisionOpen {
				opens++
			} else {
				held++
				gomega.Expect(e.Reason).ToNot(gomega.BeEmpty())
			}
		}
		gomega.Expect(opens).To(gomega.Equal(maxRowsPerTick))
		gomega.Expect(held).To(gomega.Equal(1))
	})

	ginkgo.It("reports a parked row held and never calls Hold on a read", func() {
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "p", Branch: "create", Liveness: "none",
		}
		parked := &recordingParkedHold{key: row.Key()}
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, nil, &recordingCaps{})
		tick.Parked = parked
		tick.Now = fixedNow(now)

		got, err := tick.Openable(context.Background())
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(got).To(gomega.HaveLen(1))
		gomega.Expect(got[0].Decision).To(gomega.Equal(OpenableDecisionHeld))
		gomega.Expect(got[0].Reason).To(gomega.Equal("parked-until"))
		gomega.Expect(parked.writes).To(gomega.Equal(0))
	})
})
