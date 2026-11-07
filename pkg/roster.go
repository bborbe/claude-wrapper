// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"time"

	"github.com/bborbe/errors"
)

// ToolCall is the call a worker is inside right now.
//
// It is the field that separates a worker executing a long tool call from one
// parked on a gate: the session status says both are not-idle, and this says
// which call and for how long. StartedAt and HeldSeconds go null together when
// the record carries no timestamp — "which tool" and "for how long" fail
// independently, so Name is still reported.
type ToolCall struct {
	Name         string  `json:"name"`
	InputSummary string  `json:"input_summary"`
	StartedAt    *string `json:"started_at"`
	HeldSeconds  *int    `json:"held_seconds"`
}

// AgentRow is one roster row, served by both roster tools.
//
// The field set is deliberately narrow: everything a slice 2–4 concept would
// carry (policy, shipping, wrapper session, open cards, push state) is absent
// rather than null, because a field this process cannot populate honestly is
// worse than an absent one — a caller reads a null as a measurement.
type AgentRow struct {
	AgentID string `json:"agent_id"`
	// SessionID is the identity. AgentID is an address: a foreign row's is
	// suffixed so two workers that minted the same counter value cannot collide.
	SessionID string `json:"session_id"`
	Label     string `json:"label"`
	Cwd       string `json:"cwd"`
	// ParentSession is the session that spawned this one, from the ledger's
	// spawner_session_id. Null for a row this process spawned, which holds no
	// ledger row of its own.
	ParentSession *string `json:"parent_session"`
	// Rehydrated marks a row adopted from the ledger rather than spawned here.
	//
	// Without it three fields a caller DOES see read as live measurements when
	// they are not: status is never terminal, and an empty pending set is
	// indistinguishable from "no gate outstanding".
	Rehydrated      bool      `json:"rehydrated"`
	CreatedAt       *string   `json:"created_at"`
	LastMessage     *string   `json:"last_message"`
	CurrentToolCall *ToolCall `json:"current_tool_call"`
	// SessionStatus is the registry's raw status — idle, busy, waiting, shell —
	// or null when the registry does not list the session. Unlisted is a
	// different fact from not-waiting, so it is never reported as false.
	SessionStatus *string `json:"session_status"`
	AwaitingInput *bool   `json:"awaiting_input"`
}

// Roster answers "what is the fleet doing" across every session, not only the
// ones this process spawned.
type Roster interface {
	List(ctx context.Context) ([]AgentRow, error)
	Get(ctx context.Context, agentID string) (*AgentRow, error)
}

// Liveness answers which session ids are live right now.
//
// A nil map with a nil error means the probe could not run; it is never folded
// into an empty map, because collapsing the two turns a store outage into a
// confident "nothing is live".
type Liveness interface {
	Live(ctx context.Context) (map[string]struct{}, error)
}

// NewRegistryLiveness creates a Liveness over the session registry UNION the
// heartbeat store.
//
// Neither channel alone can answer the question, and the gap is not theoretical:
//
//   - The registry is pid-keyed, so it is structurally blind to a worker with no
//     process of its own. A node-supervisor HEADLESS worker is an in-process SDK
//     `query()` inside the supervisor server, so it has no
//     `~/.claude/sessions/<pid>.json` entry at all — only the heartbeat store
//     knows it is alive.
//   - The heartbeat store is written by whichever producer stamps the session,
//     so it is blind to a session whose producer has stopped posting while the
//     process still runs.
//
// A registry-only implementation happens to pass on a TAB worker — which does
// hold a registry entry — and fails on the headless worker a manager most needs
// to see. That is why the union is the rule rather than a refinement.
//
// The registry is the pid-checked half: a file left behind by a crashed session
// is excluded, which is the direction a roster needs. A cap counter wants the
// opposite reading (presence, which over-counts and fails safe), which is why
// this is a separate type rather than a flag.
func NewRegistryLiveness(registry SessionRegistry, heartbeats HeartbeatReader) Liveness {
	return &registryLiveness{registry: registry, heartbeats: heartbeats}
}

type registryLiveness struct {
	registry   SessionRegistry
	heartbeats HeartbeatReader
}

// Live returns the set of live session ids, or nil when BOTH channels could not
// be read.
//
// One unreadable channel is logged and the other still answers: a positive from
// either is an answer, and discarding a readable channel because its sibling
// failed would hide live workers for no reason. Only a total blind spot returns
// nil, so the caller degrades to its own rows instead of adopting nothing.
func (l *registryLiveness) Live(ctx context.Context) (map[string]struct{}, error) {
	live := map[string]struct{}{}
	readable := l.collectRegistry(ctx, live) + l.collectHeartbeats(ctx, live)
	if readable == 0 {
		return nil, errors.Errorf(ctx, "no liveness channel could be read")
	}
	return live, nil
}

// collectRegistry adds the registry's live ids and reports whether the channel
// could be read.
func (l *registryLiveness) collectRegistry(
	ctx context.Context,
	live map[string]struct{},
) int {
	if l.registry == nil {
		return 0
	}
	entries, err := l.registry.Live(ctx)
	if err != nil {
		log.Printf("WARNING: liveness — the session registry could not be read: %v", err)
		return 0
	}
	for _, entry := range entries {
		// Registry wins where both channels speak: it carries the richer status,
		// and its pid check is the stronger evidence.
		live[entry.SessionID] = struct{}{}
	}
	return 1
}

// collectHeartbeats adds the heartbeat store's live ids and reports whether the
// channel could be read.
func (l *registryLiveness) collectHeartbeats(
	ctx context.Context,
	live map[string]struct{},
) int {
	if l.heartbeats == nil {
		return 0
	}
	rows, err := l.heartbeats.Rows(ctx)
	if err != nil || rows == nil {
		log.Printf("WARNING: liveness — the heartbeat store could not be read: %v", err)
		return 0
	}
	for _, row := range rows {
		// A row is live only on an explicit `live: true`. A renamed or absent key
		// must never be read as liveness.
		if !row.Live || row.SessionID == "" {
			continue
		}
		live[row.SessionID] = struct{}{}
	}
	return 1
}

// NewRoster creates the union roster.
func NewRoster(
	supervisor Supervisor,
	ledger LedgerReader,
	liveness Liveness,
	transcripts TranscriptReader,
	registry SessionRegistry,
) Roster {
	return &roster{
		supervisor:  supervisor,
		ledger:      ledger,
		liveness:    liveness,
		transcripts: transcripts,
		registry:    registry,
	}
}

type roster struct {
	supervisor  Supervisor
	ledger      LedgerReader
	liveness    Liveness
	transcripts TranscriptReader
	registry    SessionRegistry
}

// List returns this process's own sessions unioned with every live session the
// ledger knows.
//
// The union is read at REQUEST time, never once at start-up: a session that was
// not live when this process started stays invisible to a boot-time snapshot for
// its whole life, however live it becomes.
func (r *roster) List(ctx context.Context) ([]AgentRow, error) {
	own, err := r.supervisor.List(ctx)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "list own sessions failed")
	}
	rows := make([]AgentRow, 0, len(own))
	seen := make(map[string]struct{}, len(own))
	for i := range own {
		row := r.rowForOwn(own[i])
		rows = append(rows, row)
		seen[row.SessionID] = struct{}{}
	}

	foreign, err := r.foreignRows(ctx, seen)
	if err != nil {
		// A store that cannot be read returns this process's own roster rather
		// than an empty one, and the degradation is LOGGED — otherwise it is
		// indistinguishable from a fleet with no other workers, which is the
		// failure this union exists to remove.
		log.Printf(
			"WARNING: the ledger could not be read (%v) — this roster is this process's own workers ONLY, so a worker spawned elsewhere is missing from it",
			err,
		)
		return r.enrich(ctx, rows), nil
	}
	rows = append(rows, foreign...)
	return r.enrich(ctx, rows), nil
}

// Get returns one row by agent id.
func (r *roster) Get(ctx context.Context, agentID string) (*AgentRow, error) {
	rows, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].AgentID == agentID {
			return &rows[i], nil
		}
	}
	return nil, errors.Wrapf(ctx, ErrAgentNotFound, "unknown agent %q", agentID)
}

// foreignRows reads the ledger and keeps the live rows this process did not
// spawn.
func (r *roster) foreignRows(
	ctx context.Context,
	seen map[string]struct{},
) ([]AgentRow, error) {
	if r.ledger == nil || r.liveness == nil {
		return nil, nil
	}
	records, err := r.ledger.Rows(ctx)
	if err != nil {
		return nil, err
	}
	if records == nil {
		return nil, errors.Errorf(ctx, "ledger returned no rows")
	}
	live, err := r.liveness.Live(ctx)
	if err != nil {
		return nil, err
	}
	if live == nil {
		return nil, errors.Errorf(ctx, "liveness returned no set")
	}
	rows := make([]AgentRow, 0, len(records))
	for _, record := range records {
		if record.SessionID == "" || record.AgentID == "" {
			// An id-less record is skipped rather than adopted under a synthesised
			// one: the id is what agent_status is keyed on, so an invented one
			// would be addressable but meaningless.
			continue
		}
		if _, ok := live[record.SessionID]; !ok {
			continue
		}
		if _, ok := seen[record.SessionID]; ok {
			// This process's own row wins on a collision: it carries the live
			// child, while a ledger row carries identity alone.
			continue
		}
		rows = append(rows, r.rowForForeign(record))
	}
	return rows, nil
}

// rowForOwn builds a row for a session this process spawned.
func (r *roster) rowForOwn(session Session) AgentRow {
	created := session.StartedAt.UTC().Format(time.RFC3339)
	return AgentRow{
		AgentID:    session.ID,
		SessionID:  session.ID,
		Cwd:        session.Cwd,
		Rehydrated: false,
		CreatedAt:  &created,
	}
}

// rowForForeign builds a row for a ledger record this process did not spawn.
//
// The agent id is a function of the ROW ALONE — the ledger's own id plus a short
// suffix of the session id — so it is stable across calls and can never be
// shadowed by a later local mint. Deriving it from a set seeded by this
// process's own map is the defect that once handed a message to the wrong
// worker.
func (r *roster) rowForForeign(record LedgerRow) AgentRow {
	parent := record.SpawnerSessionID
	var created *string
	if record.SpawnedAt != "" {
		created = &record.SpawnedAt
	}
	return AgentRow{
		AgentID:       record.AgentID + "~" + shortSessionID(record.SessionID),
		SessionID:     record.SessionID,
		Label:         record.Label,
		Cwd:           record.Cwd,
		ParentSession: &parent,
		Rehydrated:    true,
		CreatedAt:     created,
	}
}

// enrich fills the read-time fields every row shares.
func (r *roster) enrich(ctx context.Context, rows []AgentRow) []AgentRow {
	for i := range rows {
		row := &rows[i]
		if r.registry != nil {
			if status, err := r.registry.Status(ctx, row.SessionID); err == nil {
				row.SessionStatus = status
				if status != nil {
					awaiting := *status == "waiting"
					row.AwaitingInput = &awaiting
				}
			}
		}
		if r.transcripts == nil {
			continue
		}
		if text, err := r.transcripts.LastAssistantText(ctx, row.SessionID); err == nil {
			row.LastMessage = text
		}
		if call, err := r.transcripts.CurrentToolCall(ctx, row.SessionID); err == nil {
			row.CurrentToolCall = call
		}
	}
	return rows
}

// shortSessionID is the addressing suffix of a foreign row's agent id.
func shortSessionID(sessionID string) string {
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[:8]
}
