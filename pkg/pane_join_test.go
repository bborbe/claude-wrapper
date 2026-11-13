// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("PaneJoin", func() {
	ginkgo.Describe("JoinPanes", func() {
		ginkgo.It("joins pid to pane through the tty", func() {
			panes := map[string]string{"11": "/dev/ttys008"}
			ttys := map[int]string{4242: "ttys008"}
			gomega.Expect(JoinPanes(panes, ttys)).To(gomega.Equal(map[int]string{4242: "11"}))
		})

		ginkgo.It("matches whether or not either side carries /dev/", func() {
			panes := map[string]string{"11": "ttys008"}
			ttys := map[int]string{4242: "/dev/ttys008"}
			gomega.Expect(JoinPanes(panes, ttys)).To(gomega.Equal(map[int]string{4242: "11"}))
		})

		ginkgo.It("drops a pid whose tty names no pane", func() {
			panes := map[string]string{"11": "/dev/ttys008"}
			ttys := map[int]string{4242: "ttys099"}
			gomega.Expect(JoinPanes(panes, ttys)).To(gomega.BeEmpty())
		})
	})

	ginkgo.Describe("parseWeztermPanes", func() {
		ginkgo.It("reads pane_id and tty_name pairs", func() {
			panes, err := parseWeztermPanes([]byte(
				`[{"pane_id": 11, "tty_name": "/dev/ttys008"},` +
					`{"pane_id": 16, "tty_name": "/dev/ttys023"}]`,
			))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(panes).To(gomega.Equal(map[string]string{
				"11": "/dev/ttys008",
				"16": "/dev/ttys023",
			}))
		})
	})

	ginkgo.Describe("parsePIDTTYs", func() {
		ginkgo.It("reads pid tty pairs and strips the device prefix", func() {
			ttys := parsePIDTTYs("  4242 ttys008\n  4243 ttys023\n")
			gomega.Expect(ttys).To(gomega.Equal(map[int]string{4242: "ttys008", 4243: "ttys023"}))
		})

		ginkgo.It("skips lines that are not a pid tty pair", func() {
			ttys := parsePIDTTYs("garbage\n  4242 ttys008\n")
			gomega.Expect(ttys).To(gomega.Equal(map[int]string{4242: "ttys008"}))
		})
	})
})
