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

var _ = ginkgo.Describe("NewKeepaliveClaim", func() {
	ctx := context.Background()

	runner := func(output string, code int, err error) KeepaliveScriptRunner {
		return func(context.Context, ...string) (string, int, error) { return output, code, err }
	}

	// The two words are opposite-handed, and getting them backwards makes the
	// double-writer guard resume when it should skip.
	ginkgo.It("grants the claim on claim-taken", func() {
		claim := NewKeepaliveClaim(runner("claim-taken\n", 0, nil))

		granted, err := claim(ctx, "s1")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(granted).To(gomega.BeTrue())
	})

	ginkgo.It("refuses the claim on claimed", func() {
		claim := NewKeepaliveClaim(runner("claimed\n", 0, nil))

		granted, err := claim(ctx, "s1")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(granted).To(gomega.BeFalse())
	})

	// A verdict the claim path never emits is a script/handler drift, not a grant.
	ginkgo.It("refuses an unexpected verdict rather than granting it", func() {
		claim := NewKeepaliveClaim(runner("resume\n", 0, nil))

		_, err := claim(ctx, "s1")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a runner failure rather than granting the claim", func() {
		claim := NewKeepaliveClaim(runner("", -1, errors.New(ctx, "script missing")))

		_, err := claim(ctx, "s1")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses an empty session id without running the script", func() {
		called := false
		claim := NewKeepaliveClaim(func(context.Context, ...string) (string, int, error) {
			called = true
			return "claim-taken\n", 0, nil
		})

		_, err := claim(ctx, "")

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(called).To(gomega.BeFalse())
	})

	ginkgo.It("refuses an adapter wired without a runner", func() {
		claim := NewKeepaliveClaim(nil)

		_, err := claim(ctx, "s1")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
