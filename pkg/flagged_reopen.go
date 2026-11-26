// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// modeSourceFlaggedTick is the spawn-resolution step a reopen is attributed to.
const modeSourceFlaggedTick = "flagged-tick"

// FlaggedReopener opens one flagged row through the open decision.
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
	Get(ctx context.Context, id string) (*Session, error)
}

// FlaggedIdleNudger re-drives a flagged headless row whose child this process
// still holds and whose last turn has closed.
//
// ⚠️ This is the case liveness cannot resolve. A finished turn files a terminal
// ledger row, the heartbeat poster then stops stamping the session (by design),
// and the registry still shows the pid alive — so `session-liveness.py` answers
// UNKNOWN, never ABSENT, and `decision` skips the row as `not-absent` until a
// restart kills the child. Only this process can tell that child is idle, because
// it reads the child's own event stream.
type FlaggedIdleNudger interface {
	// HoldsIdleChild reports whether this process holds the row's child with no
	// turn open and no question pending.
	HoldsIdleChild(ctx context.Context, row FlaggedRow) bool
	// Nudge sends the row's work turn to that live child. It spawns nothing.
	Nudge(ctx context.Context, row FlaggedRow) error
}

// flaggedReopener opens a flagged row through the open decision, and nudges an
// idle child this process still holds.
//
// ⚠️ When Executor is set, Reopen is the same call the tick's batch path makes:
// one OpenRow through OpenExecutor.OpenTick. Routing follows the row's resolved
// role (docs/open-endpoint.md § Decision order), never the stored mode. The
// legacy mode-based spawn path below survives only for callers that wire no
// executor (the pre-006 unit fixtures); the daemon always wires one.
type flaggedReopener struct {
	// Executor is the open decision and executor. When set, Reopen routes
	// through it instead of choosing a spawn path itself.
	Executor   OpenExecutor
	Supervisor FlaggedReopenSupervisor
	Spawner    TabSpawner
	Vaults     VaultLister
	// Ledger records the headless spawn. ⚠️ A headless spawn that files no row is
	// invisible twice over: the fleet caps count by ledger role, and the tick's own
	// recently-spawned guard reads this store — so an unrecorded reopen would be
	// opened again by the next tick while its first child was still starting.
	// The tab path files its own row inside the spawner.
	Ledger LedgerReader
	Now    libtime.CurrentDateTimeGetter
	// ProjectsDir is the Claude Code projects directory the resume check reads.
	// Empty resolves to ~/.claude/projects. It is a field so a test can point it
	// at a temp dir instead of the operator's own transcripts.
	ProjectsDir string
}

// NewFlaggedReopener wires the two spawn paths a flagged row may take.
func NewFlaggedReopener(
	supervisor FlaggedReopenSupervisor,
	spawner TabSpawner,
	vaults VaultLister,
	ledger LedgerReader,
	now libtime.CurrentDateTimeGetter,
) FlaggedReopener {
	return &flaggedReopener{
		Supervisor: supervisor,
		Spawner:    spawner,
		Vaults:     vaults,
		Ledger:     ledger,
		Now:        now,
	}
}

// Reopen opens one flagged row and gives it its first turn.
func (r *flaggedReopener) Reopen(ctx context.Context, row FlaggedRow) error {
	if r.Executor != nil {
		return r.reopenThroughDecision(ctx, row)
	}
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

// reopenThroughDecision hands the row to the open decision's tick entry point.
//
// The executor derives role, stored mode and the recorded session id from the
// row's page, exactly as it does for POST /api/1.0/open; the tick supplies the
// page identity and the task row's readiness first step. A refused verdict is
// returned as its reason token and message so the caller can record the token.
func (r *flaggedReopener) reopenThroughDecision(ctx context.Context, row FlaggedRow) error {
	verdicts := r.Executor.OpenTick(ctx, []OpenRow{openRowFor(row)})
	if len(verdicts) == 0 {
		return errors.Errorf(ctx, "open executor returned no verdict for %s", row.Key())
	}
	verdict := verdicts[0]
	switch verdict.Branch {
	case OpenBranchResume, OpenBranchCreate, OpenBranchJump, OpenBranchLive, OpenBranchYours:
		return nil
	case OpenBranchRefused:
		return errors.Errorf(ctx, "%s: %s", verdict.Reason, verdict.Message)
	}
	return errors.Errorf(ctx, "open executor returned unknown branch %q", verdict.Branch)
}

// openRowFor is the open request the tick builds for a flagged row.
//
// Role, mode and the recorded session id are not copied here: the executor
// reads them from the page, and the predicate that produced the FlaggedRow read
// the same page. The one fact the tick adds is the task row's readiness first
// step, which the open contract does not know about.
func openRowFor(row FlaggedRow) OpenRow {
	openRow := OpenRow{Kind: row.Kind, Vault: row.Vault, Name: row.Name}
	if row.Kind == OpenKindTask {
		openRow.FirstStep = readinessFirstStep
	}
	return openRow
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
	resume := r.resumableSessionID(row, target.Cwd)
	session, err := r.Supervisor.Spawn(ctx, SpawnOptions{
		Cwd:            target.Cwd,
		Model:          LauncherModel(target.Launcher),
		PermissionMode: mode,
		Label:          row.Name,
		Role:           row.Role,
		Resume:         resume,
		MCPConfigs:     launcherMCPConfigs(target.Launcher),
	})
	if err != nil {
		return errors.Wrap(ctx, err, "spawn headless")
	}
	if err := r.Supervisor.Send(ctx, session.ID, prompt); err != nil {
		// The child is live by now. A reopen that leaves an idle process behind
		// looks exactly like a working worker, so it is stopped rather than
		// orphaned — the same reasoning `spawnLocalAgent` applies. A fresh context
		// because the caller's may already be cancelled, and the stop still has to
		// run: this is cleanup, not work.
		_ = r.Supervisor.Stop(context.Background(), session.ID)
		return errors.Wrap(ctx, err, "deliver opening turn")
	}
	r.recordSpawn(ctx, row, session, resume)
	return nil
}

// resumableSessionID returns the id to resume with, or "" when the row has no
// session or its stamped session has no transcript.
//
// ⚠️ A stamped claude_session_id with no transcript strands the row: the child
// is resumed, exits at once with "No conversation found with session ID"
// (num_turns: 0), and every later tick resumes the same missing conversation.
// Measured 2026-11-17: two rows stamped at spawn, killed at the startup deadline
// before any transcript was written, then resumed and exiting at once. A missing
// transcript is treated as never-opened — the row is created fresh instead.
func (r *flaggedReopener) resumableSessionID(
	row FlaggedRow,
	cwd string,
) string {
	if row.SessionID == "" {
		return ""
	}
	if r.transcriptExists(cwd, row.SessionID) {
		return row.SessionID
	}
	log.Printf(
		"flagged-tick: resume-no-transcript session=%s row=%s — re-creating instead of resuming",
		row.SessionID, row.Name,
	)
	return ""
}

// transcriptExists reports whether the stamped session has a transcript on disk.
//
// A read error that is not "not found" (a permission problem, an unresolvable
// home) returns true: keeping the resume is the safe default, because creating
// fresh beside a live conversation would start a duplicate.
func (r *flaggedReopener) transcriptExists(cwd, sessionID string) bool {
	projectsDir := r.ProjectsDir
	if projectsDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return true
		}
		projectsDir = filepath.Join(home, ".claude", "projects")
	}
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	path := filepath.Join(projectsDir, slug, sessionID+".jsonl")
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	return !os.IsNotExist(err)
}

// HoldsIdleChild reports whether the row's headless child is held and idle.
//
// Idle is read from the child's event-derived state AND its pending-turn count:
// `idle` alone also holds between two assistant messages of one turn while a tool
// runs, and a nudge there would interleave a second turn into a working one.
// A pending question means the child is parked on a gate, not finished.
func (r *flaggedReopener) HoldsIdleChild(ctx context.Context, row FlaggedRow) bool {
	if r.Supervisor == nil || row.SessionID == "" || !isHeadlessRow(row) {
		return false
	}
	session, err := r.Supervisor.Get(ctx, row.SessionID)
	if err != nil || session == nil {
		return false
	}
	return session.State == StateIdle && session.PendingTurns == 0 && session.Question == nil
}

// Nudge sends the row's work turn to the child this process already holds.
func (r *flaggedReopener) Nudge(ctx context.Context, row FlaggedRow) error {
	if r.Supervisor == nil {
		return errors.Errorf(ctx, "no supervisor is wired, so an idle child cannot be nudged")
	}
	prompt := reopenPrompt(row)
	if prompt == "" {
		return errors.Errorf(ctx, "row %s has no reopen prompt", row.Key())
	}
	if err := r.Supervisor.Send(ctx, row.SessionID, prompt); err != nil {
		return errors.Wrap(ctx, err, "deliver nudge turn")
	}
	return nil
}

// recordSpawn files the headless spawn's ledger row.
//
// A failure is logged, never returned: the spawn has already happened, and
// refusing a live worker because the store was down would turn a store outage
// into a row that is dark despite having a session.
//
// ⚠️ A RESUME PATCHES; ONLY A FRESH SPAWN POSTS. A POST replaces the whole row,
// so reopening a session a manager opened blanked the `spawner_session_id` that
// open recorded — leaving that worker's parks ownable by nobody. Measured
// 2026-11-26: 320 of 2749 sessions in the wrapper log lost their spawner this
// way. The patch names only the fields a reopen changes; a row that does not
// exist answers ErrLedgerRowAbsent and falls through to the POST below.
func (r *flaggedReopener) recordSpawn(
	ctx context.Context,
	row FlaggedRow,
	session *Session,
	resumedFrom string,
) {
	if r.Ledger == nil {
		return
	}
	if strings.TrimSpace(resumedFrom) != "" {
		if patchLedgerRow(
			ctx,
			r.Ledger,
			session.ID,
			resumeSpawnPatch(row.Name, session.Cwd, row.Role, resumedFrom),
		) {
			return
		}
	}
	if err := r.Ledger.Post(ctx, "", LedgerRow{
		SessionID:   session.ID,
		AgentID:     session.ID,
		Label:       row.Name,
		Cwd:         session.Cwd,
		Mode:        SpawnModeHeadless,
		ModeSource:  modeSourceFlaggedTick,
		Role:        row.Role,
		ResumedFrom: resumedFrom,
		Location:    "local",
		SpawnedAt:   r.now().Format(time.RFC3339),
	}); err != nil {
		log.Printf("WARNING: ledger post for reopened session %s failed: %v", session.ID, err)
	}
}

// now is the injected clock, defaulting to the real one.
func (r *flaggedReopener) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now.Now().Time()
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

// readinessFirstStep is the gate a reopened task row runs before it starts.
//
// ⚠️ The reopen tick is model-free by construction (`specs/flagged-reopen-tick.md`:
// "No model is invoked anywhere in the path"), so it cannot score a row. The
// readiness ladder — >= 9 opens, >= 7 after one improvement pass, < 7 refuses
// (`claude-supervisor:docs/fleet-surface.md` § Spawn a worker item 2) — is an agent
// call, so the gate runs where the model already is: in the worker the tick just
// spawned. Without it a below-bar row opens ungated, which is the defect the tick
// exists to close (measured 2026-11-18: a row scored 5/10 and its worker got the row
// anyway).
//
// ⚠️ **It comes BEFORE the work command, and it forbids that command below the bar.**
// Measured 2026-11-18 on a 6.5/10 fixture: with the gate appended AFTER
// `/vault-cli:work-on-task`, the row opened, the worker ran the command, and its
// chained plan-task stopped at `phase: planning` and *printed* the gaps. The card the
// instruction asked for was never posted, because the command got there first and
// consumed the turn. The stop was delivered; the card was not. The gate is therefore
// the first thing the worker reads and the last word on whether the command runs.
//
// The `/supervisor:open` branch does not carry this: it runs Gate 2 itself.
const readinessFirstStep = "FIRST STEP — readiness gate, before any other tool call, " +
	"including the command below.\n\n" +
	"Run Agent(subagent_type='vault-cli:task-auditor', description='Readiness: <task>') " +
	"on this task and read its READINESS: <score>/10 line.\n\n" +
	"- score >= 9: run the command below.\n" +
	"- score >= 7: run one improvement pass, re-read the score, then run the command below.\n" +
	"- score < 7: post the card, then STOP. Do NOT run the command below — it stops at " +
	"planning and prints the gaps, which is not the card this gate requires.\n\n" +
	"Post the card with exactly this command, substituting the row's slug and the score " +
	"and gaps the probe named:\n" +
	"  python3 ~/.claude/plugins/marketplaces/claude-supervisor/scripts/attention-ask.py " +
	"post --dedup-key readiness-<row-slug> --payload '<score>/10 — <gaps>'\n\n"

// reopenPrompt is the first turn a reopened headless task receives.
//
// ⚠️ A resume that sends NO turn is a row that dies again inside one tick: the
// idle reaper closes an `sdk-cli` session that has been idle for 15 minutes, and a
// child resumed with nothing to do is idle from its first second.
//
// A task row puts `readinessFirstStep` FIRST, then the command, because
// `/vault-cli:work-on-task` plans but never applies the readiness ladder and its
// chained plan-task consumes the turn before a trailing instruction can be acted on.
//
// Only a task row has a headless worker turn. A goal or topic opens its manager
// tab through the open decision (docs/open-endpoint.md § Manager rows), so there
// is no worker prompt — and deliberately no `/supervisor:open` worker path here.
func reopenPrompt(row FlaggedRow) string {
	if row.Kind != "task" {
		return ""
	}
	return readinessFirstStep + `/vault-cli:work-on-task "` + row.Name + `"`
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
