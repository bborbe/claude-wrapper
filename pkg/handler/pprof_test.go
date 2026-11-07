// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/handler"
)

var _ = Describe("Pprof", func() {

	Describe("IsLoopbackListen", func() {

		DescribeTable("reports whether the listen address is loopback",
			func(addr string, expected bool) {
				Expect(handler.IsLoopbackListen(addr)).To(Equal(expected))
			},
			Entry("localhost, the wrapper's default", "localhost:18090", true),
			Entry("an explicit loopback v4 address", "127.0.0.1:18090", true),
			Entry("an explicit loopback v6 address", "[::1]:18090", true),
			// An empty host is the case the gate exists for: ":18090" binds every
			// interface, and reading it as "unspecified, therefore local" would
			// publish argv and heap contents to the network.
			Entry("an empty host binds every interface", ":18090", false),
			Entry("the wildcard v4 address", "0.0.0.0:18090", false),
			Entry("a routable address", "203.0.113.10:18090", false),
			Entry("a malformed address", "not-an-address", false),
		)
	})

	Describe("RegisterPprofIfLoopback", func() {

		// serve runs one request through a mux the registration was applied to,
		// so the spec exercises the real ServeMux dispatch rather than calling the
		// gate and the mount separately.
		serve := func(listen string, path string) *httptest.ResponseRecorder {
			mux := http.NewServeMux()
			handler.RegisterPprofIfLoopback(mux, listen)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			return recorder
		}

		It("mounts the pprof index on a loopback address", func() {
			mux := http.NewServeMux()
			Expect(handler.RegisterPprofIfLoopback(mux, "127.0.0.1:18090")).To(BeTrue())

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
			Expect(recorder.Code).To(Equal(http.StatusOK))
			Expect(recorder.Body.String()).To(ContainSubstring("goroutine"))
		})

		// The named profiles are what the investigation actually reads: a
		// /debug/pprof/ that answers but a heap that 404s would still block every
		// profile this task exists to take.
		It("serves the named profiles under the mounted prefix", func() {
			recorder := serve("127.0.0.1:18090", "/debug/pprof/heap?debug=1")
			Expect(recorder.Code).To(Equal(http.StatusOK))
			Expect(recorder.Body.String()).To(ContainSubstring("heap profile"))
		})

		It("withholds every pprof path on a non-loopback address", func() {
			mux := http.NewServeMux()
			Expect(handler.RegisterPprofIfLoopback(mux, "0.0.0.0:18090")).To(BeFalse())

			for _, path := range []string{
				"/debug/pprof/",
				"/debug/pprof/heap",
				"/debug/pprof/cmdline",
			} {
				recorder := serve("0.0.0.0:18090", path)
				Expect(recorder.Code).To(Equal(http.StatusNotFound),
					"path %s must not be served on a non-loopback listen address", path)
			}
		})

		// An empty host is the specific misreading the gate guards: the listener
		// binds every interface, so the debug surface must be absent.
		It("withholds the pprof endpoints when the listen address has no host", func() {
			mux := http.NewServeMux()
			Expect(handler.RegisterPprofIfLoopback(mux, ":18090")).To(BeFalse())

			recorder := serve(":18090", "/debug/pprof/")
			Expect(recorder.Code).To(Equal(http.StatusNotFound))
		})
	})
})
