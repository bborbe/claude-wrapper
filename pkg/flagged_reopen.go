// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"strings"

	"github.com/bborbe/errors"
)

// modeSourceFlaggedTick is the spawn-resolution step a reopen is attributed to.
const modeSourceFlaggedTick = "flagged-tick"

// FlaggedReopener opens one flagged row through the spawn path its mode names.
//
// An interface rather than a direct call for two reasons: the tick's decision
// pass can be tested without a supervisor, a tab client or a vault list, and the
// reopen itself can be tested against recording fakes — which is exactly what the
// empty-`prompt` defect needed. The tick's own fake accepted every request it was
// handed, so an always-refused tick passed the whole guard suite.
type FlaggedReopener interface {
	Reopen(ctx context.Context, row FlaggedRow) error
}

// FlaggedReopenSupervisor is the slice of Supervisor a headless reopen needs.
//
// Narrowed so a test needs three methods rather than the whole child-process
// surface; the production Supervisor satisfies it unchanged.
type FlaggedReopenSupervisor interface {
	Spawn(ctx context.Context, options SpawnOptions) (*Session, error)
	Send(ctx context.Context, id string, text string) error
	Stop(ctx context.Context, id string) error
}

// flaggedReopener routes a row to the spawn path its own mode names.
//
// ⚠️ The two paths are NOT interchangeable. `spawnTabAgent` refuses `resume`
// outright (`pkg/roster_mcp.go`): a tab launches the vault's `cc-*` script, which
// is never handed `--resume`, so a tab spawn of a row that already has a
// `claude_session_id` would open a fresh conversation while the tick believed it
// continued one. A headless row therefore goes through the Supervisor, and only a
// `mode: interactive` row goes to the tab spawner.
type flaggedReopener struct {
	Supervisor FlaggedReopenSupervisor
	Spawner    TabSpawner
	Vaults     VaultLister
}

// NewFlaggedReopener wires the two spawn paths a flagged row may take.
func NewFlaggedReopener(
	supervisor FlaggedReopenSupervisor,
	spawner TabSpawner,
	vaults VaultLister,
) FlaggedReopener {
	return &flaggedReopener{Supervisor: supervisor, Spawner: spawner, Vaults: vaults}
}

// Reopen opens one flagged row and gives it its first turn.
func (r *flaggedReopener) Reopen(ctx context.Context, row FlaggedRow) error {
	vaults, err := r.Vaults.List(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "list vaults")
	}
	serverCwd, _ := os.Getwd()
	target, err := ResolveWorkerTarget(ctx, vaults, "", row.Vault, serverCwd)
	if err != nil {
		return errors.Wrap(ctx, err, "resolve worker target")
	}
	prompt := reopenPrompt(row)
	if !isHeadlessRow(row) {
		if _, err := r.Spawner.Spawn(ctx, TabSpawnRequest{
			Prompt:     prompt,
			Label:      row.Name,
			Cwd:        target.Cwd,
			Vault:      row.Vault,
			Role:       row.Role,
			ModeSource: modeSourceFlaggedTick,
		}); err != nil {
			return errors.Wrap(ctx, err, "spawn tab")
		}
		return nil
	}
	return r.reopenHeadless(ctx, row, target, prompt)
}

// reopenHeadless resumes the row's own conversation, or starts one when it has no
// session id, and sends the work turn.
func (r *flaggedReopener) reopenHeadless(
	ctx context.Context,
	row FlaggedRow,
	target *WorkerTarget,
	prompt string,
) error {
	if r.Supervisor == nil {
		return errors.Errorf(
			ctx,
			"no supervisor is wired, so a headless row cannot be reopened",
		)
	}
	mode, _ := headlessPermissionMode(false)
	session, err := r.Supervisor.Spawn(ctx, SpawnOptions{
		Cwd:            target.Cwd,
		Model:          LauncherModel(target.Launcher),
		PermissionMode: mode,
		Label:          row.Name,
		Role:           row.Role,
		Resume:         row.SessionID,
		MCPConfigs:     launcherMCPConfigs(target.Launcher),
	})
	if err != nil {
		return errors.Wrap(ctx, err, "spawn headless")
	}
	if err := r.Supervisor.Send(ctx, session.ID, prompt); err != nil {
		// The child is live by now. A reopen that leaves an idle process behind
		// looks exactly like a working worker, so it is stopped rather than
		// orphaned — the same reasoning `spawnLocalAgent` applies.
		_ = r.Supervisor.Stop(context.Background(), session.ID)
		return errors.Wrap(ctx, err, "deliver opening turn")
	}
	return nil
}

// launcherMCPConfigs returns the launcher's MCP config file, or nil.
//
// Without it the child receives only the generated `claude-wrapper` config, so
// every server the vault declares is absent and a reopened manager comes up
// without the supervisor tools.
func launcherMCPConfigs(launcher string) []string {
	if config := LauncherMCPConfig(launcher); config != "" {
		return []string{config}
	}
	return nil
}

// reopenPrompt is the first turn a reopened worker receives.
//
// ⚠️ A resume that sends NO turn is a row that dies again inside one tick: the
// idle reaper closes an `sdk-cli` session that has been idle for 15 minutes, and a
// child resumed with nothing to do is idle from its first second. The reopen
// therefore sends the same work command `/supervisor:open` sends on its create
// branch.
func reopenPrompt(row FlaggedRow) string {
	if row.Kind == "task" {
		return `/vault-cli:work-on-task "` + row.Name + `"`
	}
	return `/supervisor:open "` + row.Name + `"`
}

// isHeadlessRow reports whether the row's own mode is headless.
func isHeadlessRow(row FlaggedRow) bool {
	if mode := strings.TrimSpace(row.Mode); mode != "" {
		return mode == SpawnModeHeadless
	}
	// An absent mode is never defaulted for the caps (AC8), but the routing still
	// needs an answer. The operator ruling is that agents are headless always, so
	// an agent row carrying no recorded mode is a headless row.
	return row.Role == RoleAgent
}
