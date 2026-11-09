// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
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

// shutdownGrace is how long a response already being written may finish after the
// drain. Together with the drain bound it must stay under launchd's 5 s
// ExitTimeOut: the drain defaults to 4 s, so 500 ms leaves the process room to
// log its own exit instead of being SIGKILLed mid-line.
const shutdownGrace = 500 * time.Millisecond

// launchdExitTimeout is the job's `exit timeout`, after which launchd SIGKILLs the
// process. The drain bound and the shutdown grace together must fit inside it.
const launchdExitTimeout = 5 * time.Second

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
	stateDir := flag.String("state-dir", "",
		"directory holding session state; defaults to ~/.claude/state/claude-wrapper/sessions")
	drainBound := flag.Duration("drain-bound", 4*time.Second,
		"overall budget a SIGTERM waits for children to reach a turn boundary before recording each as interrupted; must stay under launchd's ExitTimeOut (5 s) or launchd SIGKILLs the drain before it can report")
	flag.Parse()

	if *mcpStdio {
		runMCPStdio(*mcpLog, *wrapperURL, *sessionID, *mcpHold)
		return
	}

	warnIfDrainOverrunsExitTimeout(*drainBound)

	// One signal context for the whole process.
	//
	// It replaces the poster-only NotifyContext this used to build: that one
	// registered a handler, which suppressed Go's default SIGTERM termination, and
	// then unregistered it — so the FIRST SIGTERM did nothing at all and the daemon
	// only died on the second or on a SIGKILL. `launchctl kickstart -k` sends one
	// SIGTERM, waits, then SIGKILLs, so no shutdown path could ever run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	now := libtime.NewCurrentDateTime()
	// One decider, shared: the policy path and the board path must write their
	// records through the same writer or `permissions.jsonl` interleaves two
	// half-open files.
	decider := &pkg.PermissionDecider{Log: pkg.NewPermissionLog(*permissionLog, now), Now: now}
	store := pkg.NewSessionStore(*stateDir)
	supervisor := pkg.NewSupervisorWithPolicy(
		ctx,
		*claudeBin,
		now,
		pkg.SelfURL(*listen),
		store,
		pkg.SupervisorPolicy{
			Decider:           decider,
			PermissionTimeout: *permissionTimeout,
		},
	)
	// Re-adoption runs before the listener opens, so a supervisor that connects the
	// instant the port is up already sees the sessions that survived the restart.
	if adopted, adoptErr := supervisor.Adopt(ctx); adoptErr != nil {
		log.Printf("WARNING: re-adopt failed: %v", adoptErr)
	} else if adopted > 0 {
		log.Printf("re-adopted %d live session(s) from the previous daemon", adopted)
	}

	// One lock set for every route that can write a turn: a sequence and a
	// plain message on the same child must not interleave.
	locks := pkg.NewSessionLocks()
	sequencer := pkg.NewSequencer(
		supervisor, libtime.NewCurrentDateTime(), locks, 30*time.Minute)

	mux := newMux(supervisor, sequencer, locks, *listen, *mcpTurnTimeout)

	startFleetFaces(ctx, mux, supervisor, decider, *attentionURL, *heartbeatInterval)

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("claude-wrapper listening on %s", *listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen failed: %v", err)
		}
	}()

	<-ctx.Done()
	drainAndStop(supervisor, server, *drainBound, stop)
}

// warnIfDrainOverrunsExitTimeout names the invariant when an operator has raised
// --drain-bound past the point where launchd would SIGKILL the process mid-drain.
//
// The bound is a flag, so nothing else keeps the drain and the shutdown grace
// inside launchd's exit timeout — and a silent overrun turns a drain into a kill
// that still looks like a restart.
func warnIfDrainOverrunsExitTimeout(drainBound time.Duration) {
	total := drainBound + shutdownGrace
	if total < launchdExitTimeout {
		return
	}
	log.Printf(
		"WARNING: --drain-bound %s + %s shutdown grace = %s, at or over launchd's %s ExitTimeOut; a SIGTERM will be SIGKILLed mid-drain",
		drainBound, shutdownGrace, total, launchdExitTimeout,
	)
}

// runMCPStdio serves the child-facing permission tool on stdio and returns.
//
// It is the whole of --mcp-stdio: a separate process mode, not a route. The
// wrapper gives its own child a permission tool by spawning this binary again
// with this flag, so nothing here may start the HTTP daemon.
func runMCPStdio(logPath, wrapperURL, sessionID string, hold time.Duration) {
	server := &pkg.MCPServer{
		In:         os.Stdin,
		Out:        os.Stdout,
		LogPath:    logPath,
		Now:        libtime.NewCurrentDateTime(),
		WrapperURL: wrapperURL,
		SessionID:  sessionID,
		Allow:      true,
		Hold:       hold,
	}
	if err := server.Run(context.Background()); err != nil {
		log.Fatalf("mcp server failed: %v", err)
	}
}

// newMux mounts every route the daemon serves.
func newMux(
	supervisor pkg.Supervisor,
	sequencer pkg.Sequencer,
	locks *pkg.SessionLocks,
	listen string,
	mcpTurnTimeout time.Duration,
) *http.ServeMux {
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
		TurnTimeout: mcpTurnTimeout,
	})
	// Loopback-gated: a heap profile carries session content and
	// /debug/pprof/cmdline publishes this process's argv, so the debug surface
	// is mounted only when the listener cannot be reached from another
	// interface.
	handler.RegisterPprofIfLoopback(mux, listen)
	return mux
}

// drainAndStop takes the process down once a signal has arrived.
//
// It is split out of main so the shutdown sequence reads on its own: the order
// below is the whole point — drain the children first, then close the listener
// inside what is left of launchd's ExitTimeOut.
func drainAndStop(
	supervisor pkg.Supervisor,
	server *http.Server,
	drainBound time.Duration,
	stop func(),
) {
	// A second signal during the drain is not a second chance to die: the children
	// outlive this process either way, so the drain is allowed to finish its bound.
	stop()
	log.Printf("claude-wrapper draining: waiting up to %s overall for turn boundaries", drainBound)
	report := supervisor.Drain(context.Background(), drainBound)
	for _, result := range report.Sessions {
		if result.Interrupted {
			log.Printf("drain: session %s interrupted — %s", result.ID, result.Reason)
			continue
		}
		log.Printf("drain: session %s at turn boundary (state %s)", result.ID, result.State)
	}

	// A graceful Shutdown cannot fit in what the drain leaves of launchd's 5 s
	// ExitTimeOut. Shutdown waits for active requests to finish, and an SSE handler
	// blocks until its own request context ends — so with any supervisor subscribed
	// (the normal state) it runs to its full timeout, launchd SIGKILLs the process
	// at 5 s, and the final line below is never written. A short grace covers a
	// response already being written; Close then drops the streams, which is safe
	// because the child outlives us and its stream resumes from Last-Event-ID.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	log.Printf("claude-wrapper stopped; %d session(s) continue under the next daemon", len(report.Sessions))
}

// startFleetFaces mounts the fleet-facing MCP face and starts the central
// heartbeat poster.
//
// Split out of main so the wiring is readable on its own and main stays a flag
// block plus a server.
func startFleetFaces(
	ctx context.Context,
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
		go tunnel.Run(ctx)
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
		// The fleet-wide caps (spawn.maxConcurrent / maxConcurrentHard /
		// maxManagers / maxManagersHard / maxStartsPerMinute), read per spawn from
		// the same config file as the spawn mode. Without it a spawn past the cap
		// opens instead of being refused.
		Caps: pkg.NewSpawnCaps(ledger, heartbeats, registry, now, "", nil),
	})

	// The central heartbeat poster. It runs beside the HTTP server rather than
	// inside a handler because it is a timer, not a request: a session with no
	// supervisor of its own has nothing posting for it, and this is what makes
	// liveness independent of which server spawned what.
	// The ledger goes in so the poster can tell a finished session from a live one.
	// The registry answers "does this pid exist?", which is true for a headless
	// worker's child long after its turn ended — see pkg/heartbeat.go's
	// terminalLedgerStatuses.
	poster := pkg.NewHeartbeatPoster(registry, heartbeats, ledger, heartbeatInterval)
	go func() {
		if err := poster.Run(ctx); err != nil {
			log.Printf("heartbeat poster stopped: %v", err)
		}
	}()
}
