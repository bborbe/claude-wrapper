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

	fixture := func(tasks []string) (*DueParkWatchProcedure, *[]string) {
		resumed := &[]string{}
		return &DueParkWatchProcedure{
			DueTasks: func(context.Context) ([]string, error) { return tasks, nil },
			Resume: func(_ context.Context, task string) error {
				*resumed = append(*resumed, task)
				return nil
			},
		}, resumed
	}

	ginkgo.It("resumes the task whose watch is due", func() {
		procedure, resumed := fixture([]string{"p1"})

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultResumed,
			SessionID: "p1",
		}))
		gomega.Expect(*resumed).To(gomega.Equal([]string{"p1"}))
	})

	ginkgo.It("reports none when no watch is due", func() {
		procedure, resumed := fixture(nil)

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
		gomega.Expect(*resumed).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a due watch that names no task", func() {
		procedure, resumed := fixture([]string{""})

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*resumed).To(gomega.BeEmpty())
	})

	// ⚠️ The defect this procedure exists to fix: a gated head-of-line task must
	// not stall the leg. `DueTasks` is sorted, so without the skip the same blocked
	// task would be first on every tick and nothing would ever resume.
	ginkgo.It("skips a gated task and resumes the next due one", func() {
		resumed := &[]string{}
		procedure := &DueParkWatchProcedure{
			DueTasks: func(context.Context) ([]string, error) { return []string{"gated", "free"}, nil },
			Blockers: func(_ context.Context, task string) ([]string, error) {
				if task == "gated" {
					return []string{"Blocker A"}, nil
				}
				return nil, nil
			},
			Resume: func(_ context.Context, task string) error {
				*resumed = append(*resumed, task)
				return nil
			},
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultResumed,
			SessionID: "free",
		}))
		gomega.Expect(*resumed).To(gomega.Equal([]string{"free"}))
	})

	ginkgo.It("reports the first blocked skip with its blockers when every task is gated", func() {
		procedure := &DueParkWatchProcedure{
			DueTasks: func(context.Context) ([]string, error) {
				return []string{"gated-a", "gated-b"}, nil
			},
			Blockers: func(_ context.Context, task string) ([]string, error) {
				if task == "gated-a" {
					return []string{"Blocker A"}, nil
				}
				return []string{"Blocker B"}, nil
			},
			Resume: func(context.Context, string) error { return nil },
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect).To(gomega.Equal(KeepaliveEffect{
			Result:    KeepaliveResultSkipped,
			SessionID: "gated-a",
			Reason:    "blocked",
			Blockers:  []string{"Blocker A"},
		}))
	})

	ginkgo.It("resumes a row whose blockers have all resolved", func() {
		procedure := &DueParkWatchProcedure{
			DueTasks: func(context.Context) ([]string, error) { return []string{"freed"}, nil },
			Blockers: func(context.Context, string) ([]string, error) { return nil, nil },
			Resume:   func(context.Context, string) error { return nil },
		}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultResumed))
	})

	ginkgo.It("surfaces a blocker read failure rather than resuming or skipping", func() {
		procedure := &DueParkWatchProcedure{
			DueTasks: func(context.Context) ([]string, error) { return []string{"p1"}, nil },
			Blockers: func(context.Context, string) ([]string, error) {
				return nil, errors.New(ctx, "vault-api unavailable")
			},
			Resume: func(context.Context, string) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a watch read failure rather than reporting a quiet tick", func() {
		procedure := &DueParkWatchProcedure{
			DueTasks: func(context.Context) ([]string, error) {
				return nil, errors.New(ctx, "watch store unreadable")
			},
			Resume: func(context.Context, string) error { return nil },
		}

		_, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a resume failure rather than reporting a resume", func() {
		procedure := &DueParkWatchProcedure{
			DueTasks: func(context.Context) ([]string, error) { return []string{"p1"}, nil },
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
			DueTasks: func(context.Context) ([]string, error) { return nil, nil },
		}),
	)
})
