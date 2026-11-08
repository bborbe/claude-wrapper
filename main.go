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
	"github.com/bborbe/claude-wrapper/pkg/a2a"
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
	mcpHold := flag.Duration("mcp-hold", 90*time.Second,
		"how long the child-facing permission tool waits for an answer before returning its default")
	permissionTimeout := flag.Duration("permission-timeout", pkg.DefaultPermissionTimeout,
		"how long an unanswered headless park waits before it is denied; 0 disables the timeout")
	permissionLog := flag.String("permission-log", "",
		"path to append permissions.jsonl to; defaults to $SUPERVISOR_PERMISSION_LOG or ~/.local/state/claude-supervisor/permissions.jsonl")
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
			Hold:       *mcpHold,
		}
		if err := server.Run(context.Background()); err != nil {
			log.Fatalf("mcp server failed: %v", err)
		}
		return
	}

	now := libtime.NewCurrentDateTime()
	// One decider, shared: the policy path and the board path must write their
	// records through the same writer or `permissions.jsonl` interleaves two
	// half-open files.
	decider := &pkg.PermissionDecider{Log: pkg.NewPermissionLog(*permissionLog, now), Now: now}
	supervisor := pkg.NewSupervisorWithPolicy(
		context.Background(),
		*claudeBin,
		now,
		pkg.SelfURL(*listen),
		pkg.SupervisorPolicy{
			Decider:           decider,
			PermissionTimeout: *permissionTimeout,
		},
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

	startFleetFaces(mux, supervisor, decider, *attentionURL, *heartbeatInterval)

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
	decider *pkg.PermissionDecider,
	attentionURL string,
	heartbeatInterval time.Duration,
) {
	now := libtime.NewCurrentDateTime()
	registry := pkg.NewSessionRegistry("")
	ledger := pkg.NewLedgerClient(attentionURL, nil, now)
	heartbeats := pkg.NewHeartbeatClient(attentionURL, nil)

	// The board arms. One process reads the store for every child it holds, which
	// is what removes the per-session poller from the headless path.
	deliveryCtx, stopDelivery := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	delivery := &pkg.BoardDelivery{
		Supervisor: supervisor,
		Store:      pkg.NewBoardStore(attentionURL, nil),
		Decider:    decider,
	}
	go func() {
		defer stopDelivery()
		if err := delivery.Run(deliveryCtx); err != nil {
			log.Printf("board delivery stopped: %v", err)
		}
	}()

	// The A2A spawn targets: docker and k8s workers, created on an
	// a2a-worker-controller. A configuration that cannot be read disables the
	// tools rather than failing the daemon — a daemon with no controller is a
	// daemon whose local work still runs.
	a2aConfig, a2aErr := a2a.LoadConfig(context.Background(), "")
	if a2aErr != nil {
		log.Printf("WARNING: A2A spawn targets disabled: %v", a2aErr)
		a2aConfig = a2a.Config{}
	}
	pushRegistry := a2a.NewPushRegistry("host.docker.internal")
	a2aManager := a2a.NewManager(a2a.NewClient(a2a.NewID), pushRegistry, a2aConfig)
	// ⚠️ THE MAC CANNOT ACCEPT AN INBOUND CONNECTION FROM THE CLUSTER, so a
	// cluster worker's push is pulled from an in-cluster relay instead. No relay
	// configured means no cluster status channel, and the spawn still works.
	if tunnel, tunnelErr := a2a.NewTunnel(
		a2aConfig.RelayURL, a2aConfig.RelaySecret, pushRegistry,
	); tunnelErr == nil {
		go tunnel.Run(context.Background())
	}

	// The fleet-facing MCP face: a Claude session reading and steering the
	// roster. A third face beside /mcp and --mcp-stdio, and separate from both on
	// purpose — /mcp is Codex driving one child, --mcp-stdio is a child asking
	// for a verdict, and this one answers across sessions.
	// The local-tab spawn path: a WezTerm tab under the vault's cc-* launcher.
	// Without these two the daemon refuses every `local` spawn by name, which is
	// why they are wired here rather than left for a caller to construct.
	tabs := pkg.NewTabClient("")
	vaults := pkg.NewVaultCLILister()
	tabSpawner := pkg.NewTabSpawner(
		tabs,
		registry,
		pkg.NewRoleMapReader(""),
		vaults,
		ledger,
		now,
		// No exit recorder: the daemon ships no copy of the script, and an empty
		// path omits the hook rather than naming one that does not exist.
		"",
	)

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
		Decider:    decider,
		A2A:        a2aManager,
		Spawner:    tabSpawner,
		// The operator's spawn mode: SUPERVISOR_SPAWN_MODE, then `spawn.mode` in
		// ~/.config/claude-supervisor/config.json. Without it every spawn that
		// omits `interactive` opens a tab regardless of the configured mode.
		SpawnMode: pkg.NewSpawnModeSourcesReader("", nil),
		Vaults:    vaults,
		Tabs:      tabs,
		Now:       now,
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
