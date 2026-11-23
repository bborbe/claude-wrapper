// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"
	"strconv"
	"strings"
)

// Open kinds.
const (
	OpenKindTask  = "task"
	OpenKindGoal  = "goal"
	OpenKindTopic = "topic"
)

// Open branches.
const (
	OpenBranchJump    = "jump"
	OpenBranchLive    = "live"
	OpenBranchResume  = "resume"
	OpenBranchCreate  = "create"
	OpenBranchYours   = "yours"
	OpenBranchRefused = "refused"
)

// Open paths.
const (
	OpenPathHeadless = "headless"
	OpenPathTab      = "tab"
	OpenPathNone     = "none"
)

// Where a verdict's role came from.
const (
	OpenRoleSourceOnDisk    = "on-disk"
	OpenRoleSourceDefaulted = "defaulted"
	OpenRoleSourceKind      = "kind"
)

// OpenModeNoteNormalised is the only non-empty mode_note value.
const OpenModeNoteNormalised = "normalised interactive->headless"

// Open refusal reasons.
const (
	OpenReasonMissingRow          = "missing-row"
	OpenReasonRowUnreadable       = "row-unreadable"
	OpenReasonUndecidableRole     = "undecidable-role"
	OpenReasonManagerModeHeadless = "manager-mode-headless"
	OpenReasonTruncatedID         = "truncated-id"
	OpenReasonLivenessUnreadable  = "liveness-unreadable"
	OpenReasonAnswerPath          = "answer-path"
	OpenReasonRoleMapKeyMissing   = "role-map-key-missing"
	OpenReasonHoldWindow          = "hold-window"
	OpenReasonGoalLoopUnsupported = "goal-loop-unsupported"
	OpenReasonTargetUnsupported   = "target-unsupported"
	OpenReasonInFlight            = "in-flight"
	OpenReasonSpawnRefused        = "spawn-refused"
	OpenReasonVaultMismatch       = "vault-mismatch"
)

// OpenWindowKeyHold is the role-map key no open may ever land in.
const OpenWindowKeyHold = "Hold"

// OpenAnswerPathRemedy is the role-aware remedy of role-mode matrix
// § Answer-path remedy for an agent row. It never says "open interactive":
// that is the tab Rule 2 forbids an agent.
const OpenAnswerPathRemedy = "arm this session's manager loop and re-run"

// openRemedies is the remedy every refusal carries, keyed by its reason token.
//
// Kept beside the tokens so a new reason cannot land without one. The
// answer-path entry is the only one that is role-aware, and it never suggests
// opening a tab for an agent (role-mode matrix § Answer-path remedy).
var openRemedies = map[string]string{
	OpenReasonMissingRow:          "check the page title, kind and vault, then re-run",
	OpenReasonRowUnreadable:       "fix the page or its vault, then re-run",
	OpenReasonUndecidableRole:     "set role to agent, human or manager on the task, then re-run",
	OpenReasonManagerModeHeadless: "remove mode: headless from the page — a manager opens as a tab — then re-run",
	OpenReasonTruncatedID:         "write the full 36-character claude_session_id on the page, or clear it, then re-run",
	OpenReasonLivenessUnreadable:  "re-run once the session registry is readable",
	OpenReasonAnswerPath:          OpenAnswerPathRemedy,
	OpenReasonRoleMapKeyMissing:   "add the key with a window_id to the WezTerm role map, then re-run",
	OpenReasonHoldWindow:          "point the key at its own window in the WezTerm role map, then re-run",
	OpenReasonGoalLoopUnsupported: "the open endpoint does not port goal_loop rows; open this row by hand",
	OpenReasonTargetUnsupported:   "the open endpoint opens local rows only; spawn this row on its target with spawn_agent",
	OpenReasonInFlight:            "wait for the other open of this row to finish, then re-run",
	OpenReasonSpawnRefused:        "read the message, clear what refused (a slot, the launcher or the task state), then re-run",
	OpenReasonVaultMismatch:       "open the row through a daemon serving its own vault family, then re-run",
}

// openFlagTruthy is the supervisor's strict YAML-boolean predicate, mirrored
// from pkg/sweepgate/task.go `operatorFlagged` and claude-supervisor
// `commands/open.md` § Gate 3. Nothing wider is accepted: a `1` is not a flag.
var openFlagTruthy = map[string]bool{
	"true": true, "True": true, "TRUE": true,
	"yes": true, "Yes": true, "YES": true,
	"on": true, "On": true, "ON": true, "y": true, "Y": true,
}

// openOpeningActors are the `flag_set_by` values that name the operator, per
// the same `operatorFlagged` predicate and `commands/open.md` OPENING_ACTORS.
var openOpeningActors = map[string]bool{
	"operator": true,
	"legacy":   true,
}

// OpenRow is one row of an open request.
type OpenRow struct {
	Kind      string `json:"kind"`
	Vault     string `json:"vault"`
	Name      string `json:"name"`
	FirstStep string `json:"first_step"`
}

// OpenVerdict is the one answer an open returns per row.
//
// Field set and token values: docs/open-endpoint.md § Verdict. An empty
// string means "not decided for this row" (a row refused at the page read
// has no role yet); window_id is null unless the row opened, or will open,
// a tab.
type OpenVerdict struct {
	Kind       string `json:"kind"`
	Vault      string `json:"vault"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	RoleSource string `json:"role_source"`
	Mode       string `json:"mode"`
	ModeNote   string `json:"mode_note"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	WindowKey  string `json:"window_key"`
	WindowID   *int   `json:"window_id"`
	SessionID  string `json:"session_id"`
	PaneID     string `json:"pane_id"`
	// Bind is the endpoint's own record of the task bind it performed, or null
	// on a row that binds nothing (tab, refused, no task). It is the only
	// evidence that distinguishes the endpoint's write from a spawned child
	// stamping itself through /vault-cli:work-on-task.
	Bind    *OpenBind `json:"bind"`
	Reason  string    `json:"reason"`
	Remedy  string    `json:"remedy"`
	Message string    `json:"message"`
}

// OpenRefusal is one named refusal: its reason token, the detail, the remedy.
type OpenRefusal struct {
	Reason  string
	Message string
	Remedy  string
}

// OpenPage is what the executor read for one row's page.
type OpenPage struct {
	// Found is false when no page of the row's kind exists in the vault.
	Found bool
	// ReadErr is set when a page exists but could not be read.
	ReadErr error
	// Path is the page's absolute path; set when Found.
	Path string
	// Fields are the page's top-level frontmatter scalars (readFrontmatter).
	Fields map[string]string
}

// OpenLiveEntry is one live session-registry entry.
type OpenLiveEntry struct {
	PID       int
	SessionID string
	Name      string
}

// OpenRowFacts is everything DecideOpen reads for one row.
type OpenRowFacts struct {
	Row  OpenRow
	Page OpenPage
	// Live is the registry's live set; LiveErr is set when it could not be read.
	Live    []OpenLiveEntry
	LiveErr error
	// PaneFor returns the WezTerm pane a live pid is attached to, or "" for
	// none. It is consulted only for a human or manager row that matched a live
	// entry; nil means no pane is known for any pid.
	PaneFor func(pid int) string
}

// OpenDecision is DecideOpen's answer: the verdict so far plus what the
// executor needs to finish the open.
type OpenDecision struct {
	Verdict OpenVerdict
	// Resume is the recorded session id a resume continues; empty otherwise.
	Resume string
	// Shipping reports a task's `shipping: true`.
	Shipping bool
	// OperatorNamed reports `flag: true` set by an opening actor.
	OperatorNamed bool
	// WriteRole and WriteMode are the task values to write back with
	// `vault-cli task set`; empty means no write.
	WriteRole string
	WriteMode string
}

// OpenWindow is the WezTerm window a tab row resolved to.
type OpenWindow struct {
	Key  string
	ID   int
	Chip string
}

// refuse turns the verdict into a named refusal.
//
// It leaves Path, Role, Mode and WindowKey as they were, so a refusal decided
// after the path is known still reports it (an agent refused `answer-path`
// logs `path=headless`).
func (v *OpenVerdict) refuse(refusal OpenRefusal) {
	v.Branch = OpenBranchRefused
	v.Reason = refusal.Reason
	v.Message = refusal.Message
	v.Remedy = refusal.Remedy
	if v.Remedy == "" {
		v.Remedy = openRemedies[refusal.Reason]
	}
	v.WindowID = nil
}

// DecideOpen turns one row's facts into its verdict and the hints the
// executor needs.
//
// It is pure: no file reads, no exec, no registry or role-map reads. The first
// match wins, in the order docs/open-endpoint.md § Decision order gives. A
// refusal at any step clears the write-back hints, because a refused row is
// never written (spec DB7).
func DecideOpen(facts OpenRowFacts) OpenDecision {
	decision := OpenDecision{Verdict: OpenVerdict{
		Kind:  facts.Row.Kind,
		Vault: facts.Row.Vault,
		Name:  facts.Row.Name,
		Path:  OpenPathNone,
	}}
	if !decideOpenPage(&decision, facts) {
		return decision
	}
	if !decideOpenRole(&decision, facts) {
		return decision
	}
	if decision.Verdict.Branch == OpenBranchYours {
		return decision
	}
	if !decideOpenMode(&decision, facts) {
		return decision
	}
	if !unportedOpenFields(&decision, facts) {
		return decision
	}
	if !decideOpenSession(&decision, facts) {
		return decision
	}
	decideOpenBranch(&decision, facts)
	return decision
}

// decideOpenPage implements § Decision order item 1.
func decideOpenPage(decision *OpenDecision, facts OpenRowFacts) bool {
	verdict := &decision.Verdict
	kind := verdict.Kind
	if kind != OpenKindTask && kind != OpenKindGoal && kind != OpenKindTopic {
		return refuseOpen(decision, OpenReasonMissingRow,
			fmt.Sprintf("unknown kind %q", kind))
	}
	if !facts.Page.Found && facts.Page.ReadErr == nil {
		return refuseOpen(decision, OpenReasonMissingRow,
			fmt.Sprintf("no %s page named %q in vault %q", kind, verdict.Name, verdict.Vault))
	}
	if facts.Page.ReadErr != nil {
		return refuseOpen(decision, OpenReasonRowUnreadable,
			fmt.Sprintf("read %s page %q failed: %v", kind, verdict.Name, facts.Page.ReadErr))
	}
	return true
}

// decideOpenRole implements § Decision order item 2.
//
// A task `role: manager` is the operator's own session: it is reported as
// `yours` and nothing else is decided, so the caller can return without
// opening anything.
func decideOpenRole(decision *OpenDecision, facts OpenRowFacts) bool {
	verdict := &decision.Verdict
	switch verdict.Kind {
	case OpenKindGoal, OpenKindTopic:
		verdict.Role = RoleManager
		verdict.RoleSource = OpenRoleSourceKind
		return true
	case OpenKindTask:
		return decideOpenTaskRole(decision, facts)
	default:
		return false
	}
}

func decideOpenTaskRole(decision *OpenDecision, facts OpenRowFacts) bool {
	verdict := &decision.Verdict
	role := strings.TrimSpace(facts.Page.Fields["role"])
	switch role {
	case "":
		verdict.Role = RoleAgent
		verdict.RoleSource = OpenRoleSourceDefaulted
		decision.WriteRole = RoleAgent
	case RoleAgent, RoleHuman:
		verdict.Role = role
		verdict.RoleSource = OpenRoleSourceOnDisk
	case RoleManager:
		verdict.Role = RoleManager
		verdict.RoleSource = OpenRoleSourceOnDisk
		verdict.Branch = OpenBranchYours
		verdict.Path = OpenPathNone
	default:
		return refuseOpen(decision, OpenReasonUndecidableRole,
			fmt.Sprintf("task role %q is not agent, human or manager", role))
	}
	return true
}

// decideOpenMode implements § Decision order item 3, role-mode matrix Rule 2.
func decideOpenMode(decision *OpenDecision, facts OpenRowFacts) bool {
	verdict := &decision.Verdict
	stored := strings.TrimSpace(facts.Page.Fields["mode"])
	switch verdict.Role {
	case RoleAgent:
		decideAgentMode(decision, stored)
		return true
	case RoleHuman:
		verdict.Mode = SpawnModeInteractive
		if stored == "" {
			decision.WriteMode = SpawnModeInteractive
		}
		return true
	case RoleManager:
		return decideManagerMode(decision, stored)
	default:
		return false
	}
}

// decideAgentMode forces an agent headless and records the write-back the
// executor needs. A stored `interactive` is normalised and reported; any other
// unrecognised value is overwritten without the note, which names only the
// interactive case.
func decideAgentMode(decision *OpenDecision, stored string) {
	verdict := &decision.Verdict
	verdict.Mode = SpawnModeHeadless
	switch stored {
	case SpawnModeHeadless:
		// Already correct: no write.
	case SpawnModeInteractive:
		verdict.ModeNote = OpenModeNoteNormalised
		decision.WriteMode = SpawnModeHeadless
	case "":
		decision.WriteMode = SpawnModeHeadless
	default:
		decision.WriteMode = SpawnModeHeadless
	}
}

// decideManagerMode forces a goal or topic manager interactive and refuses a
// page that declares itself headless. Goal and topic rows never write back.
func decideManagerMode(decision *OpenDecision, stored string) bool {
	verdict := &decision.Verdict
	verdict.Mode = SpawnModeInteractive
	if verdict.Kind != OpenKindGoal && verdict.Kind != OpenKindTopic {
		return true
	}
	if stored == SpawnModeHeadless {
		return refuseOpen(decision, OpenReasonManagerModeHeadless,
			fmt.Sprintf(
				"%s page %q declares mode: headless, but a manager opens as a tab (role-mode matrix Rule 2)",
				verdict.Kind, verdict.Name,
			))
	}
	return true
}

// unportedOpenFields implements § Decision order item 4.
//
// OperatorNamed is read for every kind: a goal or topic manager's cap check
// reads it too. Shipping is a task-only fact.
func unportedOpenFields(decision *OpenDecision, facts OpenRowFacts) bool {
	verdict := &decision.Verdict
	decision.OperatorNamed = operatorNamed(facts.Page.Fields)
	if verdict.Kind != OpenKindTask {
		return true
	}
	if strings.TrimSpace(facts.Page.Fields["goal_loop"]) == "true" {
		return refuseOpen(decision, OpenReasonGoalLoopUnsupported,
			fmt.Sprintf("task %q carries goal_loop: true, which the open endpoint does not port",
				verdict.Name))
	}
	target := strings.TrimSpace(facts.Page.Fields["target"])
	if target != "" && target != "local" {
		return refuseOpen(decision, OpenReasonTargetUnsupported,
			fmt.Sprintf("task %q targets %q; the open endpoint opens local rows only",
				verdict.Name, target))
	}
	decision.Shipping = strings.TrimSpace(facts.Page.Fields["shipping"]) == "true"
	return true
}

// operatorNamed mirrors pkg/sweepgate/task.go `operatorFlagged`: the strict
// YAML-boolean spellings and the two opening actors, nothing wider.
func operatorNamed(fields map[string]string) bool {
	setBy := strings.Trim(strings.TrimSpace(fields["flag_set_by"]), `'"`)
	return openFlagTruthy[strings.TrimSpace(fields["flag"])] && openOpeningActors[setBy]
}

// decideOpenSession implements § Decision order item 5.
func decideOpenSession(decision *OpenDecision, facts OpenRowFacts) bool {
	recorded := strings.TrimSpace(facts.Page.Fields["claude_session_id"])
	if recorded != "" && !uuidPattern.MatchString(recorded) {
		return refuseOpen(decision, OpenReasonTruncatedID,
			fmt.Sprintf("recorded claude_session_id %q is not a full 36-character UUID", recorded))
	}
	if facts.LiveErr != nil {
		return refuseOpen(decision, OpenReasonLivenessUnreadable,
			fmt.Sprintf("session registry unreadable: %v", facts.LiveErr))
	}
	return true
}

// decideOpenBranch implements § Decision order item 6, then chooses the path.
func decideOpenBranch(decision *OpenDecision, facts OpenRowFacts) {
	verdict := &decision.Verdict
	recorded := strings.TrimSpace(facts.Page.Fields["claude_session_id"])
	switch {
	case recorded != "":
		decideRecordedBranch(decision, facts, recorded)
	case verdict.Kind == OpenKindGoal || verdict.Kind == OpenKindTopic:
		decideManagerBranch(decision, facts)
	default:
		verdict.Branch = OpenBranchCreate
	}
	verdict.Path = openPathFor(verdict.Role, verdict.Branch)
}

// decideRecordedBranch picks jump, live or resume from the recorded id.
//
// PaneFor is consulted only for a human or manager row: an agent never gets a
// tab, so its pane is not a fact this decision has any use for.
func decideRecordedBranch(decision *OpenDecision, facts OpenRowFacts, recorded string) {
	verdict := &decision.Verdict
	entry, found := liveEntryBySession(facts.Live, recorded)
	if !found {
		verdict.Branch = OpenBranchResume
		verdict.SessionID = recorded
		decision.Resume = recorded
		return
	}
	verdict.SessionID = recorded
	pane := ""
	if verdict.Role == RoleHuman || verdict.Role == RoleManager {
		pane = paneForPID(facts.PaneFor, entry.PID)
	}
	if pane != "" {
		verdict.Branch = OpenBranchJump
		verdict.PaneID = pane
		return
	}
	verdict.Branch = OpenBranchLive
}

// decideManagerBranch handles a goal or topic with no recorded id: a live
// entry named `<name> Manager` is the manager already running for this row.
func decideManagerBranch(decision *OpenDecision, facts OpenRowFacts) {
	verdict := &decision.Verdict
	entry, found := liveEntryByName(facts.Live, verdict.Name+" Manager")
	if !found {
		verdict.Branch = OpenBranchCreate
		return
	}
	verdict.SessionID = entry.SessionID
	if pane := paneForPID(facts.PaneFor, entry.PID); pane != "" {
		verdict.Branch = OpenBranchJump
		verdict.PaneID = pane
		return
	}
	verdict.Branch = OpenBranchLive
}

// liveEntryBySession returns the live entry for a recorded session id.
func liveEntryBySession(live []OpenLiveEntry, sessionID string) (OpenLiveEntry, bool) {
	for _, entry := range live {
		if entry.SessionID == sessionID {
			return entry, true
		}
	}
	return OpenLiveEntry{}, false
}

// liveEntryByName returns the live entry whose tab title matches, ignoring
// case and surrounding whitespace.
func liveEntryByName(live []OpenLiveEntry, name string) (OpenLiveEntry, bool) {
	for _, entry := range live {
		if strings.EqualFold(strings.TrimSpace(entry.Name), name) {
			return entry, true
		}
	}
	return OpenLiveEntry{}, false
}

// paneForPID asks the caller's pane lookup, treating a nil lookup as no pane.
func paneForPID(paneFor func(int) string, pid int) string {
	if paneFor == nil {
		return ""
	}
	return paneFor(pid)
}

// openPathFor is the ONLY place the path is chosen. Routing follows the
// resolved role, never the stored mode (role-mode matrix Rule 2).
func openPathFor(role, branch string) string {
	switch branch {
	case OpenBranchResume, OpenBranchCreate:
		switch role {
		case RoleAgent:
			return OpenPathHeadless
		case RoleHuman, RoleManager:
			return OpenPathTab
		}
	}
	return OpenPathNone
}

// refuseOpen records a refusal and clears the write-back hints.
func refuseOpen(decision *OpenDecision, reason, message string) bool {
	decision.Verdict.refuse(OpenRefusal{Reason: reason, Message: message})
	decision.WriteRole = ""
	decision.WriteMode = ""
	return false
}

// ResolveOpenWindow resolves the window a tab row opens in, per the window half
// of § Decision order item 7.
//
// It is pure. A missing or broken map, a missing key or window id refuses and
// names the key; a window equal to Hold's refuses. It never substitutes another
// key or window (spec Non-goals: no fallback window).
func ResolveOpenWindow(
	role string,
	vault string,
	entries map[string]RoleMapEntry,
	readErr error,
) (OpenWindow, *OpenRefusal) {
	key, ok := rolePurposes[role]
	if !ok {
		return OpenWindow{}, newOpenRefusal(OpenReasonSpawnRefused, agentTabRefusal)
	}
	if readErr != nil {
		return OpenWindow{}, newOpenRefusal(
			OpenReasonRoleMapKeyMissing,
			fmt.Sprintf("role map unreadable, so key %q cannot be resolved: %v", key, readErr),
		)
	}
	entry, ok := entries[key]
	if !ok {
		return OpenWindow{}, newOpenRefusal(
			OpenReasonRoleMapKeyMissing,
			fmt.Sprintf("role map has no %q key", key),
		)
	}
	if entry.WindowID == nil {
		return OpenWindow{}, newOpenRefusal(
			OpenReasonRoleMapKeyMissing,
			fmt.Sprintf("role map key %q has no window_id", key),
		)
	}
	if hold, ok := entries[OpenWindowKeyHold]; ok &&
		hold.WindowID != nil && *hold.WindowID == *entry.WindowID {
		return OpenWindow{}, newOpenRefusal(
			OpenReasonHoldWindow,
			fmt.Sprintf(
				"role map key %q names window %d, which is Hold.window_id — nothing opens in Hold",
				key, *entry.WindowID,
			),
		)
	}
	return OpenWindow{Key: key, ID: *entry.WindowID, Chip: openWindowChip(role, key, entry, vault)}, nil
}

// openWindowChip returns the `/color` argument for a resolved tab.
//
// A manager is always orange (spec AC7, § Manager rows); the Managers entry's
// own chip is not read. A human takes the vault family's chip through
// ChipForRole, falling back to the Direct entry's chip.
func openWindowChip(role, key string, entry RoleMapEntry, vault string) string {
	if role != RoleHuman {
		return "orange"
	}
	entryChip := ""
	if entry.Chip != nil {
		entryChip = *entry.Chip
	}
	return ChipForRole(&RoleResolution{Role: RoleHuman, Purpose: key, Chip: entryChip}, vault)
}

// newOpenRefusal builds a refusal carrying the reason's remedy.
func newOpenRefusal(reason, message string) *OpenRefusal {
	return &OpenRefusal{Reason: reason, Message: message, Remedy: openRemedies[reason]}
}

// FormatOpenLogLine renders the one log line § Verdict prescribes.
//
// Empty Role, Mode, Branch or Path render as `-`; a nil WindowID renders
// `none`; an empty Reason renders `-`. One line, no trailing newline.
func FormatOpenLogLine(verdict OpenVerdict) string {
	window := "none"
	if verdict.WindowID != nil {
		window = strconv.Itoa(*verdict.WindowID)
	}
	return fmt.Sprintf(
		"open: row=%s/%s/%s role=%s mode=%s branch=%s path=%s window=%s reason=%s",
		verdict.Vault,
		verdict.Kind,
		verdict.Name,
		dashIfEmpty(verdict.Role),
		dashIfEmpty(verdict.Mode),
		dashIfEmpty(verdict.Branch),
		dashIfEmpty(verdict.Path),
		window,
		dashIfEmpty(verdict.Reason),
	)
}

// dashIfEmpty renders an undecided field as the log line's placeholder.
func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
