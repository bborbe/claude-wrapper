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

var _ = ginkgo.Describe("DueParkWatchProcedure", func() {
	ctx := context.Background()

	fixture := func(sessionID string, due bool) (*DueParkWatchProcedure, *[]string) {
		resumed := &[]string{}
		return &DueParkWatchProcedure{
			Due: func(context.Context) (string, bool, error) { return sessionID, due, nil },
			Resume: func(_ context.Context, id string) error {
				*resumed = append(*resumed, id)
				return nil
			},
		}, resumed
	}

	ginkgo.It("resumes the session whose watch is due", func() {
		procedure, resumed := fixture("p1", true)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultResumed,
			SessionID: "p1",
		}))
		gomega.Expect(*resumed).To(gomega.Equal([]string{"p1"}))
	})

	ginkgo.It("reports none when no watch is due", func() {
		procedure, resumed := fixture("", false)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
		gomega.Expect(*resumed).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a due watch that names no session", func() {
		procedure, resumed := fixture("", true)

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*resumed).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a watch read failure rather than reporting a quiet tick", func() {
		procedure := &DueParkWatchProcedure{
			Due: func(context.Context) (string, bool, error) {
				return "", false, errors.New(ctx, "watch store unreadable")
			},
			Resume: func(context.Context, string) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a resume failure rather than reporting a resume", func() {
		procedure := &DueParkWatchProcedure{
			Due: func(context.Context) (string, bool, error) { return "p1", true, nil },
			Resume: func(context.Context, string) error {
				return errors.New(ctx, "spawn refused")
			},
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.DescribeTable("refuses a procedure wired without a collaborator",
		func(procedure *DueParkWatchProcedure) {
			_, err := procedure.Run(ctx)
			gomega.Expect(err).To(gomega.HaveOccurred())
		},
		ginkgo.Entry("no watch source", &DueParkWatchProcedure{
			Resume: func(context.Context, string) error { return nil },
		}),
		ginkgo.Entry("no resume action", &DueParkWatchProcedure{
			Due: func(context.Context) (string, bool, error) { return "", false, nil },
		}),
	)
})
