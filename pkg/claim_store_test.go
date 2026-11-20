// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("FileClaimStore", func() {
	var (
		ctx  context.Context
		dir  string
		path string
		now  libtime.CurrentDateTimeGetter
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
		path = filepath.Join(dir, "ownership-claims.json")
		now = fixedNow(time.Date(2026, 11, 20, 5, 0, 0, 0, time.UTC))
	})

	ginkgo.It("reads an absent store as unclaimed rather than failing", func() {
		store := NewFileClaimStore(path, now)

		holder, err := store.Holder(ctx, "w1")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(holder).To(gomega.BeEmpty())
	})

	ginkgo.It("writes the store's own shape, which ownership-claim.py reads", func() {
		store := NewFileClaimStore(path, now)

		gomega.Expect(store.Claim(ctx, "w1", "claude-wrapper", "adopted from m1")).To(
			gomega.Succeed(),
		)

		raw, err := os.ReadFile(path)
		gomega.Expect(err).To(gomega.BeNil())
		var data map[string]interface{}
		gomega.Expect(json.Unmarshal(raw, &data)).To(gomega.Succeed())
		gomega.Expect(data["version"]).To(gomega.Equal(float64(1)))
		claims, ok := data["claims"].(map[string]interface{})
		gomega.Expect(ok).To(gomega.BeTrue())
		entry, ok := claims["w1"].(map[string]interface{})
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(entry["session"]).To(gomega.Equal("w1"))
		gomega.Expect(entry["manager"]).To(gomega.Equal("claude-wrapper"))
		gomega.Expect(entry["note"]).To(gomega.Equal("adopted from m1"))
		gomega.Expect(entry["claimed_at"]).To(gomega.Equal("2026-11-20T05:00:00Z"))
	})

	ginkgo.It("keeps the store sorted and one-entry-per-line-stable, so a diff is one entry", func() {
		store := NewFileClaimStore(path, now)
		gomega.Expect(store.Claim(ctx, "b", "m", "")).To(gomega.Succeed())
		gomega.Expect(store.Claim(ctx, "a", "m", "")).To(gomega.Succeed())

		raw, err := os.ReadFile(path)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(string(raw)).To(gomega.HavePrefix("{\n  \"claims\": {\n    \"a\":"))
		gomega.Expect(string(raw)).To(gomega.HaveSuffix("}\n"))
	})

	ginkgo.It("round-trips a claim through the store it wrote", func() {
		store := NewFileClaimStore(path, now)
		gomega.Expect(store.Claim(ctx, "w1", "m1", "")).To(gomega.Succeed())

		holder, err := NewFileClaimStore(path, now).Holder(ctx, "w1")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(holder).To(gomega.Equal("m1"))
	})

	ginkgo.It("preserves a claim another writer made, never rewriting it away", func() {
		// The file has a second writer. A claim from `ownership-claim.py` must
		// survive this store's read-modify-write.
		existing := `{"version": 1, "claims": {"w9": {"session": "w9", "manager": "human", ` +
			`"note": "", "claimed_at": "2026-01-01T00:00:00Z"}}}`
		gomega.Expect(os.WriteFile(path, []byte(existing), 0o644)).To(gomega.Succeed())
		store := NewFileClaimStore(path, now)

		gomega.Expect(store.Claim(ctx, "w1", "claude-wrapper", "")).To(gomega.Succeed())

		holder, err := store.Holder(ctx, "w9")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(holder).To(gomega.Equal("human"))
	})
})
