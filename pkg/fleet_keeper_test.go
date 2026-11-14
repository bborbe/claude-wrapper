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

	ginkgo.It("reports nothing when the keepalive is healthy", func() {
		cardID, err := job("LIVE\nfleet round within its window", 0).Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(cardID).To(gomega.BeEmpty())
	})

	ginkgo.It("returns the card id the keeper posted", func() {
		cardID, err := job("STALE\nno digest for 51m (card 8f3c21)", fleetLapseOwedExitCode).Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(cardID).To(gomega.Equal("8f3c21"))
	})

	ginkgo.It("refuses a lapse it cannot attribute to a card", func() {
		// The keeper judged a lapse owed but posted nothing, so there is no card
		// anyone could poll — logging a bare success would hide that.
		cardID, err := job("STALE\nno digest for 51m", fleetLapseOwedExitCode).Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(cardID).To(gomega.BeEmpty())
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
