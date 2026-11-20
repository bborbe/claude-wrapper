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

var _ = ginkgo.Describe("StoodDownCloseProcedure", func() {
	ctx := context.Background()

	ginkgo.It("reports kept with the session when the marker is present", func() {
		procedure := &StoodDownCloseProcedure{
			Marker: func(context.Context) (string, bool, error) { return "s1", true, nil },
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		// ⚠️ `kept`, never `closed`: the tick decides and the ReaperJob closes. A
		// `closed` here would claim a close the reaper then performs a second time.
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultKept,
			SessionID: "s1",
			Reason:    "stood-down",
		}))
	})

	ginkgo.It("reports none when no marker is present", func() {
		procedure := &StoodDownCloseProcedure{
			Marker: func(context.Context) (string, bool, error) { return "", false, nil },
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
	})

	ginkgo.It("refuses a marker that names no session", func() {
		procedure := &StoodDownCloseProcedure{
			Marker: func(context.Context) (string, bool, error) { return "", true, nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a marker read failure rather than reporting a quiet tick", func() {
		procedure := &StoodDownCloseProcedure{
			Marker: func(context.Context) (string, bool, error) {
				return "", false, errors.New(ctx, "marker unreadable")
			},
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses a procedure wired without a marker source", func() {
		_, err := (&StoodDownCloseProcedure{}).Run(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
