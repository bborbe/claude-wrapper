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

var _ = ginkgo.Describe("park-event due", func() {
	ctx := context.Background()

	ginkgo.Describe("ParkEventDue.DueTasks", func() {
		ginkgo.It("returns the distinct task names across due watches", func() {
			parsed := ParkEventDue{Due: []ParkEventWatch{
				{Tasks: map[string]string{"s1": "task-a", "s2": "task-b"}},
				{Tasks: map[string]string{"s3": "task-a"}},
			}}

			gomega.Expect(parsed.DueTasks()).To(gomega.Equal([]string{"task-a", "task-b"}))
		})

		ginkgo.It("returns nothing when no watch is due", func() {
			gomega.Expect(ParkEventDue{Store: "readable"}.DueTasks()).To(gomega.BeEmpty())
		})
	})

	ginkgo.Describe("NewParkEventDue", func() {
		runner := func(output string, code int, err error) KeepaliveScriptRunner {
			return func(context.Context, ...string) (string, int, error) { return output, code, err }
		}

		ginkgo.It("names every task a due watch carries", func() {
			output := `{"store":"readable","reason":"","due":[{"slug":"x","tasks":{"s1":"my-task"},"sessions":["s1"]}]}`

			tasks, err := NewParkEventDue(runner(output, 0, nil))(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(tasks).To(gomega.Equal([]string{"my-task"}))
		})

		// ⚠️ Every due task, not just the first: the procedure skips a gated task
		// and acts on the next, so a single-name read would stall the whole leg.
		ginkgo.It("returns every due task, sorted", func() {
			output := `{"store":"readable","reason":"","due":[` +
				`{"slug":"a","tasks":{"s1":"task-b"},"sessions":["s1"]},` +
				`{"slug":"b","tasks":{"s2":"task-a"},"sessions":["s2"]}]}`

			tasks, err := NewParkEventDue(runner(output, 0, nil))(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(tasks).To(gomega.Equal([]string{"task-a", "task-b"}))
		})

		// ⚠️ Absent and readable-but-empty both license nothing, so both are a
		// quiet tick — but the two are different facts and neither is an error.
		ginkgo.It("reports nothing due for an absent store", func() {
			output := `{"store":"absent","reason":"store dir missing","due":[]}`

			tasks, err := NewParkEventDue(runner(output, 0, nil))(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(tasks).To(gomega.BeEmpty())
		})

		// ⚠️ An UNREADABLE store is not a fleet with nothing parked.
		ginkgo.It("errors when the store could not be read", func() {
			output := `{"store":"unreadable","reason":"permission denied","due":[]}`

			_, err := NewParkEventDue(runner(output, 0, nil))(ctx)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("surfaces unparseable output rather than reporting nothing due", func() {
			_, err := NewParkEventDue(runner("no park watches due", 0, nil))(ctx)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("surfaces a non-zero exit", func() {
			_, err := NewParkEventDue(runner("", 2, nil))(ctx)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("surfaces a runner failure", func() {
			_, err := NewParkEventDue(runner("", -1, errors.New(ctx, "script missing")))(ctx)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		ginkgo.It("refuses an adapter wired without a runner", func() {
			_, err := NewParkEventDue(nil)(ctx)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})
})
