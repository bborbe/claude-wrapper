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

var _ = ginkgo.Describe("NewKeepaliveHold", func() {
	ctx := context.Background()

	recording := func(code int, err error) (KeepaliveScriptRunner, *[][]string) {
		args := &[][]string{}
		return func(_ context.Context, a ...string) (string, int, error) {
			*args = append(*args, a)
			return "", code, err
		}, args
	}

	ginkgo.It("passes is-held and the session id", func() {
		run, args := recording(0, nil)

		state, err := NewKeepaliveHold(run)(ctx, "s1")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(state).To(gomega.Equal(HoldHeld))
		gomega.Expect(*args).To(gomega.Equal([][]string{{"is-held", "s1"}}))
	})

	ginkgo.It("reports HoldHeld on exit 0", func() {
		run, _ := recording(0, nil)
		state, err := NewKeepaliveHold(run)(ctx, "s1")
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(state).To(gomega.Equal(HoldHeld))
	})

	ginkgo.It("reports HoldNotHeld on exit 1", func() {
		run, _ := recording(1, nil)
		state, err := NewKeepaliveHold(run)(ctx, "s1")
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(state).To(gomega.Equal(HoldNotHeld))
	})

	// ⚠️ The load-bearing case: exit 2 is HoldUnknown, DISTINCT from HoldHeld, so the
	// tick reports `reason=hold-unknown` not `reason=hold`. Both skip the act; the
	// reason is what SC2's second fixture checks.
	ginkgo.It("reports HoldUnknown on exit 2, distinct from HoldHeld", func() {
		run, _ := recording(2, nil)
		state, err := NewKeepaliveHold(run)(ctx, "s1")
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(state).To(gomega.Equal(HoldUnknown))
		gomega.Expect(state).NotTo(gomega.Equal(HoldHeld))
	})

	ginkgo.It("errors on an unexpected exit code", func() {
		run, _ := recording(7, nil)
		_, err := NewKeepaliveHold(run)(ctx, "s1")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a runner failure", func() {
		run, _ := recording(-1, errors.New(ctx, "script missing"))
		_, err := NewKeepaliveHold(run)(ctx, "s1")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses an empty session id without running the script", func() {
		run, args := recording(0, nil)
		_, err := NewKeepaliveHold(run)(ctx, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*args).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an adapter wired without a runner", func() {
		_, err := NewKeepaliveHold(nil)(ctx, "s1")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
