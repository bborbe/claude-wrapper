// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("BoardCardHistory", func() {
	var (
		ctx    context.Context
		server *httptest.Server
		body   string
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gomega.Expect(r.URL.Path).To(gomega.Equal(AttentionHistoryPath))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	})

	ginkgo.AfterEach(func() {
		server.Close()
	})

	const item = `{"item_id":"abc123","producer_id":"p1","dedup_key":"k1"}`

	ginkgo.It("decodes a bare JSON array", func() {
		body = `[{"item_id":"other","producer_id":"p0","dedup_key":"k0"},` + item + `]`
		id, err := NewBoardCardHistory(server.URL, nil).ExistingCard(ctx, "p1", "k1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(id).To(gomega.Equal("abc123"))
	})

	ginkgo.It("decodes an items envelope", func() {
		body = `{"items":[` + item + `]}`
		id, err := NewBoardCardHistory(server.URL, nil).ExistingCard(ctx, "p1", "k1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(id).To(gomega.Equal("abc123"))
	})

	ginkgo.It("returns empty when no card matches", func() {
		body = `[]`
		id, err := NewBoardCardHistory(server.URL, nil).ExistingCard(ctx, "p1", "k1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(id).To(gomega.BeEmpty())
	})

	ginkgo.It("fails closed on garbage", func() {
		body = `"nope"`
		_, err := NewBoardCardHistory(server.URL, nil).ExistingCard(ctx, "p1", "k1")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
