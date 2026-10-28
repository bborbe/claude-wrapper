// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

func main() {
	listen := flag.String("listen", "localhost:18090", "HTTP listen address")
	claudeBin := flag.String("claude-bin", "claude", "path to the claude binary")
	flag.Parse()

	supervisor := pkg.NewSupervisor(*claudeBin)

	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.Healthz())
	mux.Handle("/api/1.0/sessions", handler.Sessions(supervisor))
	mux.Handle("/api/1.0/sessions/", handler.Session(supervisor))

	log.Printf("claude-wrapper listening on %s", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatalf("listen failed: %v", err)
	}
}
