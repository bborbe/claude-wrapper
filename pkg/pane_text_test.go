// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("PaneText", func() {
	ginkgo.Describe("StripANSI", func() {
		ginkgo.It("removes SGR and charset escapes, leaving the visible text", func() {
			gomega.Expect(StripANSI("\x1b(B\x1b[0;2mTry this\x1b[0m")).
				To(gomega.Equal("Try this"))
		})
	})

	ginkgo.Describe("VisibleIsDim", func() {
		ginkgo.It("is true when every visible character is faint", func() {
			gomega.Expect(VisibleIsDim("\x1b[0;2mghost\x1b[0m")).To(gomega.BeTrue())
		})

		ginkgo.It("is false when real text follows a dim span", func() {
			gomega.Expect(VisibleIsDim("\x1b[0;2mghost\x1b[0mdraft ok")).To(gomega.BeFalse())
		})

		ginkgo.It("does not read a truecolor escape as faint", func() {
			// 38;2;r;g;b carries a bare `2` as a colour sub-parameter; a membership
			// test would call this dim and type over the operator's typed text.
			gomega.Expect(VisibleIsDim("\x1b[38;2;255;0;0mred\x1b[0m")).To(gomega.BeFalse())
		})
	})

	ginkgo.Describe("PaneIsReady", func() {
		ginkgo.It("is true for an empty composer", func() {
			gomega.Expect(PaneIsReady("done\n❯ \n")).To(gomega.BeTrue())
		})

		ginkgo.It("is true when the composer holds only the dim placeholder", func() {
			gomega.Expect(PaneIsReady("done\n❯ \x1b[0;2mTry \"how does it work?\"\x1b[0m\n")).
				To(gomega.BeTrue())
		})

		ginkgo.It("is false when the composer holds typed text", func() {
			gomega.Expect(PaneIsReady("done\n❯ draft ok\n")).To(gomega.BeFalse())
		})

		ginkgo.It("is false when there is no composer", func() {
			gomega.Expect(PaneIsReady("just output\nno glyph\n")).To(gomega.BeFalse())
		})
	})
})
