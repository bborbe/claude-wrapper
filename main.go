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

	libtime "github.com/bborbe/time"

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
	mcpTurnTimeout := flag.Duration("mcp-turn-timeout", 120*time.Second,
		"how long an MCP send_message waits for the child's turn to close")
	flag.Parse()

	if *mcpStdio {
		server := &pkg.MCPServer{
			In:         os.Stdin,
			Out:        os.Stdout,
			LogPath:    *mcpLog,
			Now:        libtime.NewCurrentDateTime(),
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

	supervisor := pkg.NewSupervisor(context.Background(), *claudeBin, libtime.NewCurrentDateTime())
	// One lock set for every route that can write a turn: a sequence and a
	// plain message on the same child must not interleave.
	locks := pkg.NewSessionLocks()
	sequencer := pkg.NewSequencer(
		supervisor, libtime.NewCurrentDateTime(), locks, 30*time.Minute)

	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.Healthz())
	mux.Handle("/api/1.0/sessions", handler.Sessions(supervisor))
	mux.Handle("/api/1.0/sessions/", handler.Session(supervisor, sequencer, locks))
	// The client-facing MCP face: Codex → wrapper, over streamable HTTP on this
	// same listener. Not to be confused with --mcp-stdio, which is child-facing
	// (the wrapper giving its own child a permission tool) and runs in a
	// separate process.
	mux.Handle("/mcp", &pkg.MCPToolServer{
		Supervisor:  supervisor,
		Locks:       locks,
		TurnTimeout: *mcpTurnTimeout,
	})

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("claude-wrapper listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("listen failed: %v", err)
	}
}
