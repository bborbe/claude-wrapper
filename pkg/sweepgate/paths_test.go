// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("rejects a traversal subject", func() {
	ctx := context.Background()

	ginkgo.DescribeTable("refuses a subject that cannot be a safe file name",
		func(subject string) {
			slug, err := Slug(ctx, subject)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(slug).To(gomega.BeEmpty())

			path, err := StatePath(ctx, Config{StateDir: "/state"}, subject)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(path).To(gomega.BeEmpty())
		},
		ginkgo.Entry("parent traversal", "../../etc/x"),
		ginkgo.Entry("dot dot", ".."),
		ginkgo.Entry("dot", "."),
		ginkgo.Entry("empty", ""),
		ginkgo.Entry("slugs to empty", "!!!"),
	)

	ginkgo.It("never yields a path that escapes the state directory", func() {
		path, err := StatePath(ctx, Config{StateDir: "/state"}, "../../etc/x")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(path).To(gomega.BeEmpty())
	})

	ginkgo.It("slugs a benign subject the way the reference does", func() {
		slug, err := Slug(ctx, "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(slug).To(gomega.Equal("parity-topic"))
	})

	ginkgo.It("builds the five state paths", func() {
		cfg := Config{StateDir: "/state", SettledDir: "/settled"}
		statePath, err := StatePath(ctx, cfg, "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(statePath).To(gomega.Equal(filepath.Join("/state", "parity-topic.json")))

		tickPath, err := TickPath(ctx, cfg, "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(tickPath).To(gomega.Equal(filepath.Join("/state", "parity-topic.tick.txt")))

		cardPath, err := CardPath(ctx, cfg, "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cardPath).To(gomega.Equal(filepath.Join("/state", "parity-topic.card.json")))

		settledPath, err := SettledPath(ctx, cfg, "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(settledPath).To(gomega.Equal(filepath.Join("/settled", "parity-topic.json")))

		cadencePath, err := CadencePath(ctx, cfg, "topic", "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cadencePath).
			To(gomega.Equal(filepath.Join("/state", "topic--parity-topic.cadence")))
	})
})
