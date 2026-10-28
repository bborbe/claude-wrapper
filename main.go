// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

func main() {
	listen := flag.String("listen", "localhost:18090", "HTTP listen address")
	claudeBin := flag.String("claude-bin", "claude", "path to the claude binary")
	mcpStdio := flag.Bool("mcp-stdio", false, "serve the permission MCP tool on stdio instead of HTTP")
	mcpLog := flag.String("mcp-log", "", "path to append raw permission requests to")
	wrapperURL := flag.String("wrapper-url", "", "base URL of the wrapper this tool reports to")
	sessionID := flag.String("session-id", "", "session id this tool answers for")
	flag.Parse()

	if *mcpStdio {
		server := &pkg.MCPServer{
			In:         os.Stdin,
			Out:        os.Stdout,
			LogPath:    *mcpLog,
			WrapperURL: *wrapperURL,
			SessionID:  *sessionID,
			Allow:      true,
			Hold:       90 * time.Second,
		}
		if err := server.Run(context.Background()); err != nil {
			log.Fatalf("mcp server failed: %v", err)
		}
		return
	}

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
