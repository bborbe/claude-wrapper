// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("KeepaliveCursorFile", func() {
	ctx := context.Background()

	ginkgo.It("round-trips a cursor through the file", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "cursor.json")
		store := NewKeepaliveCursorFile(path)

		gomega.Expect(store.Save(ctx, map[string]string{"v1": "page-b", "v2": "page-z"})).To(gomega.Succeed())

		loaded, err := store.Load(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(loaded).To(gomega.Equal(map[string]string{"v1": "page-b", "v2": "page-z"}))
	})

	ginkgo.It("returns an empty cursor for a missing file", func() {
		store := NewKeepaliveCursorFile(filepath.Join(ginkgo.GinkgoT().TempDir(), "absent.json"))

		loaded, err := store.Load(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(loaded).To(gomega.BeEmpty())
	})

	// ⚠️ A corrupt cursor is EMPTY, not an error — the tick must still run, starting
	// from the beginning.
	ginkgo.It("treats a corrupt cursor as empty", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "cursor.json")
		gomega.Expect(os.WriteFile(path, []byte("not json {"), 0o600)).To(gomega.Succeed())

		loaded, err := NewKeepaliveCursorFile(path).Load(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(loaded).To(gomega.BeEmpty())
	})
})
