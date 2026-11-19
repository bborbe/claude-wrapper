// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// OpenModeSource is the mode_source an open-endpoint spawn files in the
// spawn ledger, so a worker can be traced to the open that made it.
const OpenModeSource = "open"

// openPython is the interpreter the open endpoint's scripts run under.
const openPython = "python3"

// The ready-gated waits before a manager's start commands. The second goal
// command waits longer because the first one runs a planning turn.
const (
	openStartWait    = 60 * time.Second
	openGoalLoopWait = 300 * time.Second
)

// OpenTabRequest is one human or manager tab the open executor asks for.
// The window is already resolved (ResolveOpenWindow); this type never reads
// the role map.
type OpenTabRequest struct {
	// Kind is task, goal or topic; Role is human or manager.
	Kind string
	Role string
	// Subject is the page title; PagePath its absolute path.
	Subject  string
	PagePath string
	Vault    Vault
	WindowID int
	Chip     string
	// Resume is the recorded session id; empty on create.
	Resume string
	// FirstStep is typed in front of a human row's work command.
	FirstStep string
	// TaskStatus is the task page's `status:`, for the terminal-task guard.
	TaskStatus       string
	OperatorNamed    bool
	SpawnerSessionID string
}

// OpenTabResult is what an opened tab produced.
type OpenTabResult struct {
	PaneID    string
	SessionID string
	TabName   string
	Launcher  string
	// Note reports a start command that was not sent; empty when every one landed.
	Note string
}

// OpenTabOpener opens a human row's Direct tab or a manager's Managers tab.
type OpenTabOpener interface {
	Open(ctx context.Context, request OpenTabRequest) (*OpenTabResult, error)
}

// NewOpenTabOpener wires the tab half of the open endpoint.
func NewOpenTabOpener(
	tabs TabClient,
	registry SessionRegistry,
	vaults VaultLister,
	caps SpawnCaps,
	ledger LedgerReader,
	scripts ScriptRunner,
	now libtime.CurrentDateTimeGetter,
	managerLauncherScript string,
	topicSpawnScript string,
	exitRecord string,
) OpenTabOpener {
	return &openTabOpener{
		tabs:                  tabs,
		registry:              registry,
		vaults:                vaults,
		caps:                  caps,
		ledger:                ledger,
		scripts:               scripts,
		now:                   now,
		managerLauncherScript: managerLauncherScript,
		topicSpawnScript:      topicSpawnScript,
		exitRecord:            exitRecord,
		sleep:                 time.Sleep,
	}
}

type openTabOpener struct {
	tabs                  TabClient
	registry              SessionRegistry
	vaults                VaultLister
	caps                  SpawnCaps
	ledger                LedgerReader
	scripts               ScriptRunner
	now                   libtime.CurrentDateTimeGetter
	managerLauncherScript string
	topicSpawnScript      string
	exitRecord            string
	// sleep is the registration poll's wait, injected so a test clock and the
	// loop's notion of elapsed time are the same source.
	sleep func(time.Duration)
}

// Open opens one tab, following docs/open-endpoint.md § Human rows and
// § Manager rows.
//
// Every refusal is decided before the first side effect (the pane). From the
// moment the pane exists the tab is open, so all bookkeeping runs on a context
// detached from the caller and never returns an error.
func (o *openTabOpener) Open(
	ctx context.Context,
	request OpenTabRequest,
) (*OpenTabResult, error) {
	if err := validateOpenTabRequest(ctx, request); err != nil {
		return nil, err
	}
	if err := o.refuseFinishedTask(ctx, request); err != nil {
		return nil, err
	}
	launcher, err := o.resolveLauncher(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := o.runTopicCeiling(ctx, request); err != nil {
		return nil, err
	}
	if err := o.enforceCaps(ctx, request); err != nil {
		return nil, err
	}
	tabName := openTabName(request)
	var preexisting map[string]struct{}
	if request.Resume == "" {
		// Snapshotted BEFORE the pane exists, so a session that registers under
		// this name in the gap is not adopted as ours.
		preexisting = PreexistingHolders(ctx, o.registry, tabName)
	}
	paneID, err := o.spawnTab(ctx, request, launcher, tabName)
	if err != nil {
		return nil, err
	}
	tailCtx := context.WithoutCancel(ctx)
	result := &OpenTabResult{PaneID: paneID, TabName: tabName, Launcher: launcher}
	result.SessionID = o.resolveSessionID(tailCtx, request, tabName, preexisting)
	o.postLedger(tailCtx, request, result)
	result.Note = o.sendStartCommands(tailCtx, request, paneID)
	return result, nil
}

// validateOpenTabRequest refuses a request no tab can serve.
func validateOpenTabRequest(ctx context.Context, request OpenTabRequest) error {
	if err := RefuseAgentTab(ctx, request.Role); err != nil {
		return err
	}
	role := strings.TrimSpace(request.Role)
	if role != RoleHuman && role != RoleManager {
		return errors.Errorf(ctx, "open tab: role %q has no tab", request.Role)
	}
	if strings.TrimSpace(request.Vault.Path) == "" {
		return errors.Errorf(
			ctx, "open tab: vault %q has no path, so the tab has no cwd", request.Vault.Name,
		)
	}
	return nil
}

// refuseFinishedTask applies the terminal-task resume guard to a task row.
func (o *openTabOpener) refuseFinishedTask(
	ctx context.Context,
	request OpenTabRequest,
) error {
	if request.Kind != OpenKindTask || request.Resume == "" {
		return nil
	}
	return refuseTerminalTask(ctx, request.Resume, &TaskRef{
		Title:  request.Subject,
		Status: request.TaskStatus,
		Vault:  request.Vault.Name,
	})
}

// resolveLauncher picks the launcher for the row's role.
func (o *openTabOpener) resolveLauncher(
	ctx context.Context,
	request OpenTabRequest,
) (string, error) {
	if strings.TrimSpace(request.Role) == RoleManager {
		return o.managerLauncher(ctx, request)
	}
	launcher, _, err := humanLauncher(ctx, &request.Vault, request.Subject)
	return launcher, err
}

// humanLauncher resolves matrix Rule 4: the task's own launcher, then the
// `-claude` sibling of claude_script, then manager_launcher, then claude_script.
func humanLauncher(
	ctx context.Context,
	vault *Vault,
	subject string,
) (launcher, source string, err error) { //nolint:unparam // source is Rule 4's answer
	if value := strings.TrimSpace(TaskLauncher(vault.Path, subject)); value != "" {
		if err := validateLauncher(ctx, value, "launcher", vault.Name); err != nil {
			return "", "", err
		}
		return value, "task", nil
	}
	if claudeScript := strings.TrimSpace(vault.ClaudeScript); claudeScript != "" {
		sibling := claudeSibling(claudeScript)
		if launcherExists(sibling) {
			if err := validateLauncher(ctx, sibling, "claude_script", vault.Name); err != nil {
				return "", "", err
			}
			return sibling, "claude-sibling", nil
		}
	}
	if managerLauncher := strings.TrimSpace(vault.ManagerLauncher); managerLauncher != "" {
		if err := validateLauncher(ctx, managerLauncher, "manager_launcher", vault.Name); err != nil {
			return "", "", err
		}
		return managerLauncher, "manager", nil
	}
	if claudeScript := strings.TrimSpace(vault.ClaudeScript); claudeScript != "" {
		if err := validateLauncher(ctx, claudeScript, "claude_script", vault.Name); err != nil {
			return "", "", err
		}
		return claudeScript, "vault", nil
	}
	return "", "", errors.Wrapf(
		ctx, ErrNoLauncher, "vault %q has no launcher for a human row", vault.Name,
	)
}

// claudeSibling ports manager-launcher.py's sibling(): the `-claude` sibling of
// a claude script, unchanged when it is already one.
func claudeSibling(claudeScript string) string {
	dir, base := filepath.Split(claudeScript)
	base = strings.TrimSuffix(base, "-deepseek")
	if strings.HasSuffix(base, "-claude") {
		return claudeScript
	}
	return filepath.Join(dir, base+"-claude")
}

// launcherExists reports whether a launcher value can be executed.
//
// A value containing a path separator is a file path and is checked with
// FileExists; a bare name is looked up on PATH.
func launcherExists(value string) bool {
	if strings.Contains(value, "/") {
		return FileExists(value)
	}
	_, err := exec.LookPath(value)
	return err == nil
}

// managerLauncher runs manager-launcher.py and returns the launcher it prints.
func (o *openTabOpener) managerLauncher(
	ctx context.Context,
	request OpenTabRequest,
) (string, error) {
	if strings.TrimSpace(request.Vault.ManagerLauncher) == "" {
		return "", errors.Errorf(
			ctx,
			"vault %q has no manager_launcher in vault-cli config, so no manager launcher can be resolved",
			request.Vault.Name,
		)
	}
	result, err := o.scripts.RunScript(
		ctx,
		openPython,
		o.managerLauncherScript,
		request.Vault.ManagerLauncher,
		request.PagePath,
		"--default-source", "manager-default",
	)
	if err != nil {
		return "", errors.Wrap(ctx, err, "manager-launcher.py could not run")
	}
	if result.ExitCode != 0 {
		return "", errors.Errorf(ctx, "%s", managerLauncherRefusal(result))
	}
	launcher := firstLauncherField(result.Stdout)
	if launcher == "" {
		return "", errors.Errorf(ctx, "manager-launcher.py printed no launcher")
	}
	if err := validateLauncher(ctx, launcher, "manager_launcher", request.Vault.Name); err != nil {
		return "", err
	}
	return launcher, nil
}

// managerLauncherRefusal is the message a non-zero manager-launcher.py passes
// through: its stderr, else its stdout, else its exit code.
func managerLauncherRefusal(result ScriptResult) string {
	if text := strings.TrimSpace(result.Stderr); text != "" {
		return text
	}
	if text := strings.TrimSpace(result.Stdout); text != "" {
		return text
	}
	return fmt.Sprintf("manager-launcher.py exited %d", result.ExitCode)
}

// firstLauncherField returns the first tab-separated field of the first stdout
// line, trimmed.
func firstLauncherField(stdout string) string {
	line := stdout
	if index := strings.IndexByte(line, '\n'); index >= 0 {
		line = line[:index]
	}
	fields := strings.Split(line, "\t")
	return strings.TrimSpace(fields[0])
}

// runTopicCeiling runs topic-spawn.py for a topic create and passes its refusal
// through unchanged.
func (o *openTabOpener) runTopicCeiling(ctx context.Context, request OpenTabRequest) error {
	if request.Kind != OpenKindTopic || request.Resume != "" {
		return nil
	}
	result, err := o.scripts.RunScript(
		ctx,
		openPython,
		o.topicSpawnScript,
		"--vault", request.Vault.Path,
		"--topic", request.Subject,
		"--topic-open", "--hand-run",
	)
	if err != nil {
		return errors.Wrap(ctx, err, "topic-spawn.py could not run")
	}
	lines := nonEmptyLines(result.Stdout)
	if result.ExitCode == 0 && len(lines) > 0 && lines[0] == "open-ok: "+request.Subject {
		return nil
	}
	return errors.Errorf(ctx, "%s", topicCeilingRefusal(result, lines))
}

// topicCeilingRefusal is the message a refusing topic-spawn.py passes through:
// its second stdout line, else its first, else stderr, else its exit code.
func topicCeilingRefusal(result ScriptResult, lines []string) string {
	if len(lines) > 1 {
		return lines[1]
	}
	if len(lines) > 0 {
		return lines[0]
	}
	if text := strings.TrimSpace(result.Stderr); text != "" {
		return text
	}
	return fmt.Sprintf("topic-spawn.py exited %d", result.ExitCode)
}

// nonEmptyLines returns the trimmed, non-empty lines of text.
func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// enforceCaps runs the shared caps guard for the row's role.
func (o *openTabOpener) enforceCaps(ctx context.Context, request OpenTabRequest) error {
	if strings.TrimSpace(request.Role) != RoleManager {
		return enforceSpawnCaps(ctx, o.caps, SpawnModeInteractive, nil, request.OperatorNamed)
	}
	kind := managerKindForOpenKind(request.Kind)
	return enforceSpawnCaps(
		ctx,
		o.caps,
		SpawnModeInteractive,
		func(ctx context.Context) (string, []string) {
			return kind, o.vaultRoots(ctx)
		},
		request.OperatorNamed,
	)
}

// managerKindForOpenKind maps a goal or topic open to its manager cap kind.
func managerKindForOpenKind(kind string) string {
	if kind == OpenKindTopic {
		return ManagerKindTopic
	}
	return ManagerKindGoal
}

// vaultRoots mirrors RosterToolServer.vaultRoots: the configured vault paths,
// or nil when the lister is absent or the list cannot be read.
func (o *openTabOpener) vaultRoots(ctx context.Context) []string {
	if o.vaults == nil {
		return nil
	}
	vaults, err := o.vaults.List(ctx)
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(vaults))
	for _, vault := range vaults {
		if vault.Path != "" {
			roots = append(roots, vault.Path)
		}
	}
	return roots
}

// openTabName is the tab title the row opens under.
func openTabName(request OpenTabRequest) string {
	if strings.TrimSpace(request.Role) == RoleManager {
		return request.Subject + " Manager"
	}
	return request.Subject
}

// openTabColour is the `/color` command a tab starts with, or "" for none.
func openTabColour(chip string) string {
	if chip == "" {
		return ""
	}
	return "/color " + chip
}

// spawnTab opens the pane and refuses a spawn that named no pane.
func (o *openTabOpener) spawnTab(
	ctx context.Context,
	request OpenTabRequest,
	launcher string,
	tabName string,
) (string, error) {
	windowID := request.WindowID
	paneID, err := o.tabs.Spawn(ctx, TabSpawnOptions{
		Cwd:             request.Vault.Path,
		Launcher:        launcher,
		TabName:         tabName,
		Prompt:          openTabColour(request.Chip),
		WindowID:        &windowID,
		ExitRecord:      o.exitRecord,
		Resume:          request.Resume,
		ResetSessionEnv: true,
	})
	if err != nil {
		return "", err
	}
	if err := requirePaneID(ctx, paneID); err != nil {
		return "", err
	}
	return paneID, nil
}

// resolveSessionID is the resumed id, or the id the new tab registers under.
func (o *openTabOpener) resolveSessionID(
	ctx context.Context,
	request OpenTabRequest,
	tabName string,
	preexisting map[string]struct{},
) string {
	if request.Resume != "" {
		return request.Resume
	}
	return awaitTabRegistration(ctx, o.registry, o.now, o.sleep, tabName, preexisting)
}

// postLedger files the open's spawn row. Best-effort: a failure is logged and
// never returned, because the tab is already open.
func (o *openTabOpener) postLedger(
	ctx context.Context,
	request OpenTabRequest,
	result *OpenTabResult,
) {
	if result.SessionID == "" || o.ledger == nil {
		return
	}
	if err := o.ledger.Post(ctx, request.SpawnerSessionID, LedgerRow{
		SessionID:   result.SessionID,
		AgentID:     result.SessionID,
		Label:       result.TabName,
		Cwd:         request.Vault.Path,
		Mode:        SpawnModeInteractive,
		ModeSource:  OpenModeSource,
		Launcher:    result.Launcher,
		PaneID:      result.PaneID,
		Role:        request.Role,
		ResumedFrom: request.Resume,
		Location:    "local",
		SpawnedAt:   o.now.Now().Time().UTC().Format(time.RFC3339),
	}); err != nil {
		log.Printf(
			"WARNING: the ledger row for open session %s could not be written: %v",
			result.SessionID,
			err,
		)
	}
}

// openStartCommand is one ready-gated start command and the wait before it.
type openStartCommand struct {
	text string
	wait time.Duration
}

// openStartCommands is the § Manager rows / § Human rows command table.
func openStartCommands(request OpenTabRequest) []openStartCommand {
	switch request.Kind {
	case OpenKindGoal:
		return []openStartCommand{
			{text: goalWorkCommand(request.Subject), wait: openStartWait},
			{text: goalLoopCommand(request.Subject), wait: openGoalLoopWait},
		}
	case OpenKindTopic:
		return []openStartCommand{
			{text: goalLoopCommand(request.Subject), wait: openStartWait},
		}
	default:
		return []openStartCommand{
			{text: taskWorkCommand(request), wait: openStartWait},
		}
	}
}

// taskWorkCommand is the human row's work command, with the first step in front.
func taskWorkCommand(request OpenTabRequest) string {
	command := fmt.Sprintf(`/vault-cli:work-on-task "%s"`, request.Subject)
	if request.FirstStep == "" {
		return command
	}
	return request.FirstStep + "\n\n" + command
}

// goalWorkCommand is the goal manager's first start command.
func goalWorkCommand(subject string) string {
	return fmt.Sprintf(`/vault-cli:work-on-goal "%s"`, subject)
}

// goalLoopCommand arms a goal or topic manager's loop.
func goalLoopCommand(subject string) string {
	return fmt.Sprintf(`/supervisor:goal-loop "%s"`, subject)
}

// sendStartCommands sends the row's commands in order, ready-gated, and stops
// at the first one that never reaches a ready composer. A failure is a Note,
// never an error: the tab is open.
func (o *openTabOpener) sendStartCommands(
	ctx context.Context,
	request OpenTabRequest,
	paneID string,
) string {
	for _, command := range openStartCommands(request) {
		if err := SendWhenReadyWithin(
			ctx, o.tabs, paneID, command.text, o.now, command.wait,
		); err != nil {
			note := fmt.Sprintf("start command %q not sent: %v", command.text, err)
			log.Printf("WARNING: open tab pane %s: %s", paneID, note)
			return note
		}
	}
	return ""
}
