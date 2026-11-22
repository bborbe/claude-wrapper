// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// ReloadOutcome is the per-session result of a fleet reload. Exactly five
// values, each with a single source.
type ReloadOutcome string

const (
	// OutcomeReloaded is the only success: delivery succeeded and the session's
	// newest .in_use marker moved.
	OutcomeReloaded ReloadOutcome = "reloaded"
	// OutcomeRefused means delivery was attempted but the composer never became
	// ready (a draft or a modal). Nothing was typed.
	OutcomeRefused ReloadOutcome = "refused"
	// OutcomeBusy means the session is mid-turn or parked on a gate, or its lock
	// is held. It is skipped, never interrupted.
	OutcomeBusy ReloadOutcome = "busy"
	// OutcomeNoPane means a tab whose pane resolved by neither the ledger nor the
	// pid→pane join, a headless child this daemon does not hold, or a shell.
	OutcomeNoPane ReloadOutcome = "no-pane"
	// OutcomeUnverified means delivery was attempted but the marker did not move
	// within the settle timeout, or could not be read.
	OutcomeUnverified ReloadOutcome = "unverified"
)

// ReloadResult is one session's outcome.
type ReloadResult struct {
	SessionID string        `json:"session_id"`
	Outcome   ReloadOutcome `json:"outcome"`
	Detail    string        `json:"detail"`
}

// ReloadReport is the whole run: per-session results plus the SC3 population
// diff between the primary and cross-check enumerations.
type ReloadReport struct {
	Results []ReloadResult `json:"results"`
	Missing []string       `json:"missing,omitempty"`
	Extra   []string       `json:"extra,omitempty"`
}

// SlashRunner delivers one allowlisted slash command to one session and returns
// the delivery error, if any. It is the seam the fleet reload delegates through
// so both transports reuse run_slash_command rather than re-implementing them.
type SlashRunner interface {
	Run(ctx context.Context, agentID string, command string) error
}

// MarkerReader reads the newest .in_use/<pid> marker across the plugin cache.
// ok is false when no marker exists for the pid.
type MarkerReader interface {
	Newest(pid int) (mtime time.Time, ok bool)
}

// ReloadFleetDeps is everything a fleet reload needs. Every field is required.
type ReloadFleetDeps struct {
	Registry      SessionRegistry
	Roster        Roster
	Slash         SlashRunner
	Markers       MarkerReader
	SettleTimeout time.Duration
	PollInterval  time.Duration
}

// DefaultReloadSettleTimeout is how long a delivery waits for the marker to move
// before the outcome is unverified. The reload is asynchronous; a marker read
// immediately after a stdin send can be stale.
const DefaultReloadSettleTimeout = 10 * time.Second

// DefaultReloadPollInterval is how often the marker is re-read during the settle.
const DefaultReloadPollInterval = time.Second

// ReloadFleet reloads every live session (or the `only` subset), one outcome per
// session, and returns the SC3 population diff. A single session's delivery
// failure never aborts the run.
func ReloadFleet(
	ctx context.Context,
	deps ReloadFleetDeps,
	only []string,
) (ReloadReport, error) {
	if deps.SettleTimeout <= 0 {
		deps.SettleTimeout = DefaultReloadSettleTimeout
	}
	if deps.PollInterval <= 0 {
		deps.PollInterval = DefaultReloadPollInterval
	}
	sessions, err := liveSessions(ctx, deps, only)
	if err != nil {
		return ReloadReport{}, err
	}
	report := ReloadReport{Results: make([]ReloadResult, 0, len(sessions))}
	for _, session := range sessions {
		report.Results = append(report.Results, reloadOne(ctx, deps, session))
	}
	missing, extra, err := crossCheck(ctx, deps.Registry, deps.Roster)
	if err != nil {
		return report, err
	}
	report.Missing, report.Extra = missing, extra
	return report, nil
}

// reloadOne decides and executes one session's outcome.
func reloadOne(
	ctx context.Context,
	deps ReloadFleetDeps,
	session fleetSession,
) ReloadResult {
	result := ReloadResult{SessionID: session.Entry.SessionID}
	if outcome, detail, skip := reloadSkipOutcome(session); skip {
		result.Outcome, result.Detail = outcome, detail
		return result
	}
	entry := session.Entry

	before, hadMarker := deps.Markers.Newest(entry.PID)
	if err := deps.Slash.Run(ctx, entry.SessionID, "/reload-plugins"); err != nil {
		result.Outcome = classifyReloadError(err)
		result.Detail = err.Error()
		return result
	}
	if !hadMarker {
		result.Outcome = OutcomeUnverified
		result.Detail = "no .in_use marker before delivery"
		return result
	}
	if waitMarkerMoved(ctx, deps, entry.PID, before) {
		result.Outcome = OutcomeReloaded
		result.Detail = "marker moved"
		return result
	}
	result.Outcome = OutcomeUnverified
	result.Detail = "marker did not move within the settle timeout"
	return result
}

// reloadSkipOutcome maps a pre-delivery skip onto the reload vocabulary. A
// status that could not be read is `unverified`: nothing was attempted, and the
// reload's own outcome set has no closer value for it.
func reloadSkipOutcome(session fleetSession) (ReloadOutcome, string, bool) {
	switch session.Skip {
	case skipBusy:
		return OutcomeBusy, session.Detail, true
	case skipNoPane:
		return OutcomeNoPane, session.Detail, true
	case skipUnreadable:
		return OutcomeUnverified, session.Detail, true
	case skipNone:
		return "", "", false
	}
	return "", "", false
}

// waitMarkerMoved polls until the pid's newest marker is newer than before, or
// the settle timeout elapses.
func waitMarkerMoved(
	ctx context.Context,
	deps ReloadFleetDeps,
	pid int,
	before time.Time,
) bool {
	deadline := time.Now().Add(deps.SettleTimeout)
	for {
		if mtime, ok := deps.Markers.Newest(pid); ok && mtime.After(before) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(deps.PollInterval):
		}
	}
}

// classifyReloadError maps a runSlashCommand/SendWhenReady error onto an outcome.
// The patterns are the verbatim error strings the delivery path returns.
func classifyReloadError(err error) ReloadOutcome {
	message := err.Error()
	switch {
	case strings.Contains(message, "is running a sequence"):
		return OutcomeBusy
	case strings.Contains(message, "showed no empty composer"):
		return OutcomeRefused
	case strings.Contains(message, "is not a local tab or headless worker"),
		strings.Contains(message, "no pane and no live child"),
		errors.Is(err, ErrSessionNotFound):
		return OutcomeNoPane
	default:
		return OutcomeUnverified
	}
}

// crossCheck diffs the primary enumeration (SessionRegistry.Live) against the
// cross-check (roster.List). missing = live in the registry, absent from the
// roster; extra = in the roster, absent from the registry.
func crossCheck(
	ctx context.Context,
	registry SessionRegistry,
	roster Roster,
) (missing []string, extra []string, err error) {
	live, err := registry.Live(ctx)
	if err != nil {
		return nil, nil, errors.Wrapf(ctx, err, "cross-check: list live failed")
	}
	rows, err := roster.List(ctx)
	if err != nil {
		return nil, nil, errors.Wrapf(ctx, err, "cross-check: roster list failed")
	}
	liveIDs := make(map[string]struct{}, len(live))
	for _, entry := range live {
		liveIDs[entry.SessionID] = struct{}{}
	}
	rosterIDs := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		rosterIDs[row.SessionID] = struct{}{}
	}
	for id := range liveIDs {
		if _, ok := rosterIDs[id]; !ok {
			missing = append(missing, id)
		}
	}
	for id := range rosterIDs {
		if _, ok := liveIDs[id]; !ok {
			extra = append(extra, id)
		}
	}
	return missing, extra, nil
}

// toSet builds a lookup set from a slice, dropping empties.
func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

// CacheMarkerReader reads .in_use/<pid> markers from a plugin cache directory.
type CacheMarkerReader struct {
	CacheDir string
}

// NewCacheMarkerReader returns a reader over ~/.claude/plugins/cache when dir is
// empty.
func NewCacheMarkerReader(dir string) *CacheMarkerReader {
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".claude", "plugins", "cache")
		}
	}
	return &CacheMarkerReader{CacheDir: dir}
}

// Newest returns the newest .in_use/<pid> marker's mtime across every
// marketplace/plugin/version in the cache — the census's newest_marker() rule
// (newest by mtime alone).
func (r *CacheMarkerReader) Newest(pid int) (time.Time, bool) {
	if r == nil || r.CacheDir == "" {
		return time.Time{}, false
	}
	pattern := filepath.Join(r.CacheDir, "*", "*", "*", ".in_use", strconv.Itoa(pid))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	found := false
	for _, path := range matches {
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		if !found || info.ModTime().After(newest) {
			newest = info.ModTime()
			found = true
		}
	}
	return newest, found
}

// rosterSlashRunner adapts (*RosterToolServer).runSlashCommand to SlashRunner.
// It lives here so the fleet reload delegates through the one delivery path.
type rosterSlashRunner struct {
	Server *RosterToolServer
}

// NewRosterSlashRunner returns a SlashRunner over the given roster server.
func NewRosterSlashRunner(server *RosterToolServer) SlashRunner {
	return &rosterSlashRunner{Server: server}
}

// Run marshals the tool arguments and calls runSlashCommand, discarding the JSON
// reply on success.
func (r *rosterSlashRunner) Run(ctx context.Context, agentID string, command string) error {
	args, err := json.Marshal(map[string]string{"agent_id": agentID, "command": command})
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal run_slash_command args failed")
	}
	_, err = r.Server.runSlashCommand(ctx, args)
	return err
}

// ReloadFleetReport runs the fleet reload over this server's live sessions. Both
// the MCP tool and the REST route call it, so the two surfaces cannot drift.
func (s *RosterToolServer) ReloadFleetReport(
	ctx context.Context,
	only []string,
) (ReloadReport, error) {
	return ReloadFleet(ctx, ReloadFleetDeps{
		Registry: s.Registry,
		Roster:   s.Roster,
		Slash:    NewRosterSlashRunner(s),
		Markers:  NewCacheMarkerReader(""),
	}, only)
}

// reloadFleet is the reload_fleet MCP tool. Optional `sessions` scopes the run.
func (s *RosterToolServer) reloadFleet(ctx context.Context, args json.RawMessage) (string, error) {
	var body struct {
		Sessions []string `json:"sessions"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &body); err != nil {
			return "", errors.Wrapf(ctx, err, "decode arguments failed")
		}
	}
	report, err := s.ReloadFleetReport(ctx, body.Sessions)
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, report)
}

// reloadFleetToolDefinition is the MCP schema for reload_fleet.
func reloadFleetToolDefinition() any {
	return toolDef(ToolReloadFleet,
		"Reload plugins in every live local session (headless and tab) and report one "+
			"outcome per session. Optional `sessions` scopes the run to named ids. Outcomes: "+
			"`reloaded` / `refused` / `busy` / `no-pane` / `unverified`. Busy and parked "+
			"sessions are skipped, never interrupted; a non-empty composer is refused.",
		map[string]any{
			"sessions": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "session ids to scope the run to; empty means every live session",
			},
		})
}
