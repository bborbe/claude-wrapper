// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("RotatePages", func() {
	pages := func(vault string, names ...string) []KeepalivePageRef {
		refs := make([]KeepalivePageRef, 0, len(names))
		for _, n := range names {
			refs = append(refs, KeepalivePageRef{Vault: vault, Name: n})
		}
		return refs
	}

	ginkgo.It("starts at the first page when there is no cursor and caps per vault", func() {
		selected, next := RotatePages(nil, pages("v", "a", "b", "c", "d"), 2)

		gomega.Expect(selected).To(gomega.Equal(pages("v", "a", "b")))
		gomega.Expect(next).To(gomega.Equal(map[string]string{"v": "b"}))
	})

	ginkgo.It("resumes after the cursor", func() {
		selected, next := RotatePages(map[string]string{"v": "b"}, pages("v", "a", "b", "c", "d"), 2)

		gomega.Expect(selected).To(gomega.Equal(pages("v", "c", "d")))
		gomega.Expect(next).To(gomega.Equal(map[string]string{"v": "d"}))
	})

	// ⚠️ The wrap: past the end, the scan returns to the start so later ticks reach
	// the pages before the cursor too.
	ginkgo.It("wraps to the start when the scan runs past the end", func() {
		selected, next := RotatePages(map[string]string{"v": "c"}, pages("v", "a", "b", "c", "d"), 3)

		gomega.Expect(selected).To(gomega.Equal(pages("v", "d", "a", "b")))
		gomega.Expect(next).To(gomega.Equal(map[string]string{"v": "b"}))
	})

	// A cursor naming a page that no longer exists resumes at the next greater name.
	ginkgo.It("resumes at the next name when the cursor page is gone", func() {
		selected, _ := RotatePages(map[string]string{"v": "bb"}, pages("v", "a", "cc", "dd"), 1)

		gomega.Expect(selected).To(gomega.Equal(pages("v", "cc")))
	})

	// A cursor beyond every name wraps to the first.
	ginkgo.It("wraps to the first name when the cursor is past every page", func() {
		selected, _ := RotatePages(map[string]string{"v": "zzz"}, pages("v", "a", "b"), 1)

		gomega.Expect(selected).To(gomega.Equal(pages("v", "a")))
	})

	// Each vault gets its own cap and its own cursor.
	ginkgo.It("applies the cap per vault, not globally", func() {
		all := append(pages("v1", "a", "b", "c"), pages("v2", "x", "y", "z")...)

		selected, next := RotatePages(nil, all, 2)

		gomega.Expect(selected).To(gomega.Equal([]KeepalivePageRef{
			{Vault: "v1", Name: "a"}, {Vault: "v1", Name: "b"},
			{Vault: "v2", Name: "x"}, {Vault: "v2", Name: "y"},
		}))
		gomega.Expect(next).To(gomega.Equal(map[string]string{"v1": "b", "v2": "y"}))
	})

	// ⚠️ A zero/negative cap selects nothing rather than the whole fleet.
	ginkgo.It("selects nothing and keeps the cursor when the cap is not positive", func() {
		selected, next := RotatePages(map[string]string{"v": "b"}, pages("v", "a", "b", "c"), 0)

		gomega.Expect(selected).To(gomega.BeEmpty())
		gomega.Expect(next).To(gomega.Equal(map[string]string{"v": "b"}))
	})

	ginkgo.It("does not repeat a page when the cap exceeds the list", func() {
		selected, _ := RotatePages(nil, pages("v", "a", "b"), 5)

		gomega.Expect(selected).To(gomega.Equal(pages("v", "a", "b")))
	})
})
