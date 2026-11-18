// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("RefuseUnsafeSubject", func() {
	ctx := context.Background()

	ginkgo.DescribeTable("refuses a subject that would traverse a path",
		func(subject string) {
			gomega.Expect(RefuseUnsafeSubject(ctx, subject)).NotTo(gomega.Succeed())
		},
		ginkgo.Entry("parent reference", "../../../etc/passwd"),
		ginkgo.Entry("embedded parent reference", "foo/../../bar"),
		ginkgo.Entry("forward slash", "a/b"),
		ginkgo.Entry("backslash", `a\b`),
	)

	ginkgo.DescribeTable("accepts an ordinary subject",
		func(subject string) {
			gomega.Expect(RefuseUnsafeSubject(ctx, subject)).To(gomega.Succeed())
		},
		ginkgo.Entry("simple", "Development Pipeline Throughput"),
		ginkgo.Entry("dotted", "sweep.gate"),
		ginkgo.Entry("single dot", "v1.2"),
	)
})

// The defect this guards: Slug refused a traversal while ResolveSubject joined
// the same subject straight into a vault path and then STAT-ED and READ it, so a
// subject reaching ResolveSubject could read outside the vault. Both now share
// one rule; this spec pins ResolveSubject's half.
var _ = ginkgo.Describe("ResolveSubject", func() {
	ginkgo.It("refuses a traversing subject before touching the filesystem", func() {
		branch, members, err := ResolveSubject(context.Background(), "/nonexistent-vault", "../../../etc/passwd")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("path traversal"))
		gomega.Expect(branch).To(gomega.BeEmpty())
		gomega.Expect(members).To(gomega.BeEmpty())
	})
})
