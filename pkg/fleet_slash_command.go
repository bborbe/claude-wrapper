// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/bborbe/errors"
)

// FleetOutcome is the per-session result of a fleet-wide slash delivery. Exactly
// five values.
//
// There is no `reloaded` analogue here on purpose: a plugin reload leaves a
// marker behind, so it can be verified after the fact, but a slash command's
// effect cannot be observed from the daemon. `delivered` therefore means the
// command reached the session — not that its effect took hold.
type FleetOutcome string

const (
	// FleetDelivered is the only success: the command reached the session.
	FleetDelivered FleetOutcome = "delivered"
	// FleetRefused means delivery was attempted but the composer never became
	// ready (a draft or a modal). Nothing was typed.
	FleetRefused FleetOutcome = "refused"
	// FleetBusy means the session is mid-turn or parked on a gate. It is skipped,
	// never interrupted.
	FleetBusy FleetOutcome = "busy"
	// FleetNoPane means a tab whose pane resolved by neither the ledger nor the
	// pid→pane join, a headless child this daemon does not hold, or a shell.
	FleetNoPane FleetOutcome = "no-pane"
	// FleetFailed means delivery returned an error that is none of the above.
	FleetFailed FleetOutcome = "failed"
)

// FleetCommandResult is one session's outcome.
type FleetCommandResult struct {
	SessionID string       `json:"session_id"`
	Outcome   FleetOutcome `json:"outcome"`
	Detail    string       `json:"detail"`
}

// FleetCommandReport is the whole run: the command, one result per session, and
// the same population diff the reload reports.
type FleetCommandReport struct {
	Command string               `json:"command"`
	Results []FleetCommandResult `json:"results"`
	Missing []string             `json:"missing,omitempty"`
	Extra   []string             `json:"extra,omitempty"`
}

// RunFleetCommand delivers one allowlisted slash command to every live session
// (or the `only` subset) and reports one outcome per session. A single session's
// delivery failure never aborts the run.
//
// The allowlist is checked here as well as in runSlashCommand so a command that
// can never be delivered fails once, with the refusal, instead of once per live
// session.
func RunFleetCommand(
	ctx context.Context,
	deps ReloadFleetDeps,
	command string,
	only []string,
) (FleetCommandReport, error) {
	if !allowedSlashCommands.Contains(command) {
		return FleetCommandReport{}, errors.Wrapf(
			ctx,
			ErrSlashCommandNotAllowed,
			"command %q is not allowlisted (allowed: %s)",
			command,
			strings.Join(allowedSlashCommands, ", "),
		)
	}
	sessions, err := liveSessions(ctx, deps, only)
	if err != nil {
		return FleetCommandReport{}, err
	}
	report := FleetCommandReport{
		Command: command,
		Results: make([]FleetCommandResult, 0, len(sessions)),
	}
	for _, session := range sessions {
		// One delivery can block for the composer deadline, so a whole run can take
		// minutes: stop between sessions when the caller has gone away.
		select {
		case <-ctx.Done():
			return report, errors.Wrapf(ctx, ctx.Err(), "fleet command cancelled")
		default:
		}
		report.Results = append(report.Results, runFleetCommandOne(ctx, deps, session, command))
	}
	missing, extra, err := crossCheck(ctx, deps.Registry, deps.Roster)
	if err != nil {
		return report, err
	}
	report.Missing, report.Extra = missing, extra
	return report, nil
}

// runFleetCommandOne delivers to one session, or records why it did not.
func runFleetCommandOne(
	ctx context.Context,
	deps ReloadFleetDeps,
	session fleetSession,
	command string,
) FleetCommandResult {
	result := FleetCommandResult{SessionID: session.Entry.SessionID}
	if outcome, detail, skip := commandSkipOutcome(session); skip {
		result.Outcome, result.Detail = outcome, detail
		return result
	}
	if err := deps.Slash.Run(ctx, session.Entry.SessionID, command); err != nil {
		result.Outcome = classifyFleetError(err)
		result.Detail = err.Error()
		return result
	}
	result.Outcome = FleetDelivered
	result.Detail = "command delivered"
	return result
}

// commandSkipOutcome maps a pre-delivery skip onto the fleet-command vocabulary.
// A status that could not be read is `failed`: nothing was attempted, and no
// weaker value would say so.
func commandSkipOutcome(session fleetSession) (FleetOutcome, string, bool) {
	switch session.Skip {
	case skipBusy:
		return FleetBusy, session.Detail, true
	case skipNoPane:
		return FleetNoPane, session.Detail, true
	case skipUnreadable:
		return FleetFailed, session.Detail, true
	case skipNone:
		return "", "", false
	}
	return "", "", false
}

// classifyFleetError reuses the reload's error→outcome mapping, which is derived
// from the verbatim strings the delivery path returns, and folds its `unverified`
// into `failed`: a reconnect has no marker to wait on, so an unclassifiable error
// is a failure rather than an unobservable success.
func classifyFleetError(err error) FleetOutcome {
	switch classifyReloadError(err) {
	case OutcomeRefused:
		return FleetRefused
	case OutcomeBusy:
		return FleetBusy
	case OutcomeNoPane:
		return FleetNoPane
	default:
		return FleetFailed
	}
}

// FleetSlashCommandReport runs the fleet command over this server's live
// sessions. Both the MCP tool and the REST route call it, so the two surfaces
// cannot drift.
func (s *RosterToolServer) FleetSlashCommandReport(
	ctx context.Context,
	command string,
	only []string,
) (FleetCommandReport, error) {
	return RunFleetCommand(ctx, ReloadFleetDeps{
		Registry: s.Registry,
		Roster:   s.Roster,
		Slash:    NewRosterSlashRunner(s),
		Markers:  NewCacheMarkerReader(""),
	}, command, only)
}

// fleetSlashCommandToolDefinition is the MCP schema for fleet_slash_command.
func fleetSlashCommandToolDefinition() any {
	return toolDef(ToolFleetSlashCommand,
		"Run one allowlisted slash command in EVERY live local session — headless "+
			"and tab — and report one outcome per session. Optional `sessions` "+
			"scopes the run to named ids. Outcomes: `delivered` / `refused` / "+
			"`busy` / `no-pane` / `failed`. Busy and parked sessions are skipped, "+
			"never interrupted; a non-empty composer is refused. `delivered` means "+
			"the command reached the session, not that its effect was observed. "+
			"Same allowlist as run_slash_command: `/compact`, `/reload-plugins`, "+
			"`/mcp reconnect tts`.",
		map[string]any{
			"command": stringProp("the allowlisted command to deliver to every session"),
			"sessions": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "session ids to scope the run to; empty means every live session",
			},
		},
		"command")
}

// fleetSlashCommand is the fleet_slash_command MCP tool.
func (s *RosterToolServer) fleetSlashCommand(
	ctx context.Context,
	args json.RawMessage,
) (string, error) {
	var body struct {
		Command  string   `json:"command"`
		Sessions []string `json:"sessions"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &body); err != nil {
			return "", errors.Wrapf(ctx, err, "decode arguments failed")
		}
	}
	if body.Command == "" {
		return "", errors.Errorf(ctx, "command is required")
	}
	report, err := s.FleetSlashCommandReport(ctx, body.Command, body.Sessions)
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, report)
}
