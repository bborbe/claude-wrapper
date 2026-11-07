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
	"os/signal"
	"syscall"
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
	attentionURL := flag.String("attention-url", "http://localhost:18080",
		"base URL of attention-controller, which serves the spawn ledger and the session-heartbeat store")
	heartbeatInterval := flag.Duration("heartbeat-interval", pkg.DefaultHeartbeatInterval,
		"how often to stamp a heartbeat for every live session")
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

	supervisor := pkg.NewSupervisor(
		context.Background(),
		*claudeBin,
		libtime.NewCurrentDateTime(),
		pkg.SelfURL(*listen),
	)
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
		Sequencer:   sequencer,
		Locks:       locks,
		TurnTimeout: *mcpTurnTimeout,
	})
	// Loopback-gated: a heap profile carries session content and
	// /debug/pprof/cmdline publishes this process's argv, so the debug surface
	// is mounted only when the listener cannot be reached from another
	// interface.
	handler.RegisterPprofIfLoopback(mux, *listen)

	startFleetFaces(mux, supervisor, *attentionURL, *heartbeatInterval)

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

// startFleetFaces mounts the fleet-facing MCP face and starts the central
// heartbeat poster.
//
// Split out of main so the wiring is readable on its own and main stays a flag
// block plus a server.
func startFleetFaces(
	mux *http.ServeMux,
	supervisor pkg.Supervisor,
	attentionURL string,
	heartbeatInterval time.Duration,
) {
	now := libtime.NewCurrentDateTime()
	registry := pkg.NewSessionRegistry("")
	ledger := pkg.NewLedgerClient(attentionURL, nil, now)
	heartbeats := pkg.NewHeartbeatClient(attentionURL, nil)

	// The fleet-facing MCP face: a Claude session reading and steering the
	// roster. A third face beside /mcp and --mcp-stdio, and separate from both on
	// purpose — /mcp is Codex driving one child, --mcp-stdio is a child asking
	// for a verdict, and this one answers across sessions.
	mux.Handle("/supervisor/mcp", &pkg.RosterToolServer{
		Roster: pkg.NewRoster(
			supervisor,
			ledger,
			pkg.NewRegistryLiveness(registry, heartbeats),
			pkg.NewTranscriptReader(registry, now),
			registry,
		),
		Supervisor: supervisor,
		Ledger:     ledger,
	})

	// The central heartbeat poster. It runs beside the HTTP server rather than
	// inside a handler because it is a timer, not a request: a session with no
	// supervisor of its own has nothing posting for it, and this is what makes
	// liveness independent of which server spawned what.
	poster := pkg.NewHeartbeatPoster(registry, heartbeats, heartbeatInterval)
	posterCtx, stopPoster := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	go func() {
		defer stopPoster()
		if err := poster.Run(posterCtx); err != nil {
			log.Printf("heartbeat poster stopped: %v", err)
		}
	}()
}
