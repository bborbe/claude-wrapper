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

var _ = ginkgo.Describe("record-resume adapters", func() {
	ctx := context.Background()

	// recording captures the argv each adapter passes, so a test asserts the
	// exact flags rather than only that something ran.
	recording := func(code int, err error) (KeepaliveScriptRunner, *[][]string) {
		args := &[][]string{}
		return func(_ context.Context, a ...string) (string, int, error) {
			*args = append(*args, a)
			return "", code, err
		}, args
	}

	ginkgo.Describe("NewKeepaliveRecordResume", func() {
		ginkgo.It("passes --record-resume and the session id", func() {
			run, args := recording(0, nil)

			err := NewKeepaliveRecordResume(run)(ctx, "s1")

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(*args).To(gomega.Equal([][]string{{"--record-resume", "s1"}}))
		})

		// ⚠️ A refusal means the resume did not take; reporting it as success would
		// arm a cap for a resume that never happened.
		ginkgo.It("errors on a non-zero exit", func() {
			run, _ := recording(1, nil)

			err := NewKeepaliveRecordResume(run)(ctx, "s1")

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("refuses an empty session id without running the script", func() {
			run, args := recording(0, nil)

			err := NewKeepaliveRecordResume(run)(ctx, "")

			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(*args).To(gomega.BeEmpty())
		})

		ginkgo.It("refuses an adapter wired without a runner", func() {
			gomega.Expect(NewKeepaliveRecordResume(nil)(ctx, "s1")).To(gomega.HaveOccurred())
		})
	})

	ginkgo.Describe("NewWorkerRecordResume", func() {
		ginkgo.It("passes --vault, --task and --record-resume", func() {
			run, args := recording(0, nil)

			err := NewWorkerRecordResume(run)(ctx, "private-personal", "my-task", "s1")

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(*args).To(gomega.Equal([][]string{{
				"--vault", "private-personal", "--task", "my-task", "--record-resume", "s1",
			}}))
		})

		// ⚠️ Without --vault or --task the script exits 2 and the worker cap
		// silently never arms.
		ginkgo.DescribeTable("refuses an incomplete call without running the script",
			func(vault, task, sid string) {
				run, args := recording(0, nil)

				err := NewWorkerRecordResume(run)(ctx, vault, task, sid)

				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(*args).To(gomega.BeEmpty())
			},
			ginkgo.Entry("no vault", "", "my-task", "s1"),
			ginkgo.Entry("no task", "private-personal", "", "s1"),
			ginkgo.Entry("no session id", "private-personal", "my-task", ""),
		)

		ginkgo.It("errors on a non-zero exit", func() {
			run, _ := recording(2, nil)

			err := NewWorkerRecordResume(run)(ctx, "v", "t", "s1")

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("surfaces a runner failure", func() {
			run, _ := recording(-1, errors.New(ctx, "script missing"))

			err := NewWorkerRecordResume(run)(ctx, "v", "t", "s1")

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("refuses an adapter wired without a runner", func() {
			gomega.Expect(NewWorkerRecordResume(nil)(ctx, "v", "t", "s1")).To(gomega.HaveOccurred())
		})
	})
})
