// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
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
}

// TabSpawnResult is what a tab spawn produced.
//
// SessionID is empty when the tab never registered inside the poll deadline. That
// is a successful spawn with an unresolved identity, not a failure: the tab is
// open and usable, but there is no key to file a ledger row under and no id to
// address a follow-up message to.
type TabSpawnResult struct {
	AgentID   string
	Label     string
	Cwd       string
	Vault     string
	Launcher  string
	Source    string
	Model     string
	Role      string
	PaneID    string
	SessionID string
	TabName   string
	Color     string
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
	tabs       TabClient
	registry   SessionRegistry
	roleMap    RoleMapReader
	vaults     VaultLister
	ledger     LedgerReader
	now        libtime.CurrentDateTimeGetter
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
	target, err := ResolveWorkerTarget(ctx, vaults, request.Cwd, request.Vault, "")
	if err != nil {
		return nil, err
	}
	role, err := ResolveRole(ctx, s.roleMap, request.Role)
	if err != nil {
		return nil, err
	}
	model, err := LauncherModel(ctx, target.Launcher)
	if err != nil {
		return nil, err
	}

	label := strings.TrimSpace(request.Label)
	if label == "" {
		label = target.Vault
	}
	baseName := TabNameFor(label)
	tabName := UniqueTabName(ctx, s.registry, baseName)
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

	result := &TabSpawnResult{
		Label:    label,
		Cwd:      target.Cwd,
		Vault:    target.Vault,
		Launcher: target.Launcher,
		Source:   target.Source,
		Model:    model,
		Role:     role.Role,
		PaneID:   paneID,
		TabName:  tabName,
	}

	// The colour is a separate submit AFTER the prompt is up, and it never
	// carries the task prompt with it: `/color` takes the whole trimmed argument,
	// so `/color pink\n<task>` fails validation.
	if chip := ChipForRole(role, target.Vault); chip != "" && paneID != "" {
		result.Color = chip
		_ = s.sendColor(ctx, paneID, chip)
	}

	result.SessionID = s.awaitRegistration(ctx, tabName, preexisting)
	s.postLedger(ctx, request.SpawnerSessionID, result)
	return result, nil
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
	_ = s.ledger.Post(ctx, spawnerSessionID, LedgerRow{
		SessionID: result.SessionID,
		Label:     result.Label,
		Cwd:       result.Cwd,
		Mode:      "interactive",
		Launcher:  result.Launcher,
		PaneID:    result.PaneID,
		Role:      result.Role,
	})
}

// sendColor types the colour command once the composer is ready.
//
// Best-effort by design: the node server logs a failed colour and still reports
// the spawn, because a worker in the wrong colour is usable and a worker that
// never opened is not.
func (s *tabSpawner) sendColor(ctx context.Context, paneID, chip string) error {
	return s.tabs.SendText(ctx, paneID, "/color "+chip)
}

// awaitRegistration polls the registry for the session this spawn created.
//
// The exclusion set is what makes the join sound: without it a slow register
// from a previous spawn under the same name is indistinguishable from this one.
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
		case <-time.After(registrationInterval):
		}
	}
}
