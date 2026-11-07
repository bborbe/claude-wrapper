// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"log"
	"net"
	"net/http"
	"net/http/pprof"
)

// IsLoopbackListen reports whether a `host:port` listen address binds to a
// loopback interface. It is the gate on mounting the pprof endpoints.
//
// ⚠️ An empty host (":18090") is NOT loopback — it binds every interface — and
// net.SplitHostPort returns it as "", so it is refused explicitly rather than
// read as "unspecified, therefore local". A malformed or unresolvable address
// is likewise false: the caller's failure mode for "cannot prove loopback" is
// to withhold the debug surface, not to expose it.
//
// ⚠️ The name `localhost` is RESOLVED rather than trusted. A hosts file or
// resolver mapping it to a routable address would otherwise make this report
// loopback while the listener binds publicly — publishing argv, which is the
// exact failure this gate exists to prevent. The lookup is affordable because
// registration happens once per process, not per request.
func IsLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		resolved, resolveErr := net.ResolveIPAddr("ip", host)
		if resolveErr != nil {
			return false
		}
		ip = resolved.IP
	}
	return ip.IsLoopback()
}

// RegisterPprofIfLoopback mounts the net/http/pprof endpoints on mux when
// listen is a loopback address, and reports whether it did.
//
// The profile surface is not a business route: its paths are fixed by the Go
// tooling (`go tool pprof http://localhost:18090/debug/pprof/heap`), so they
// sit under /debug/pprof rather than /api/1.0. The five paths registered here
// are the same ones net/http/pprof registers on http.DefaultServeMux,
// re-registered on this mux so the wrapper's own routes stay the only thing on
// the default.
//
// ⚠️ It is a named function rather than an inline `if` at the call site so the
// gate and the registration can be exercised together against a real mux. The
// failure this guards is silent in both directions — a gate that wrongly
// reports "not loopback" disables profiling in production with only a log
// line, and a registration that silently no-ops looks identical to a working
// one until someone tries to take a profile.
func RegisterPprofIfLoopback(mux *http.ServeMux, listen string) bool {
	if !IsLoopbackListen(listen) {
		log.Printf(
			"pprof endpoints NOT mounted: listen address %q is not loopback, and "+
				"/debug/pprof/cmdline would publish this process's argv to every interface bound",
			listen,
		)
		return false
	}
	// Logged on the mounted branch too, so both halves of the gate are
	// diagnosable from default-verbosity logs — the skip already logs, and a
	// silent success reads identically to a registration that never ran.
	log.Printf("pprof endpoints mounted on loopback listen address %q", listen)
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return true
}
