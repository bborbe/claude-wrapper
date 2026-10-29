// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "net"

// SelfURL is the base URL a child uses to reach the wrapper that spawned it.
//
// Every child's permission tool is pointed at this URL, and getting it wrong is
// silent: a failed registration is only logged, so the gate defaults to allow
// and the prompt is never seen. Two listen spellings need mapping, and both are
// valid net.Listen targets:
//
//   - a bare port (`:18090`) has an empty host, and prefixed naively yields
//     `http://:18090`, which the child's HTTP client rejects with
//     "no Host in request URL";
//   - a wildcard host (`0.0.0.0`, `::`) is not a routable destination on every
//     platform, so it becomes loopback too.
//
// Anything else is passed through unchanged.
func SelfURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
