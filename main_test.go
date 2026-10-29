// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "testing"

// A child registers its questions against this URL. Getting it wrong is silent:
// a failed registration is only logged, so the gate defaults to allow and the
// prompt is never seen.
func TestSelfURL(t *testing.T) {
	cases := map[string]string{
		"localhost:18090": "http://localhost:18090",
		"127.0.0.1:8080":  "http://127.0.0.1:8080",
		// A bare-port listen address is a valid net.Listen target whose host is
		// empty; prefixed naively it yields "http://:18090", which the child's
		// HTTP client rejects with "no Host in request URL".
		":18090": "http://localhost:18090",
	}
	for listen, want := range cases {
		if got := selfURL(listen); got != want {
			t.Errorf("selfURL(%q) = %q, want %q", listen, got, want)
		}
	}
}
