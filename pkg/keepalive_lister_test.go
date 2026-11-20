// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("KeepaliveLister", func() {
	ctx := context.Background()

	source := func(pages ...KeepalivePage) KeepalivePageSource {
		return func(context.Context) ([]KeepalivePage, error) { return pages, nil }
	}

	ginkgo.It("filters the live population and rotates from the cursor", func() {
		lister := &KeepaliveLister{
			Source: source(
				KeepalivePage{Vault: "v", Name: "a", SessionID: "s1", Status: "in_progress"},
				KeepalivePage{Vault: "v", Name: "b", SessionID: "", Status: "in_progress"}, // no session → dropped
				KeepalivePage{Vault: "v", Name: "c", SessionID: "s3", Status: "completed"}, // completed → dropped
				KeepalivePage{Vault: "v", Name: "d", SessionID: "s4", Status: "in_progress"},
			),
			Cursor: NewKeepaliveCursorFile(filepath.Join(ginkgo.GinkgoT().TempDir(), "c.json")),
			Kind:   KeepaliveKindTask,
			Cap:    20,
		}

		selected, err := lister.Select(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		// a and d are the only live task pages.
		gomega.Expect(selected).To(gomega.Equal([]KeepalivePageRef{
			{Vault: "v", Name: "a", SessionID: "s1"}, {Vault: "v", Name: "d", SessionID: "s4"},
		}))
	})

	ginkgo.It("advances the cursor to the last evaluated page per vault", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "c.json")
		lister := &KeepaliveLister{
			Source: source(),
			Cursor: NewKeepaliveCursorFile(path),
			Kind:   KeepaliveKindTask,
			Cap:    20,
		}

		gomega.Expect(lister.Advance(ctx, map[string]string{"v": "page-d"})).To(gomega.Succeed())

		loaded, err := NewKeepaliveCursorFile(path).Load(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(loaded).To(gomega.Equal(map[string]string{"v": "page-d"}))
	})
})
