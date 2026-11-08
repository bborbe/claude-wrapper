// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The park a child raises during a wrapper restart used to be lost: the POST
// failed while the daemon was between processes, `decide` took its failure path,
// and the tool returned a denial for a question no operator ever saw — even
// though parks are persisted by request id for the successor to re-serve. These
// specs pin the retry that makes that persistence reachable, and the boundary
// that stops it retrying a request the wrapper actually rejected.
var _ = ginkgo.Describe("registering a park across a wrapper restart", func() {
	const params = `{"arguments":{"tool_name":"Bash","tool_use_id":"t1","input":{}}}`

	ginkgo.It("retries a 5xx and lands the park once the daemon is back", func() {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&calls, 1) <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		server := &MCPServer{WrapperURL: srv.URL, SessionID: "s1"}
		gomega.Expect(server.register(context.Background(), "r1", []byte(params))).To(gomega.Succeed())
		gomega.Expect(atomic.LoadInt32(&calls)).To(gomega.Equal(int32(3)),
			"the first two attempts are the restart, the third is the successor")
	})

	ginkgo.It("does not retry a 4xx", func() {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer srv.Close()

		server := &MCPServer{WrapperURL: srv.URL, SessionID: "s1"}
		gomega.Expect(server.register(context.Background(), "r1", []byte(params))).NotTo(gomega.Succeed())
		gomega.Expect(atomic.LoadInt32(&calls)).To(gomega.Equal(int32(1)),
			"a rejected request is not an absent daemon; retrying cannot change it")
	})

	ginkgo.It("stops retrying once the host that asked has gone away", func() {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		server := &MCPServer{WrapperURL: srv.URL, SessionID: "s1"}
		gomega.Expect(server.register(ctx, "r1", []byte(params))).NotTo(gomega.Succeed())
		// Whether the single attempt reaches the server before the cancelled context
		// aborts it is a race; what matters is that it is not retried.
		gomega.Expect(atomic.LoadInt32(&calls)).To(gomega.BeNumerically("<=", 1),
			"a cancelled caller must not keep retrying a verdict it can no longer receive")
	})
})
