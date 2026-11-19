// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
)

// openWriteBackTimeout bounds one `vault-cli task set` invocation.
const openWriteBackTimeout = 30 * time.Second

// openCallerMode is chosen by the entry point and by nothing in a request.
type openCallerMode int

const (
	openModeRequest openCallerMode = iota
	openModeTick
)

// OpenExecutor opens rows through the open decision, one verdict per row in
// request order.
//
// The two methods ARE the caller modes, and nothing in a row selects one:
// OpenRequest serves POST /api/1.0/open and open_rows and runs the
// answer-path probe and pane activation; OpenTick serves the flagged reopen
// tick, which is the daemon rather than a session, so it runs no probe and
// focuses no pane (docs/open-endpoint.md § Decision order item 6,
// § Answer-path probe). Rows are validated by the caller (OpenSurface,
// prompt 4) before either is reached.
type OpenExecutor interface {
	OpenRequest(ctx context.Context, rows []OpenRow, callerSessionID string) []OpenVerdict
	OpenTick(ctx context.Context, rows []OpenRow) []OpenVerdict
}

// OpenExecutorDeps are the executor's collaborators. Every one is required
// in the daemon; a test supplies fakes.
type OpenExecutorDeps struct {
	Vaults   VaultLister
	Registry SessionRegistry
	Panes    PaneJoiner
	RoleMap  RoleMapReader
	// Tabs activates a live pane on a request-mode jump.
	Tabs      TabClient
	TabOpener OpenTabOpener
	Headless  OpenHeadlessSpawner
	// Runner runs the vault-cli write-back.
	Runner CommandRunner
	// Scripts runs the answer-path probe.
	Scripts          ScriptRunner
	AnswerPathScript string
	// Logf receives the per-row log line and every WARNING; nil means log.Printf.
	Logf func(format string, args ...any)
}

// NewOpenExecutor creates the open executor.
func NewOpenExecutor(deps OpenExecutorDeps) OpenExecutor {
	return &openExecutor{
		deps:     deps,
		inFlight: map[string]struct{}{},
	}
}

type openExecutor struct {
	deps OpenExecutorDeps
	mu   sync.Mutex
	// inFlight is the per-daemon duplicate-open guard. It is in-memory only,
	// which is the accepted limitation docs/open-endpoint.md records.
	inFlight map[string]struct{}
}

// OpenRequest opens rows as a caller with a session.
func (e *openExecutor) OpenRequest(
	ctx context.Context,
	rows []OpenRow,
	callerSessionID string,
) []OpenVerdict {
	return e.openBatch(ctx, rows, openModeRequest, callerSessionID)
}

// OpenTick opens rows as the daemon's tick.
func (e *openExecutor) OpenTick(ctx context.Context, rows []OpenRow) []OpenVerdict {
	return e.openBatch(ctx, rows, openModeTick, "")
}

// openBatch lists the vaults once and opens each row in order. A client that
// disconnects stops the batch before the next row; a row already being opened
// finishes its side effects on a detached context.
func (e *openExecutor) openBatch(
	ctx context.Context,
	rows []OpenRow,
	mode openCallerMode,
	callerSessionID string,
) []OpenVerdict {
	verdicts := make([]OpenVerdict, 0, len(rows))
	vaults, err := e.listVaults(ctx)
	if err != nil {
		return e.refuseBatch(ctx, rows, verdicts, err)
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		sideCtx := context.WithoutCancel(ctx)
		verdicts = append(
			verdicts,
			e.openRow(ctx, sideCtx, vaults, row, mode, callerSessionID),
		)
	}
	return verdicts
}

// refuseBatch refuses every row when the vault list itself could not be read.
func (e *openExecutor) refuseBatch(
	ctx context.Context,
	rows []OpenRow,
	verdicts []OpenVerdict,
	err error,
) []OpenVerdict {
	message := fmt.Sprintf("vault list unreadable: %v", err)
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		verdict := newOpenVerdict(row)
		verdict.refuse(OpenRefusal{Reason: OpenReasonRowUnreadable, Message: message})
		verdicts = append(verdicts, e.finish(verdict))
	}
	return verdicts
}

// openRow decides and applies one row. Every return passes through finish, so
// exactly one log line is written per row, refusals included.
func (e *openExecutor) openRow(
	ctx context.Context,
	sideCtx context.Context,
	vaults []Vault,
	row OpenRow,
	mode openCallerMode,
	callerSessionID string,
) OpenVerdict {
	key := openRowKey(row)
	if !e.claim(key) {
		verdict := newOpenVerdict(row)
		verdict.refuse(OpenRefusal{
			Reason:  OpenReasonInFlight,
			Message: fmt.Sprintf("another open of %s is in progress in this daemon", key),
		})
		return e.finish(verdict)
	}
	defer e.release(key)

	vault, ok := openVaultByName(vaults, row.Vault)
	if !ok {
		verdict := newOpenVerdict(row)
		verdict.refuse(OpenRefusal{
			Reason:  OpenReasonRowUnreadable,
			Message: fmt.Sprintf("vault %q is not configured", row.Vault),
		})
		return e.finish(verdict)
	}

	facts := e.openRowFacts(ctx, *vault, row)
	decision := DecideOpen(facts)
	return e.finish(
		e.applyOpenDecision(ctx, sideCtx, *vault, row, facts.Page, decision, mode, callerSessionID),
	)
}

// openRowFacts reads the page, the live registry and (lazily) the panes.
func (e *openExecutor) openRowFacts(
	ctx context.Context,
	vault Vault,
	row OpenRow,
) OpenRowFacts {
	page := readOpenPage(ctx, vault, row.Kind, row.Name)
	entries, liveErr := e.liveEntries(ctx)
	live, pids := openLiveFacts(entries)
	return OpenRowFacts{
		Row:     row,
		Page:    page,
		Live:    live,
		LiveErr: liveErr,
		PaneFor: e.paneLookup(ctx, pids),
	}
}

// liveEntries reads the registry's live set.
func (e *openExecutor) liveEntries(ctx context.Context) ([]RegistryEntry, error) {
	if e.deps.Registry == nil {
		return nil, nil
	}
	return e.deps.Registry.Live(ctx)
}

// openLiveFacts splits registry entries into the decision's shape and its pids.
func openLiveFacts(entries []RegistryEntry) ([]OpenLiveEntry, []int) {
	live := make([]OpenLiveEntry, 0, len(entries))
	pids := make([]int, 0, len(entries))
	for _, entry := range entries {
		live = append(live, OpenLiveEntry{
			PID:       entry.PID,
			SessionID: entry.SessionID,
			Name:      entry.Name,
		})
		pids = append(pids, entry.PID)
	}
	return live, pids
}

// paneLookup runs the pid → pane join once, on the first question the decision
// asks. An agent row never asks, so its batch pays no join at all.
func (e *openExecutor) paneLookup(ctx context.Context, pids []int) func(int) string {
	var once bool
	var panes map[int]string
	return func(pid int) string {
		if !once {
			once = true
			panes = e.joinPanes(ctx, pids)
		}
		return panes[pid]
	}
}

// joinPanes runs the join and degrades a failure to "no pane".
func (e *openExecutor) joinPanes(ctx context.Context, pids []int) map[int]string {
	if e.deps.Panes == nil {
		return map[int]string{}
	}
	panes, err := e.deps.Panes.PanesForPIDs(ctx, pids)
	if err != nil {
		e.logf("WARNING: open: pane join failed, live rows read as pane-less: %v", err)
		return map[int]string{}
	}
	return panes
}

// applyOpenDecision performs the side effect the decision selected.
func (e *openExecutor) applyOpenDecision(
	ctx context.Context,
	sideCtx context.Context,
	vault Vault,
	row OpenRow,
	page OpenPage,
	decision OpenDecision,
	mode openCallerMode,
	callerSessionID string,
) OpenVerdict {
	switch decision.Verdict.Branch {
	case OpenBranchRefused, OpenBranchYours:
		return decision.Verdict
	case OpenBranchLive:
		e.writeBack(sideCtx, row, decision)
		return decision.Verdict
	case OpenBranchJump:
		return e.applyJump(sideCtx, row, decision, mode)
	case OpenBranchResume, OpenBranchCreate:
		return e.applySpawn(
			ctx, sideCtx, vault, row, page, decision, mode, callerSessionID,
		)
	}
	return decision.Verdict
}

// applyJump writes back and, in request mode only, focuses the live pane.
func (e *openExecutor) applyJump(
	sideCtx context.Context,
	row OpenRow,
	decision OpenDecision,
	mode openCallerMode,
) OpenVerdict {
	e.writeBack(sideCtx, row, decision)
	verdict := decision.Verdict
	if mode == openModeTick {
		return verdict
	}
	if e.deps.Tabs == nil {
		verdict.refuse(OpenRefusal{
			Reason:  OpenReasonSpawnRefused,
			Message: fmt.Sprintf("activate pane %s: no tab client is wired", verdict.PaneID),
		})
		return verdict
	}
	if err := e.deps.Tabs.Activate(sideCtx, verdict.PaneID); err != nil {
		verdict.refuse(OpenRefusal{
			Reason:  OpenReasonSpawnRefused,
			Message: fmt.Sprintf("activate pane %s: %v", verdict.PaneID, err),
		})
	}
	return verdict
}

// applySpawn routes a resume/create row to its headless or tab path.
func (e *openExecutor) applySpawn(
	ctx context.Context,
	sideCtx context.Context,
	vault Vault,
	row OpenRow,
	page OpenPage,
	decision OpenDecision,
	mode openCallerMode,
	callerSessionID string,
) OpenVerdict {
	switch decision.Verdict.Path {
	case OpenPathHeadless:
		return e.openHeadlessRow(ctx, sideCtx, row, decision, mode, callerSessionID)
	case OpenPathTab:
		return e.openTabRow(
			ctx, sideCtx, vault, row, page, decision, mode, callerSessionID,
		)
	}
	return decision.Verdict
}

// openHeadlessRow probes (request mode), writes back, then spawns headless.
func (e *openExecutor) openHeadlessRow(
	ctx context.Context,
	sideCtx context.Context,
	row OpenRow,
	decision OpenDecision,
	mode openCallerMode,
	callerSessionID string,
) OpenVerdict {
	verdict := decision.Verdict
	if mode == openModeRequest {
		if refusal := e.answerPathProbe(ctx, callerSessionID); refusal != nil {
			verdict.refuse(*refusal)
			return verdict
		}
	}
	e.writeBack(sideCtx, row, decision)
	if e.deps.Headless == nil {
		verdict.refuse(OpenRefusal{
			Reason:  OpenReasonSpawnRefused,
			Message: "open executor is not wired: no headless spawner",
		})
		return verdict
	}
	session, err := e.deps.Headless.SpawnOpenHeadless(sideCtx, OpenHeadlessRequest{
		Vault:           row.Vault,
		Task:            row.Name,
		Resume:          decision.Resume,
		Prompt:          workCommand(row),
		Shipping:        decision.Shipping,
		OperatorNamed:   decision.OperatorNamed,
		CallerSessionID: callerSessionIDFor(mode, callerSessionID),
	})
	if err != nil {
		verdict.refuse(OpenRefusal{Reason: OpenReasonSpawnRefused, Message: err.Error()})
		return verdict
	}
	verdict.SessionID = session.ID
	return verdict
}

// openTabRow reads the role map per row, resolves the window, writes back and
// opens the tab.
func (e *openExecutor) openTabRow(
	ctx context.Context,
	sideCtx context.Context,
	vault Vault,
	row OpenRow,
	page OpenPage,
	decision OpenDecision,
	mode openCallerMode,
	callerSessionID string,
) OpenVerdict {
	verdict := decision.Verdict
	if key, ok := rolePurposes[verdict.Role]; ok {
		verdict.WindowKey = key
	}
	entries, readErr := e.readRoleMap(ctx)
	window, refusal := ResolveOpenWindow(verdict.Role, row.Vault, entries, readErr)
	if refusal != nil {
		verdict.refuse(*refusal)
		return verdict
	}
	e.writeBack(sideCtx, row, decision)
	if e.deps.TabOpener == nil {
		verdict.refuse(OpenRefusal{
			Reason:  OpenReasonSpawnRefused,
			Message: "open executor is not wired: no tab opener",
		})
		return verdict
	}
	result, err := e.deps.TabOpener.Open(sideCtx, OpenTabRequest{
		Kind:             row.Kind,
		Role:             verdict.Role,
		Subject:          row.Name,
		PagePath:         page.Path,
		Vault:            vault,
		WindowID:         window.ID,
		Chip:             window.Chip,
		Resume:           decision.Resume,
		FirstStep:        row.FirstStep,
		TaskStatus:       page.Fields["status"],
		OperatorNamed:    decision.OperatorNamed,
		SpawnerSessionID: callerSessionIDFor(mode, callerSessionID),
	})
	if err != nil {
		verdict.refuse(OpenRefusal{Reason: OpenReasonSpawnRefused, Message: err.Error()})
		return verdict
	}
	windowID := window.ID
	verdict.WindowID = &windowID
	verdict.SessionID = result.SessionID
	verdict.PaneID = result.PaneID
	verdict.Message = result.Note
	return verdict
}

// readRoleMap reads the role map for one row. It is never cached.
func (e *openExecutor) readRoleMap(ctx context.Context) (map[string]RoleMapEntry, error) {
	if e.deps.RoleMap == nil {
		return nil, errors.Errorf(ctx, "no role map reader is wired")
	}
	return e.deps.RoleMap.Read(ctx)
}

// answerPathProbe runs the probe that gates every request-mode headless spawn.
func (e *openExecutor) answerPathProbe(
	ctx context.Context,
	callerSessionID string,
) *OpenRefusal {
	if callerSessionID == "" {
		return newOpenRefusal(
			OpenReasonAnswerPath,
			"no X-Claude-Code-Session-Id header, so the answer-path probe has no session to check",
		)
	}
	if e.deps.Scripts == nil {
		return newOpenRefusal(OpenReasonAnswerPath, "the answer-path probe is not wired")
	}
	result, err := e.deps.Scripts.RunScript(
		ctx, openPython, e.deps.AnswerPathScript, "--session-id", callerSessionID,
	)
	if err != nil {
		return newOpenRefusal(OpenReasonAnswerPath, err.Error())
	}
	if result.ExitCode == 0 {
		return nil
	}
	message := strings.TrimSpace(result.Stdout)
	if message == "" {
		message = strings.TrimSpace(result.Stderr)
	}
	if message == "" {
		message = fmt.Sprintf("answer-path-check.py exited %d", result.ExitCode)
	}
	return newOpenRefusal(OpenReasonAnswerPath, message)
}

// writeBack writes a defaulted or normalised task value. A failure is a
// WARNING and never changes the verdict.
func (e *openExecutor) writeBack(ctx context.Context, row OpenRow, decision OpenDecision) {
	if row.Kind != OpenKindTask || e.deps.Runner == nil {
		return
	}
	e.writeBackField(ctx, row, "role", decision.WriteRole)
	e.writeBackField(ctx, row, "mode", decision.WriteMode)
}

// writeBackField runs one `vault-cli task set` as an argv, never a shell.
func (e *openExecutor) writeBackField(
	ctx context.Context,
	row OpenRow,
	field string,
	value string,
) {
	if value == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, openWriteBackTimeout)
	defer cancel()
	_, err := e.deps.Runner.Run(
		writeCtx, "vault-cli", "--vault", row.Vault, "task", "set", row.Name, field, value,
	)
	if err != nil {
		e.logf(
			"WARNING: open: write-back %s=%s for %s/%s failed: %v",
			field, value, row.Vault, row.Name, err,
		)
	}
}

// finish logs the row's one line and returns the verdict.
func (e *openExecutor) finish(verdict OpenVerdict) OpenVerdict {
	e.logf("%s", FormatOpenLogLine(verdict))
	return verdict
}

// logf writes through the injected logger, or log.Printf when none is given.
func (e *openExecutor) logf(format string, args ...any) {
	if e.deps.Logf != nil {
		e.deps.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// listVaults reads the configured vaults once per batch.
func (e *openExecutor) listVaults(ctx context.Context) ([]Vault, error) {
	if e.deps.Vaults == nil {
		return nil, errors.Errorf(ctx, "no vault lister is wired")
	}
	return e.deps.Vaults.List(ctx)
}

// openVaultByName resolves the row's vault from the batch's list.
func openVaultByName(vaults []Vault, name string) (*Vault, bool) {
	for i := range vaults {
		if vaults[i].Name == name {
			return &vaults[i], true
		}
	}
	return nil, false
}

// newOpenVerdict is the verdict a row carries before any fact is read.
func newOpenVerdict(row OpenRow) OpenVerdict {
	return OpenVerdict{
		Kind:  row.Kind,
		Vault: row.Vault,
		Name:  row.Name,
		Path:  OpenPathNone,
	}
}

// openRowKey is the in-flight guard's identity for a row.
func openRowKey(row OpenRow) string {
	return row.Vault + "/" + row.Kind + "/" + row.Name
}

// workCommand is the opening turn an agent row receives.
func workCommand(row OpenRow) string {
	command := fmt.Sprintf(`/vault-cli:work-on-task "%s"`, row.Name)
	if row.FirstStep == "" {
		return command
	}
	return row.FirstStep + "\n\n" + command
}

// callerSessionIDFor gives the tick mode no caller: the tick is the daemon.
func callerSessionIDFor(mode openCallerMode, callerSessionID string) string {
	if mode == openModeRequest {
		return callerSessionID
	}
	return ""
}

// claim takes the row's in-flight key, or reports that another open holds it.
func (e *openExecutor) claim(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.inFlight[key]; ok {
		return false
	}
	e.inFlight[key] = struct{}{}
	return true
}

// release frees the row's in-flight key.
func (e *openExecutor) release(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.inFlight, key)
}
