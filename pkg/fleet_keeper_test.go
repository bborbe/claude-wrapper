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

var _ = ginkgo.Describe("FleetKeeperJob", func() {
	ctx := context.Background()

	job := func(output string, code int) *FleetKeeperJob {
		return &FleetKeeperJob{
			Check: func(context.Context) (string, int, error) { return output, code, nil },
		}
	}

	// Every fixture below is a real output shape, copied from the `report()` calls
	// in `fleet-keeper.py` at `claude-supervisor@176b0783`: the verdict word alone
	// on the first line, the sentence on the second. A fake shaped like the
	// keepalive's `LIVE`/`STALE` words would test a contract the keeper never
	// emits, which is how the exit-code defect survived its own tests.
	const card = "81c928cb8b96a3351e9ab72575ae0fc5"

	ginkgo.DescribeTable("a quiet verdict raises no card and no error",
		func(output string) {
			cardID, err := job(output, 0).Tick(ctx)
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(cardID).To(gomega.BeEmpty())
		},
		ginkgo.Entry("ok", "ok\nthe fleet round is inside its limit"),
		ginkgo.Entry("stopped", "stopped\nthe fleet loop was stood down with /supervisor:stop — a deliberate stop, not a lapse; left alone until it is re-armed"),
		ginkgo.Entry("pending", "pending\nstale for 301s of the 2400s that make a lapse — possibly a slow but healthy round; not reported yet"),
	)

	ginkgo.DescribeTable("reported with a card id returns that id",
		func(output string) {
			cardID, err := job(output, 1).Tick(ctx)
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(cardID).To(gomega.Equal(card))
		},
		ginkgo.Entry("a new lapse",
			"reported\nstale for 2698s — lapse reported for session 90601db5 (card "+card+")"),
		ginkgo.Entry("a lapse already reported this day",
			"reported\nlapse since 12:29:23Z already reported (card "+card+"); nothing more until it recovers"),
	)

	// ⚠️ The discriminating cases. A fix that accepts every exit `1` passes the
	// table above and fails here — which is the whole point of the split: the
	// keeper exits `1` for three different verdicts.
	ginkgo.DescribeTable("a non-quiet outcome that is not a raised card is an error",
		func(output string, code int) {
			cardID, err := job(output, code).Tick(ctx)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(cardID).To(gomega.BeEmpty())
		},
		ginkgo.Entry("reported without a card id",
			"reported\nstale for 2698s — lapse reported for session 90601db5", 1),
		ginkgo.Entry("cannot-tell on an unreadable ledger",
			"cannot-tell\nthe digest ledger is missing — reported, never treated as fine (card abc123)", 1),
		ginkgo.Entry("cannot-tell on an unwritable lapse record",
			"cannot-tell\ncannot persist lapse state — reported (card abc123)", 1),
		ginkgo.Entry("dead",
			"dead\nfleet manager 90601db5 is ABSENT — reported (card abc123)", 1),
		ginkgo.Entry("an unrecognised first line",
			"Traceback (most recent call last):\n  File \"fleet-keeper.py\", line 856", 1),
		ginkgo.Entry("an exit-0 verdict the handler does not know",
			"paused\nwaiting for a window", 0),
		ginkgo.Entry("a quiet verdict that exited non-zero",
			"ok\nthe fleet round is inside its limit", 1),
		ginkgo.Entry("a reported verdict that exited zero",
			"reported\nstale for 2698s (card abc123)", 0),
		ginkgo.Entry("the retired exit-10 contract",
			"stale\nno digest for 51m", 10),
	)

	ginkgo.It("names the verdict in the error, so a cannot-tell is distinguishable from a dead", func() {
		_, err := job("cannot-tell\nthe digest ledger is missing", 1).Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("cannot-tell"))

		_, err = job("dead\nfleet manager 90601db5 is ABSENT", 1).Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("dead"))
	})

	ginkgo.It("carries the keeper's own explanation into the error", func() {
		_, err := job("cannot-tell\nthe digest ledger is missing — reported, never treated as fine", 1).Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("the digest ledger is missing"))
	})

	ginkgo.It("separates a word it has never seen from a verdict the keeper means", func() {
		// Script/handler drift and a keeper failure need different fixes, so they
		// must not collapse into one message.
		_, err := job("paused\nwaiting for a window", 0).Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("unrecognised"))

		_, err = job("dead\nfleet manager 90601db5 is ABSENT", 1).Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.Not(gomega.ContainSubstring("unrecognised")))
	})

	ginkgo.It("lists every verdict the keeper prints", func() {
		gomega.Expect(AvailableFleetKeeperVerdicts).To(gomega.HaveLen(6))
		for _, verdict := range AvailableFleetKeeperVerdicts {
			gomega.Expect(AvailableFleetKeeperVerdicts.Contains(verdict)).To(gomega.BeTrue())
		}
		gomega.Expect(AvailableFleetKeeperVerdicts.Contains(FleetKeeperVerdict("paused"))).To(gomega.BeFalse())
	})

	ginkgo.It("surfaces a check failure rather than reporting a quiet tick", func() {
		broken := &FleetKeeperJob{
			Check: func(context.Context) (string, int, error) {
				return "", 0, errors.New(ctx, "keepalive missing")
			},
		}
		_, err := broken.Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
