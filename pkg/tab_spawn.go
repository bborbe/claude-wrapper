// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// TabSpawnRequest is one local-tab spawn.
type TabSpawnRequest struct {
	Prompt   string
	Label    string
	Cwd      string
	Vault    string
	Role     string
	Env      map[string]string
	WindowID *int
	// SpawnerSessionID is the caller's own session, taken from the MCP request
	// header. It becomes the row's attribution and is never defaulted.
	SpawnerSessionID string
	// ModeSource is the ResolveSpawnMode step that chose the tab, carried into the
	// response and the ledger row so a tab worker can be traced to its cause.
	ModeSource string
}

// TabSpawnResult is what a tab spawn produced.
//
// SessionID is empty when the tab never registered inside the poll deadline. That
// is a successful spawn with an unresolved identity, not a failure: the tab is
// open and usable, but there is no key to file a ledger row under and no id to
// address a follow-up message to.
// The wire names are snake_case because the rest of the MCP surface is
// (`send_agent_message` answers `agent_id`, `pane_id`, `session_id`), and a
// caller reading `agent_id` off a PascalCase payload gets nothing at all.
type TabSpawnResult struct {
	// AgentID is the handle a follow-up `send_agent_message` addresses. A tab
	// worker has one identity — its session id — so this is that id, and it is
	// empty exactly when SessionID is.
	AgentID      string `json:"agent_id"`
	Label        string `json:"label"`
	Cwd          string `json:"cwd"`
	Vault        string `json:"vault"`
	Launcher     string `json:"launcher"`
	Source       string `json:"launcher_source"`
	Model        string `json:"model"`
	Role         string `json:"role"`
	PaneID       string `json:"pane_id"`
	SessionID    string `json:"session_id"`
	TabName      string `json:"tab_name"`
	Color        string `json:"color,omitempty"`
	ColorApplied bool   `json:"color_applied"`
	Mode         string `json:"mode"`
	ModeSource   string `json:"mode_source"`
}

// TabSpawner opens a named WezTerm tab for a worker.
type TabSpawner interface {
	Spawn(ctx context.Context, request TabSpawnRequest) (*TabSpawnResult, error)
}

// NewTabSpawner creates a TabSpawner.
//
// Every collaborator is injected rather than constructed here, so the ordering
// rules this type exists to enforce can be tested without a terminal, a vault
// config or a ledger.
func NewTabSpawner(
	tabs TabClient,
	registry SessionRegistry,
	roleMap RoleMapReader,
	vaults VaultLister,
	ledger LedgerReader,
	now libtime.CurrentDateTimeGetter,
	exitRecord string,
) TabSpawner {
	return &tabSpawner{
		tabs:       tabs,
		registry:   registry,
		roleMap:    roleMap,
		vaults:     vaults,
		ledger:     ledger,
		now:        now,
		sleep:      time.Sleep,
		exitRecord: exitRecord,
	}
}

// registrationDeadline bounds the wait for a spawned tab to register itself.
//
// A tab worker is a separate process and registers about a second after its pane
// opens, so this is a poll rather than an assumption. 8s matches the node server.
const (
	registrationDeadline = 8 * time.Second
	registrationInterval = 250 * time.Millisecond
)

type tabSpawner struct {
	tabs     TabClient
	registry SessionRegistry
	roleMap  RoleMapReader
	vaults   VaultLister
	ledger   LedgerReader
	now      libtime.CurrentDateTimeGetter
	// sleep is the registration poll's wait, injected so a test clock and the
	// loop's notion of elapsed time are the same source.
	sleep func(time.Duration)
	// mu guards inFlight, the set of tab names a spawn here has claimed but not
	// yet registered.
	mu         sync.Mutex
	inFlight   map[string]struct{}
	exitRecord string
}

// Spawn opens the tab, names it, colours it and files the ledger row.
//
// The order is load-bearing and follows the node server: every refusal is
// decided BEFORE the first side effect, because a spawn that fails after opening
// a tab leaves an orphan pane the caller never learns about.
func (s *tabSpawner) Spawn(
	ctx context.Context,
	request TabSpawnRequest,
) (*TabSpawnResult, error) {
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return nil, errors.Wrapf(ctx, ErrInvalidSpawnOptions, "prompt is required")
	}
	vaults, err := s.vaults.List(ctx)
	if err != nil {
		return nil, err
	}
	// The server's own cwd is named in the refusal a bare spawn produces, so the
	// caller can see which directory a defaulted spawn would have used.
	serverCwd, _ := os.Getwd()
	target, err := ResolveWorkerTarget(ctx, vaults, request.Cwd, request.Vault, serverCwd)
	if err != nil {
		return nil, err
	}
	role, err := ResolveRole(ctx, s.roleMap, request.Role)
	if err != nil {
		return nil, err
	}
	model := LauncherModel(target.Launcher)

	label := strings.TrimSpace(request.Label)
	if label == "" {
		label = target.Vault
	}
	baseName := TabNameFor(label)
	tabName := s.reserveTabName(ctx, baseName)
	// Released once this spawn has resolved its identity: from then on the
	// registry holds the name and a later spawn sees it there.
	defer s.releaseTabName(tabName)
	// Snapshotted AFTER the name is chosen and BEFORE the process starts: a
	// session that registers under this name in the gap is not ours.
	preexisting := PreexistingHolders(ctx, s.registry, tabName)

	windowID := request.WindowID
	if windowID == nil {
		windowID = role.WindowID
	}
	paneID, err := s.tabs.Spawn(ctx, TabSpawnOptions{
		Cwd:        target.Cwd,
		Launcher:   target.Launcher,
		TabName:    tabName,
		Prompt:     prompt,
		Env:        request.Env,
		WindowID:   windowID,
		ExitRecord: s.exitRecord,
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(paneID) == "" {
		// A wezterm that answered 0 without a pane id has opened nothing we can
		// address: no colour, no message, and no handle for the caller.
		return nil, errors.Errorf(
			ctx,
			"the tab spawner reported success but named no pane, so the tab cannot be "+
				"addressed — treat this as a failed spawn rather than a worker that opened",
		)
	}
	// The tab is OPEN from here on, and everything below is bookkeeping for it. A
	// client that disconnects mid-call must not orphan an open pane with no colour
	// and no ledger row, so the tail runs on a context detached from the request.
	tailCtx := context.WithoutCancel(ctx)

	result := &TabSpawnResult{
		Label:      label,
		Cwd:        target.Cwd,
		Vault:      target.Vault,
		Launcher:   target.Launcher,
		Source:     target.Source,
		Model:      model,
		Role:       role.Role,
		PaneID:     paneID,
		TabName:    tabName,
		Mode:       SpawnModeInteractive,
		ModeSource: request.ModeSource,
	}

	// The colour is a separate submit AFTER the composer is up, and it never
	// carries the task prompt with it: `/color` takes the whole trimmed argument,
	// so `/color pink\n<task>` fails validation.
	if chip := ChipForRole(role, target.Vault); chip != "" {
		result.Color = chip
		// Reported, not assumed: the node server logs a failed colour and still
		// reports the spawn, because a worker in the wrong colour is usable and a
		// worker that never opened is not. `color_applied` is what the caller reads.
		result.ColorApplied = s.sendColor(tailCtx, paneID, chip) == nil
	}

	result.SessionID = s.awaitRegistration(tailCtx, tabName, preexisting)
	result.AgentID = result.SessionID
	s.postLedger(tailCtx, request.SpawnerSessionID, result)
	return result, nil
}

// reserveTabName picks a name free in the registry AND not already reserved by a
// spawn this process has in flight, then reserves it.
//
// The reservation is what makes choose-name → snapshot → spawn atomic within the
// daemon: two spawns with the same label (the default label is the vault name, so
// two `vault: v1` spawns collide) would otherwise both pick `⚙ v1`, both snapshot
// an empty exclusion set, and both adopt whichever session registered first — so
// the second row would carry another worker's session id against its own pane.
func (s *tabSpawner) reserveTabName(ctx context.Context, baseName string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight == nil {
		s.inFlight = map[string]struct{}{}
	}
	for n := 0; n < 100; n++ {
		candidate := baseName
		if n > 0 {
			candidate = fmt.Sprintf("%s (%d)", baseName, n+1)
		}
		if _, taken := s.inFlight[candidate]; taken {
			continue
		}
		held, err := s.registry.Named(ctx, candidate)
		if err != nil {
			// An unreadable registry is survivable: the exclusion snapshot still
			// guards the poll, and refusing to spawn over a read-only probe would be
			// the worse outcome.
			return candidate
		}
		if len(held) == 0 {
			s.inFlight[candidate] = struct{}{}
			return candidate
		}
	}
	return baseName
}

// releaseTabName frees a reservation once its spawn has resolved an identity.
func (s *tabSpawner) releaseTabName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, name)
}

// postLedger files the spawn row.
//
// Best-effort and never fatal: the tab is already open, so a store outage costs
// a record rather than a worker. A row is skipped entirely when the session id
// never resolved — there is no key to file it under, and a row keyed on anything
// else would be an entry that reads as evidence and names the wrong session.
func (s *tabSpawner) postLedger(
	ctx context.Context,
	spawnerSessionID string,
	result *TabSpawnResult,
) {
	if result.SessionID == "" || s.ledger == nil {
		return
	}
	if err := s.ledger.Post(ctx, spawnerSessionID, LedgerRow{
		// AgentID is REQUIRED for the row to be adopted: the roster skips an
		// id-less record, so a row without it leaves the worker invisible to
		// `list_agents` and unreachable by `send_agent_message`.
		AgentID:    result.AgentID,
		SessionID:  result.SessionID,
		Label:      result.Label,
		Cwd:        result.Cwd,
		Mode:       SpawnModeInteractive,
		ModeSource: result.ModeSource,
		Launcher:   result.Launcher,
		PaneID:     result.PaneID,
		Role:       result.Role,
	}); err != nil {
		// Never fatal — the worker is open — but logged, because a row that never
		// landed leaves the worker out of the roster and unreachable, and silence
		// there is indistinguishable from a spawn that filed nothing on purpose.
		log.Printf(
			"WARNING: the ledger row for session %s could not be written: %v",
			result.SessionID,
			err,
		)
	}
}

// sendColor types the colour command once the composer is ready.
//
// Best-effort by design: the node server logs a failed colour and still reports
// the spawn, because a worker in the wrong colour is usable and a worker that
// never opened is not. It goes through the same ready-gated send as any other
// message, so the colour is not typed into a pane that is still booting.
func (s *tabSpawner) sendColor(ctx context.Context, paneID, chip string) error {
	return SendWhenReady(ctx, s.tabs, paneID, "/color "+chip, s.now)
}

// awaitRegistration polls the registry for the session this spawn created.
//
// The exclusion set is what makes the join sound: without it a slow register
// from a previous spawn under the same name is indistinguishable from this one.
//
// The deadline is read from the injected clock AND the wait is an injected
// sleeper, because the two must come from the same source: a loop that compares
// an injected clock against a real `time.After` never advances under a test
// clock and spins until the process is killed.
func (s *tabSpawner) awaitRegistration(
	ctx context.Context,
	tabName string,
	preexisting map[string]struct{},
) string {
	deadline := s.now.Now().Time().Add(registrationDeadline)
	for {
		sessionID, err := FindRegisteredByName(ctx, s.registry, tabName, preexisting)
		if err == nil && sessionID != "" {
			return sessionID
		}
		if s.now.Now().Time().After(deadline) {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		default:
		}
		s.sleep(registrationInterval)
	}
}
