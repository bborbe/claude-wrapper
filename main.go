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
	"path/filepath"
	"strings"
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

// defaultFlaggedRowsScript is the deployed copy of the fleet's eligibility
// predicate. The marketplace copy is the one the running fleet uses, so it is
// the default rather than the source checkout — a tick reading a stale source
// tree would disagree with `/supervisor:open --flagged` about what is eligible.
const defaultFlaggedRowsScript = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/flagged-rows.py"

// flaggedTickFlags are the reopen tick's two flags, registered together so
// main stays inside its length budget.
type flaggedTickFlags struct {
	interval *time.Duration
	script   *string
}

func registerFlaggedTickFlags() flaggedTickFlags {
	return flaggedTickFlags{
		interval: flag.Duration("flagged-tick-interval", pkg.DefaultFlaggedTickInterval,
			"how often to reopen flagged rows that have gone dark; 0 disables the tick"),
		script: flag.String("flagged-rows-script", defaultFlaggedRowsScript,
			"path to claude-supervisor's flagged-rows.py, which decides which rows are eligible"),
	}
}

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
	flagged := registerFlaggedTickFlags()
	stateDir := flag.String("state-dir", "",
		"directory holding session state; defaults to ~/.claude/state/claude-wrapper/sessions")
	drainBound := flag.Duration("drain-bound", 4*time.Second,
		"overall budget a SIGTERM waits for children to reach a turn boundary before recording each as interrupted; must stay under launchd's ExitTimeOut (5 s) or launchd SIGKILLs the drain before it can report")
	closeOnTerminalTaskDirs := flag.String("close-on-terminal-task-dirs", "", closeOnTerminalTaskDirsUsage)
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
	supervisor, ledger := newSupervisor(
		ctx, *claudeBin, *listen, *attentionURL, store, decider, *permissionTimeout, now,
		taskResolverFromFlag(*closeOnTerminalTaskDirs),
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

	startFleetFaces(
		ctx, mux, supervisor, decider, ledger, locks, *attentionURL, *heartbeatInterval,
		*flagged.interval, *flagged.script,
	)

	// The startup deadline is the only verb that can clear a child stuck in
	// `starting`: it never emits an init event, so nothing else reaches it.
	go startStartupSweeper(ctx, supervisor, pkg.StartupSweepInterval)

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

// newSupervisor builds the supervisor together with the one ledger client its
// turn-end verdicts are written through.
//
// ⚠️ The client is built HERE rather than in startFleetFaces because the
// supervisor needs it FIRST. The process that holds the child is the only one
// that can see its turn end, so it is the one that files the verdict; the same
// client is handed to the fleet faces afterwards, which is what keeps the spawn
// POST and the terminal PATCH one writer rather than two.
func newSupervisor(
	ctx context.Context,
	claudeBin string,
	listen string,
	attentionURL string,
	store pkg.SessionStore,
	decider *pkg.PermissionDecider,
	permissionTimeout time.Duration,
	now libtime.CurrentDateTimeGetter,
	taskResolver pkg.AnchoredTaskResolver,
) (pkg.Supervisor, pkg.LedgerReader) {
	ledger := pkg.NewLedgerClient(attentionURL, nil, now)
	return pkg.NewSupervisorWithPolicy(
		ctx,
		claudeBin,
		now,
		pkg.SelfURL(listen),
		store,
		pkg.SupervisorPolicy{
			Decider:           decider,
			PermissionTimeout: permissionTimeout,
			Ledger:            ledger,
			TaskResolver:      taskResolver,
		},
	), ledger
}

const closeOnTerminalTaskDirsUsage = "comma-separated absolute task dirs; at turn end a child whose task " +
	"(claude_session_id) is completed or aborted is closed. Empty disables"

// taskResolverFromFlag builds the close-on-terminal-task resolver; an empty
// flag returns nil, which leaves the feature off.
func taskResolverFromFlag(value string) pkg.AnchoredTaskResolver {
	var dirs []string
	for _, dir := range strings.Split(value, ",") {
		if dir = strings.TrimSpace(dir); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	log.Printf("close-on-terminal-task: watching %d task dir(s)", len(dirs))
	return pkg.NewDirAnchoredTaskResolver(dirs)
}

// startStartupSweeper terminates children that rest in `starting` past the
// configured deadline.
//
// It runs on the process lifetime: the daemon's own goroutines read ctx so a
// draining process stops them rather than leaving them to fire against a dead
// process. The interval is a sweep granularity, not the bound — the bound is
// read from the config file on every tick, so an operator's change lands on the
// next sweep with no restart.
func startStartupSweeper(
	ctx context.Context,
	supervisor pkg.Supervisor,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if stopped := supervisor.EnforceStartupDeadline(ctx); stopped > 0 {
				log.Printf("startup deadline: terminated %d session(s) stuck in starting", stopped)
			}
		}
	}
}

// startLifecycleJobs runs the ticks that used to be launchd jobs, now that the
// service holds the state they poll.
//
// Each is started in its own goroutine and each names its own failure: a job
// that dies must not take the others with it, and a silent one is exactly the
// failure the measured 8-25 minute lag came from.
func startLifecycleJobs(
	ctx context.Context,
	supervisor pkg.Supervisor,
	now libtime.CurrentDateTimeGetter,
) {
	jobs := []interface {
		Run(context.Context) error
	}{
		&pkg.IdleReaperJob{Supervisor: supervisor},
		&pkg.StateLedgerJob{Supervisor: supervisor, Now: now},
	}
	for _, job := range jobs {
		go func(j interface {
			Run(context.Context) error
		}) {
			if err := j.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("WARNING: lifecycle job stopped: %v", err)
			}
		}(job)
	}
}

// warnIfDrainOverrunsExitTimeout names the invariant when an operator has raised// --drain-bound past the point where launchd would SIGKILL the process mid-drain.
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
	// ledger is the process's one ledger client. It arrives as a parameter rather
	// than being built here because the supervisor writes the turn-end verdict
	// through the same client — one writer, one timeout, one attribution line.
	ledger pkg.LedgerReader,
	// locks is the one per-session lock set; set_agent_permission_mode fails fast on it.
	locks *pkg.SessionLocks,
	attentionURL string,
	heartbeatInterval time.Duration,
	flaggedTickInterval time.Duration,
	flaggedRowsScript string,
) {
	now := libtime.NewCurrentDateTime()
	registry := pkg.NewSessionRegistry("")
	heartbeats := pkg.NewHeartbeatClient(attentionURL, nil)

	startBoardDelivery(supervisor, decider, attentionURL)

	// The A2A spawn targets: docker and k8s workers, created on ann
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
	// The local-tab spawn path: a tab under the vault's cc-* launcher. Without
	// these the daemon refuses every `local` spawn by name.
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

	// The fleet-wide caps; shared with the flagged tick so both read one limit.
	caps := pkg.NewSpawnCaps(ledger, heartbeats, registry, now, "", nil)

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
		// The fleet-wide caps; shared with the flagged tick so both read one limit.
		Caps: caps,
		// The id → name join send_agent_message needs for a headless worker: it holds
		// the session id, the caller's `SendMessage` takes the registry name, and this
		// is the only place that maps one to the other.
		Registry: registry,
		// The same lock set the REST and /mcp faces take, so a mode change refuses
		// with `running a sequence` instead of queuing on this face only.
		Locks: locks,
		// The append-only switch history; without it every reply says `recorded: false`.
		PermissionModeLog: pkg.NewPermissionModeLog("", now),
	})

	go startFlaggedTick(
		ctx,
		flaggedTickInterval,
		flaggedRowsScript,
		pkg.NewFlaggedReopener(supervisor, tabSpawner, vaults, ledger, now),
		caps,
		ledger,
		now,
	)

	startHeartbeatPoster(ctx, registry, heartbeats, ledger, heartbeatInterval)

	// The jobs that used to run as launchd ticks. They read state this process
	// already holds, so they live here rather than outside it.
	startLifecycleJobs(ctx, supervisor, now)
}

// startBoardDelivery runs the board arms: one process reads the store for every
// child it holds, which is what removes the per-session poller from the headless
// path.
func startBoardDelivery(
	supervisor pkg.Supervisor,
	decider *pkg.PermissionDecider,
	attentionURL string,
) {
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
}

// startFlaggedTick reopens flagged rows that have gone dark.
//
// The close side is deterministic — launchd's idle-reaper closes idle headless
// workers on a 900 s tick — so the reopen side has to be too. It was a model
// step in the fleet round, measured at 0 opens across 100 digests (2026-11-11),
// which is why 19 of 28 eligible flagged tasks sat with no live session.
//
// An interval of 0 disables the tick: a tick wired to a suspect predicate
// reopens rows that are already running.
func startFlaggedTick(
	ctx context.Context,
	interval time.Duration,
	script string,
	reopener pkg.FlaggedReopener,
	caps pkg.SpawnCaps,
	ledger pkg.LedgerReader,
	now libtime.CurrentDateTimeGetter,
) {
	if interval <= 0 {
		log.Printf("flagged tick disabled (--flagged-tick-interval=%s)", interval)
		return
	}
	tick := &pkg.FlaggedTick{
		Source:   pkg.ExecFlaggedRowsSource{Script: expandHome(script)},
		Reopener: reopener,
		Caps:     caps,
		Ledger:   ledger,
		Now:      now,
		Interval: interval,
	}
	if err := tick.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("flagged tick stopped: %v", err)
	}
}

// expandHome resolves a leading `~/`, which neither exec nor the shell expands
// for a flag value that arrives pre-quoted.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}

// startHeartbeatPoster runs the central heartbeat poster.
//
// It runs beside the HTTP server rather than inside a handler because it is a
// timer, not a request: a session with no supervisor of its own has nothing
// posting for it, and this is what makes liveness independent of which server
// spawned what. The ledger goes in so the poster can tell a finished session
// from a live one, and the registry answers "does this pid exist?" — true for a
// headless worker's child long after its turn ended (see pkg/heartbeat.go's
// terminalLedgerStatuses).
func startHeartbeatPoster(
	ctx context.Context,
	registry pkg.SessionRegistry,
	heartbeats pkg.HeartbeatClient,
	ledger pkg.LedgerReader,
	heartbeatInterval time.Duration,
) {
	poster := pkg.NewHeartbeatPoster(registry, heartbeats, ledger, heartbeatInterval)
	go func() {
		if err := poster.Run(ctx); err != nil {
			log.Printf("heartbeat poster stopped: %v", err)
		}
	}()
}
