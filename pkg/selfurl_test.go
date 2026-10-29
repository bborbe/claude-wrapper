// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// A child registers its questions against this URL, and a failed registration
// is only logged — so a wrong value means the gate silently defaults to allow
// and the prompt is never seen.
var _ = ginkgo.Describe("SelfURL", func() {
	ginkgo.DescribeTable("resolves the URL a child reports to",
		func(listen, want string) {
			gomega.Expect(SelfURL(listen)).To(gomega.Equal(want))
		},
		ginkgo.Entry("a normal host and port", "localhost:18090", "http://localhost:18090"),
		ginkgo.Entry("a loopback IP", "127.0.0.1:8080", "http://127.0.0.1:8080"),
		// A bare port is a valid net.Listen target; prefixed naively it yields
		// "http://:18090", rejected by the child as "no Host in request URL".
		ginkgo.Entry("a bare port", ":18090", "http://localhost:18090"),
		// A wildcard is not a routable destination on every platform.
		ginkgo.Entry("an IPv4 wildcard", "0.0.0.0:18090", "http://localhost:18090"),
		ginkgo.Entry("an IPv6 wildcard", "[::]:18090", "http://localhost:18090"),
	)
})
