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

	ginkgo.It("raises nothing when the keepalive is healthy", func() {
		raised := new(int)
		job := &FleetKeeperJob{
			Check: func(context.Context) (string, int, error) { return "ok", 0, nil },
			Raise: func(context.Context, string) (string, error) {
				*raised++
				return "card-1", nil
			},
		}
		cardID, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(cardID).To(gomega.BeEmpty())
		gomega.Expect(*raised).To(gomega.Equal(0))
	})

	ginkgo.It("raises exactly one card when a lapse is owed and reports its id", func() {
		raised := new(int)
		job := &FleetKeeperJob{
			Check: func(context.Context) (string, int, error) { return "stale 51m", fleetLapseOwedExitCode, nil },
			Raise: func(_ context.Context, message string) (string, error) {
				*raised++
				gomega.Expect(message).To(gomega.ContainSubstring("Fleet round lapsed"))
				gomega.Expect(message).To(gomega.ContainSubstring("stale 51m"))
				return "card-42", nil
			},
		}
		cardID, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(cardID).To(gomega.Equal("card-42"))
		gomega.Expect(*raised).To(gomega.Equal(1))
	})

	ginkgo.It("refuses a card with no id, because nobody could poll it", func() {
		job := &FleetKeeperJob{
			Check: func(context.Context) (string, int, error) { return "stale", fleetLapseOwedExitCode, nil },
			Raise: func(context.Context, string) (string, error) { return "", nil },
		}
		cardID, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(cardID).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a check failure rather than reporting a quiet tick", func() {
		job := &FleetKeeperJob{
			Check: func(context.Context) (string, int, error) {
				return "", 0, errors.New(ctx, "keepalive missing")
			},
		}
		_, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
