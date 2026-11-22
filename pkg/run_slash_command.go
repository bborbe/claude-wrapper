// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/bborbe/errors"
)

// allowedSlashCommands is every command run_slash_command will deliver.
//
// Fixed in code, exact match, no arguments: the tool writes a real command into
// a live worker, so what it can run has to be reviewable as a diff rather than
// supplied at call time — the same property the macro table rests on.
//
// `/mcp reconnect tts` is here because `tts` is ONE shared HTTP MCP server
// (`http://127.0.0.1:12000/mcp`): its LaunchAgent restarts it (`KeepAlive`,
// `ThrottleInterval 30`), and every restart orphans every live session's
// connection at once. Reconnect is the only remedy, and it is per session — so
// without this entry the fleet remedy costs one keystroke per pane. A second
// server is one more exact-match entry, added when it is needed.
var allowedSlashCommands = StringList{"/compact", "/reload-plugins", "/mcp reconnect tts"}

// runSlashCommandToolDefinition is the MCP schema for run_slash_command.
func runSlashCommandToolDefinition() any {
	return toolDef(ToolRunSlashCommand,
		"Run one allowlisted slash command in a local worker, as a real command "+
			"rather than a message. A HEADLESS worker receives it as a user turn on "+
			"its stdin; a TAB worker has it typed into its pane. Allowlist: "+
			"`/compact`, `/reload-plugins` — anything else is refused with `not "+
			"allowlisted`. A2A and cluster workers are refused: this daemon holds "+
			"neither their stdin nor a pane. Use it where `send_agent_message` would "+
			"only hand back a `SendMessage` name, which delivers a peer message that "+
			"runs no command.",
		map[string]any{
			"agent_id": stringProp("id of the local worker to run the command in"),
			"command":  stringProp("exactly `/compact` or `/reload-plugins`"),
		},
		"agent_id", "command")
}

// runSlashCommand delivers one allowlisted slash command to a local worker.
//
// A headless worker is addressed through the supervisor, which holds its stdin:
// a slash command written as a user turn runs as the command (measured
// 2026-11-17 — `/reload-plugins` answers `Reloaded: …` from a `<synthetic>`
// turn, `/compact` emits `compact_boundary` with `trigger: manual`). That is the
// path send_agent_message lacks: for a headless worker it hands back a
// `SendMessage` name, and a peer message is not a user turn.
func (s *RosterToolServer) runSlashCommand(
	ctx context.Context,
	args json.RawMessage,
) (string, error) {
	var body struct {
		AgentID string `json:"agent_id"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" {
		return "", errors.Errorf(ctx, "agent_id is required")
	}
	if !allowedSlashCommands.Contains(body.Command) {
		return "", errors.Wrapf(
			ctx,
			ErrSlashCommandNotAllowed,
			"command %q is not allowlisted (allowed: %s)",
			body.Command,
			strings.Join(allowedSlashCommands, ", "),
		)
	}
	if handled, response, err := s.runSlashCommandOnStdin(ctx, body.AgentID, body.Command); handled {
		return response, err
	}
	if handled, response, err := s.runSlashCommandInPane(ctx, body.AgentID, body.Command); handled {
		return response, err
	}
	return "", errors.Errorf(
		ctx,
		"agent %s is not a local tab or headless worker of this daemon — run_slash_command "+
			"reaches only those; an A2A or cluster worker has no stdin or pane here",
		body.AgentID,
	)
}

// runSlashCommandOnStdin writes the command as a user turn to a headless child
// the supervisor holds. It reports handled=false for an id the supervisor does
// not hold, so the pane arm can try; any other read failure is surfaced rather
// than falling through, because a pane send would then act on a guess.
func (s *RosterToolServer) runSlashCommandOnStdin(
	ctx context.Context,
	agentID string,
	command string,
) (bool, string, error) {
	if s.Supervisor == nil {
		return false, "", nil
	}
	if _, err := s.Supervisor.Get(ctx, agentID); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return false, "", nil
		}
		return true, "", errors.Wrapf(ctx, err, "look up worker %s failed", agentID)
	}
	if s.Locks == nil {
		return true, "", errors.Errorf(ctx, "run_slash_command on a headless worker is not wired")
	}
	// TryAcquire, the same fail-fast set_agent_permission_mode uses: a running
	// sequence counts the child's turns, and a turn written into the middle of
	// it would be read as one of its steps.
	release, ok := s.Locks.TryAcquire(agentID)
	if !ok {
		return true, "", errors.Errorf(ctx, "session %q is running a sequence", agentID)
	}
	defer release()
	if err := s.Supervisor.Send(ctx, agentID, command); err != nil {
		return true, "", errors.Wrapf(
			ctx, err, "send %s to headless worker %s failed", command, agentID,
		)
	}
	response, err := marshalJSON(ctx, map[string]any{
		"agent_id": agentID,
		"command":  command,
		"sent":     true,
		"channel":  "stdin",
	})
	return true, response, err
}

// runSlashCommandInPane types the command into a local tab worker's pane.
//
// A local row with no pane is answered here rather than passed on: it is this
// daemon's own spawn, so "nothing to type into" is the true answer, and the
// generic refusal would misname it as a remote worker.
func (s *RosterToolServer) runSlashCommandInPane(
	ctx context.Context,
	agentID string,
	command string,
) (bool, string, error) {
	if s.Tabs == nil {
		return false, "", nil
	}
	paneID, mode, sessionID, ok := s.resolvePane(ctx, agentID)
	if !ok {
		return false, "", nil
	}
	if paneID == "" {
		return true, "", errors.Errorf(
			ctx,
			"agent %s is a %s worker with no pane and no live child in this daemon — "+
				"there is nothing to run %s in",
			agentID,
			mode,
			command,
		)
	}
	if err := SendWhenReady(ctx, s.Tabs, paneID, command, s.currentDateTime()); err != nil {
		return true, "", err
	}
	response, err := marshalJSON(ctx, map[string]any{
		"agent_id":   agentID,
		"command":    command,
		"sent":       true,
		"channel":    "pane",
		"pane_id":    paneID,
		"session_id": sessionID,
	})
	return true, response, err
}

// resolvePane resolves agentID to a pane: the ledger row first, then the
// pid→pane join for a tab absent from the ledger. ok=false means this daemon
// does not own the agent at all (an A2A or cluster worker), which is the
// caller's cue to refuse it by name rather than answer a pane send.
func (s *RosterToolServer) resolvePane(
	ctx context.Context,
	agentID string,
) (paneID string, mode string, sessionID string, ok bool) {
	if row, found := s.localLedgerRow(ctx, agentID); found {
		return row.PaneID, row.Mode, row.SessionID, true
	}
	pid, found := s.pidForSession(ctx, agentID)
	if !found {
		return "", "", "", false
	}
	// A live pid with no ledger row is a tab this daemon did not spawn. Join it
	// to its pane; if the join misses, the empty pane is the honest answer and
	// runSlashCommandInPane reports "no pane and no live child".
	if s.PaneJoiner == nil {
		return "", "interactive", agentID, true
	}
	panes, err := s.PaneJoiner.PanesForPIDs(ctx, []int{pid})
	if err != nil {
		return "", "interactive", agentID, true
	}
	if pane, ok := panes[pid]; ok && pane != "" {
		return pane, "interactive", agentID, true
	}
	return "", "interactive", agentID, true
}

// pidForSession returns the live pid registered for a session id, if any.
func (s *RosterToolServer) pidForSession(
	ctx context.Context,
	sessionID string,
) (int, bool) {
	if s.Registry == nil {
		return 0, false
	}
	entries, err := s.Registry.Live(ctx)
	if err != nil {
		return 0, false
	}
	for _, entry := range entries {
		if entry.SessionID == sessionID {
			return entry.PID, true
		}
	}
	return 0, false
}

// localLedgerRow resolves an agent id to the ledger row of a worker this daemon
// spawned locally: any row carrying a pane, or a pane-less `interactive` /
// `headless` row.
//
// The agent id is matched against the roster's session id and against the row's
// own session id, so both the roster's agent id and a bare session id resolve.
// `cluster` and `a2a` rows carry no pane either but belong to a remote
// controller, so they never resolve here — claiming them would answer an A2A
// worker as unreachable before the A2A arm ever ran.
func (s *RosterToolServer) localLedgerRow(
	ctx context.Context,
	agentID string,
) (LedgerRow, bool) {
	if s.Roster == nil || s.Ledger == nil {
		return LedgerRow{}, false
	}
	sessionID := ""
	if rows, err := s.Roster.List(ctx); err != nil {
		// A roster that cannot be read must not read as "not a tab worker": the
		// caller would then be told the agent is unknown, which is a different
		// and false fact. Degrade loudly, as the roster itself does.
		log.Printf(
			"WARNING: the roster could not be read (%v) — resolving agent %q against the ledger only",
			err,
			agentID,
		)
	} else {
		for _, row := range rows {
			if row.AgentID == agentID {
				sessionID = row.SessionID
				break
			}
		}
	}
	ledgerRows, err := s.Ledger.Rows(ctx)
	if err != nil {
		return LedgerRow{}, false
	}
	for _, ledgerRow := range ledgerRows {
		if ledgerRow.SessionID != sessionID && ledgerRow.SessionID != agentID {
			continue
		}
		if ledgerRow.PaneID == "" &&
			ledgerRow.Mode != "interactive" && ledgerRow.Mode != "headless" {
			continue
		}
		return ledgerRow, true
	}
	return LedgerRow{}, false
}
