// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	libtime "github.com/bborbe/time"
)

const (
	// DefaultFleetPusherInterval is how often the wake tick runs. It matches the
	// retired launchd job's StartInterval (900 s).
	DefaultFleetPusherInterval = 15 * time.Minute

	// FleetManagerName is the tab title the fleet manager registers under. It is
	// refused by name: waking it starts a model round nobody scheduled.
	FleetManagerName = "Fleet Manager"

	// PusherNamePrefix is the tab-title prefix of a pusher session. Another pusher
	// is idle between its own rounds by design, and two pushers waking each other
	// is a loop.
	PusherNamePrefix = "Fleet Pusher"

	// FleetPusherMessage is the text typed into an idle session. It is deliberately
	// narrow: a wake is not an authorisation, and the line says so.
	FleetPusherMessage = "Fleet wake: you are idle at the prompt with open work. Take the next " +
		"unchecked subtask on your anchored task. Nothing was authorised on your behalf and " +
		"this is not a green light past any gate."
)

// The skip classes and outcomes this tick emits, named so a spec asserts on the
// same string the operator reads. The six skip classes are `pane-wake.py`'s.
const (
	OutcomeSkipSelf         = "skip:self"
	OutcomeSkipFleetManager = "skip:fleet-manager"
	OutcomeSkipPusher       = "skip:pusher"
	OutcomeSkipNotLive      = "skip:not-live"
	OutcomeSkipNoPane       = "skip:no-pane"
	OutcomeSkipOpenCard     = "skip:open-card"
	OutcomeDryRun           = "dry-run"
	OutcomeDelivered        = "delivered"
)

// FleetPusherRow is one session's outcome in one tick.
type FleetPusherRow struct {
	SessionID string
	PID       int
	Pane      string
	Outcome   string
}

// Line renders the per-session log line.
func (r FleetPusherRow) Line() string {
	return fmt.Sprintf(
		"job=fleet-pusher action=deliver session=%s outcome=%s", r.SessionID, r.Outcome,
	)
}

// FleetPusherResult is one tick's whole outcome.
type FleetPusherResult struct {
	Sessions  int
	Delivered int
	Skipped   int
	Rows      []FleetPusherRow
	// Err is non-empty when a fleet-wide read failed. The tick line is still
	// emitted, carrying the error, because a silent cycle is indistinguishable
	// from a dead one.
	Err string
}

// Line renders the one summary line a tick writes.
//
// It is emitted UNCONDITIONALLY, zeros included: an idle fleet must still prove
// the tick ran, which is exactly what the retired shell script failed to do — it
// printed the counter line only when it was non-empty, so a healthy idle tick and
// a dead tick looked identical.
func (r FleetPusherResult) Line() string {
	line := fmt.Sprintf(
		"job=fleet-pusher action=tick sessions=%d delivered=%d skipped=%d",
		r.Sessions, r.Delivered, r.Skipped,
	)
	if r.Err != "" {
		line += " error=" + r.Err
	}
	return line
}

// FleetPusher wakes idle tab sessions, in-process.
//
// It replaces `fleet-pusher-tick.sh` + `pane-wake.py` + `pane-wake-guard.py` +
// `plugin-version-census.py` + `session-liveness.py`. The live-session read is the
// wrapper's own registry, the pid → pane join is PaneJoiner, the guard is
// PaneGuardVerdict, and the send half is the existing SendWhenReady.
type FleetPusher struct {
	Registry SessionRegistry
	Joiner   PaneJoiner
	Tabs     TabClient
	Cards    OpenCardReader
	Subject  SubjectStateReader
	Now      libtime.CurrentDateTimeGetter
	Interval time.Duration
	// HeartbeatPath is the file the watchdog reads. Empty disables the write.
	HeartbeatPath string
	// Sessions scopes the tick to explicit session ids. Empty means every live
	// session.
	Sessions []string
	// Send opts in to typing. The default is a dry run, so the tick can be
	// exercised against the live fleet without waking anyone.
	Send bool
	// Message defaults to FleetPusherMessage.
	Message string
	// SelfSession and SelfPane identify the process's own pane, so it never wakes
	// itself.
	SelfSession string
	SelfPane    string
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
}

// Run ticks until the context ends, the first tick immediately.
//
// The first tick fires at once rather than after one interval: a daemon that
// restarts after a deploy should wake what went idle during the restart, and
// waiting a full interval is a liveness gap the restart itself created.
func (p *FleetPusher) Run(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultFleetPusherInterval
	}
	p.tickAndLog(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.tickAndLog(ctx)
		}
	}
}

// TickOnce runs a single tick and logs it, for the one-shot scope probe.
func (p *FleetPusher) TickOnce(ctx context.Context) FleetPusherResult {
	return p.tickAndLog(ctx)
}

// tickAndLog writes the heartbeat, runs one tick and writes its lines.
func (p *FleetPusher) tickAndLog(ctx context.Context) FleetPusherResult {
	// Heartbeat first: a dead tick leaves this ageing, which is the only signal a
	// dead agent gives. It is written BEFORE any work, so a fresh stamp does NOT
	// mean wakes were delivered — the watchdog reads it only to detect death.
	if err := p.writeHeartbeat(); err != nil {
		p.logf("job=fleet-pusher action=heartbeat outcome=failed error=%v", err)
	}
	result := p.Tick(ctx)
	p.logf("%s", result.Line())
	for _, row := range result.Rows {
		p.logf("%s", row.Line())
	}
	return result
}

// Tick runs one wake pass and reports what it did.
func (p *FleetPusher) Tick(ctx context.Context) FleetPusherResult {
	entries, err := p.Registry.Live(ctx)
	if err != nil {
		// A failed liveness read is NOT an empty fleet. The line still carries the
		// zeros, so the cycle is visible, but it names the failure rather than
		// reporting a confident idle fleet.
		return FleetPusherResult{Err: "registry: " + err.Error()}
	}
	byID := make(map[string]RegistryEntry, len(entries))
	for _, entry := range entries {
		byID[entry.SessionID] = entry
	}

	ids := p.sessionIDs(entries)
	pids := make([]int, 0, len(ids))
	for _, id := range ids {
		if entry, ok := byID[id]; ok && entry.PID > 0 {
			pids = append(pids, entry.PID)
		}
	}
	panes, joinErr := p.Joiner.PanesForPIDs(ctx, pids)
	if joinErr != nil {
		// A failed join is every session with no pane, never a guess. It is logged
		// once rather than per row.
		p.logf("job=fleet-pusher action=join outcome=failed error=%v", joinErr)
		panes = map[int]string{}
	}

	openCards, cardsReadable := p.openCards(ctx)

	result := FleetPusherResult{Sessions: len(ids), Rows: make([]FleetPusherRow, 0, len(ids))}
	for _, id := range ids {
		row := p.rowFor(ctx, id, byID, panes, openCards, cardsReadable)
		result.Rows = append(result.Rows, row)
		if row.Outcome == OutcomeDelivered {
			result.Delivered++
		}
	}
	result.Skipped = result.Sessions - result.Delivered
	return result
}

// sessionIDs returns the scoped ids, or every live session's id, sorted so the
// log is stable across ticks.
func (p *FleetPusher) sessionIDs(entries []RegistryEntry) []string {
	ids := make([]string, 0, len(entries))
	if len(p.Sessions) > 0 {
		ids = append(ids, p.Sessions...)
	} else {
		for _, entry := range entries {
			ids = append(ids, entry.SessionID)
		}
	}
	sort.Strings(ids)
	return ids
}

// openCards reads the board's open cards, failing closed.
func (p *FleetPusher) openCards(ctx context.Context) (map[string]struct{}, bool) {
	if p.Cards == nil {
		return map[string]struct{}{}, true
	}
	cards, err := p.Cards.OpenSessionIDs(ctx)
	if err != nil {
		// Fail closed: a store that cannot be read may hold a card naming any of
		// these sessions, and a wake would bury it. Every session is skipped.
		p.logf("job=fleet-pusher action=cards outcome=failed error=%v", err)
		return map[string]struct{}{}, false
	}
	return cards, true
}

// rowFor decides one session's outcome, in the order `pane-wake.py` uses.
func (p *FleetPusher) rowFor(
	ctx context.Context,
	id string,
	byID map[string]RegistryEntry,
	panes map[int]string,
	openCards map[string]struct{},
	cardsReadable bool,
) FleetPusherRow {
	row := FleetPusherRow{SessionID: id}
	entry, listed := byID[id]
	if listed {
		row.PID = entry.PID
	}

	if id == p.SelfSession {
		row.Outcome = OutcomeSkipSelf
		return row
	}
	if listed && strings.TrimSpace(entry.Name) == FleetManagerName {
		row.Outcome = OutcomeSkipFleetManager
		return row
	}
	if listed && strings.HasPrefix(strings.TrimSpace(entry.Name), PusherNamePrefix) {
		row.Outcome = OutcomeSkipPusher
		return row
	}
	if !listed {
		row.Outcome = OutcomeSkipNotLive
		return row
	}
	pane, hasPane := panes[entry.PID]
	if !hasPane {
		row.Outcome = OutcomeSkipNoPane
		return row
	}
	row.Pane = pane
	if p.SelfPane != "" && pane == p.SelfPane {
		row.Outcome = OutcomeSkipSelf
		return row
	}
	if !cardsReadable {
		row.Outcome = OutcomeSkipOpenCard
		return row
	}
	if _, carded := openCards[id]; carded {
		row.Outcome = OutcomeSkipOpenCard
		return row
	}

	text, readErr := p.Tabs.Text(ctx, pane)
	var screen *string
	if readErr == nil {
		screen = &text
	}
	subjectState := p.subjectState(ctx, id)
	verdict := PaneGuardVerdict(screen, false, subjectState)
	if !IsWakeVerdict(verdict) {
		row.Outcome = verdict
		return row
	}
	if !p.Send {
		row.Outcome = OutcomeDryRun
		return row
	}
	if err := SendWhenReadyWith(
		ctx, p.Tabs, pane, p.message(), p.Now, PaneIsReady,
	); err != nil {
		row.Outcome = "failed:" + err.Error()
		return row
	}
	row.Outcome = OutcomeDelivered
	return row
}

// subjectState reads the session's manager state, failing closed on a resolved
// subject whose state cannot be read.
func (p *FleetPusher) subjectState(ctx context.Context, id string) string {
	if p.Subject == nil {
		return ""
	}
	state, resolved, err := p.Subject.State(ctx, id)
	if err != nil {
		if resolved {
			return SubjectStateUnreadable
		}
		return ""
	}
	return state
}

func (p *FleetPusher) message() string {
	if p.Message != "" {
		return p.Message
	}
	return FleetPusherMessage
}

// writeHeartbeat stamps the heartbeat file the watchdog reads.
func (p *FleetPusher) writeHeartbeat() error {
	if p.HeartbeatPath == "" {
		return nil
	}
	path := expandTilde(p.HeartbeatPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	stamp := p.now().UTC().Format("2006-01-02T15:04:05Z")
	return os.WriteFile(path, []byte(stamp), 0o600)
}

func (p *FleetPusher) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now.Now().Time()
}

func (p *FleetPusher) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// expandTilde resolves a leading `~/`, which exec does not expand for a flag
// value that arrives pre-quoted.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}
