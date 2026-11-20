// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"sort"
	"sync"
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
	Rehydrated bool `json:"rehydrated"`
	// Unlisted marks a row that is live on the heartbeat endpoint but is neither
	// a session this process spawned nor a ledger record — an ordinary
	// interactive tab, or a peer's worker.
	//
	// It exists because the alternative is silence: a roster that omits these
	// answers "nothing is running" to a manager looking at a live fleet. The row
	// carries liveness and nothing else, and UnlistedReason says so, so an empty
	// label or a null status is not read as a measurement.
	Unlisted bool `json:"unlisted"`
	// UnlistedReason explains an Unlisted row in words. Empty on every other row.
	UnlistedReason  string    `json:"unlisted_reason,omitempty"`
	CreatedAt       *string   `json:"created_at"`
	LastMessage     *string   `json:"last_message"`
	CurrentToolCall *ToolCall `json:"current_tool_call"`
	// SessionStatus is the registry's raw status — idle, busy, waiting, shell —
	// or null when the registry does not list the session. Unlisted is a
	// different fact from not-waiting, so it is never reported as false.
	SessionStatus *string `json:"session_status"`
	AwaitingInput *bool   `json:"awaiting_input"`
	// PendingQuestion is the park this process holds for the session, or null.
	//
	// It is populated for a session this daemon HOLDS — including one adopted from
	// a previous daemon generation, whose question `Adopt` restored from the
	// session's durable record — and null for a foreign or unlisted row, which this
	// process cannot answer and so cannot honestly report. That is the whole point
	// of the field: a manager's own-parks step reads it to answer a worker's park,
	// and a park that lived only in a dead process's memory would read here as
	// "nothing parked".
	//
	// ⚠️ The field is present on EVERY row, never omitted. `commands/steps/own-parks.md`
	// reads its presence as "this server can report parks", and its absence as an
	// older server that cannot — so an `omitempty` here would make the live daemon
	// look like the server that shipped before parks were readable at all.
	PendingQuestion *PendingQuestion `json:"pending_question"`
	// PendingPermissions is always empty here, and deliberately so.
	//
	// A wrapper park is a `pending_question`, never a `pending_permissions` entry:
	// `pending_permissions` is the policy-layer list, whose rows carry
	// `allowlist_entry`, and the policy has already answered every allowlisted park
	// before one can reach this roster. Emitting it as `[]` keeps a reader from
	// treating a wrapper park as a policy-classified one and dispatching it to a
	// wrangler that would only escalate it.
	PendingPermissions []any `json:"pending_permissions"`
}

// PendingQuestion is a held session's park in the shape the fleet's own-parks step
// reads (`commands/steps/own-parks.md`).
//
// It carries only the identifying input — `command` and `file_path` — never a
// Write's `content`, matching `pending_permissions`' own rule that a park must be
// judgeable without dumping its payload into a manager's context. For an
// `AskUserQuestion` the `questions` carry the prompt and its option labels, which
// is what the step routes to the operator.
type PendingQuestion struct {
	RequestID   string           `json:"request_id"`
	Kind        string           `json:"kind"`
	ToolName    string           `json:"tool_name"`
	ToolUseID   string           `json:"tool_use_id,omitempty"`
	Input       map[string]any   `json:"input,omitempty"`
	Questions   []QuestionPrompt `json:"questions,omitempty"`
	RequestedAt string           `json:"requested_at,omitempty"`
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

	foreign, live, err := r.foreignRows(ctx, seen)
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
	rows = append(rows, r.unlistedRows(live, seen, foreign)...)
	return r.enrich(ctx, rows), nil
}

// unlistedRows returns one row per session the liveness union reports live that
// no row already carries.
//
// This is the honest half of the roster. A session this daemon did not spawn and
// the ledger has no record of — an ordinary interactive tab, a peer's worker — is
// genuinely live and genuinely invisible to both existing sources, and a short
// list answers "nothing is running" to a manager looking at a live fleet. The
// rows are marked Unlisted so a caller reads them as liveness alone.
//
// A nil live set means the probe could not run; that is rendered as no rows, and
// the caller's own degradation log is the signal — never as "nothing is
// unlisted", which would be the same confident wrong answer this change removes.
func (r *roster) unlistedRows(
	live map[string]struct{},
	seen map[string]struct{},
	foreign []AgentRow,
) []AgentRow {
	if live == nil {
		return nil
	}
	known := make(map[string]struct{}, len(seen)+len(foreign))
	for id := range seen {
		known[id] = struct{}{}
	}
	for i := range foreign {
		known[foreign[i].SessionID] = struct{}{}
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		if _, ok := known[id]; !ok {
			ids = append(ids, id)
		}
	}
	// Sorted so two calls over the same fleet render the same order: a roster
	// that reshuffles between reads is a diff a caller cannot trust.
	sort.Strings(ids)
	rows := make([]AgentRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, rowForUnlisted(id))
	}
	return rows
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
) ([]AgentRow, map[string]struct{}, error) {
	if r.ledger == nil || r.liveness == nil {
		// No liveness source: the roster is honest about being unable to tell,
		// which is why the live set is nil rather than empty.
		return nil, nil, nil
	}
	records, err := r.ledger.Rows(ctx)
	if err != nil {
		return nil, nil, err
	}
	if records == nil {
		return nil, nil, errors.Errorf(ctx, "ledger returned no rows")
	}
	live, err := r.liveness.Live(ctx)
	if err != nil {
		return nil, nil, err
	}
	if live == nil {
		return nil, nil, errors.Errorf(ctx, "liveness returned no set")
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
	return rows, live, nil
}

// rowForUnlisted builds a row for a session that is live but that neither this
// process nor the ledger knows.
//
// The agent id is the session id, the same convention this process's own rows
// use: an unlisted session has no ledger id to borrow, and inventing one would
// make it addressable under an id nothing else agrees with.
func rowForUnlisted(sessionID string) AgentRow {
	return AgentRow{
		AgentID:        sessionID,
		SessionID:      sessionID,
		Unlisted:       true,
		UnlistedReason: "live on the heartbeat endpoint, but not a session this daemon spawned and not in the ledger",
	}
}

// rowForOwn builds a row for a session this process spawned.
func (r *roster) rowForOwn(session Session) AgentRow {
	created := session.StartedAt.UTC().Format(time.RFC3339)
	return AgentRow{
		AgentID:            session.ID,
		SessionID:          session.ID,
		Cwd:                session.Cwd,
		Rehydrated:         false,
		CreatedAt:          &created,
		PendingQuestion:    pendingQuestionOf(session.Question),
		PendingPermissions: []any{},
	}
}

// pendingQuestionOf reduces a held session's question to the roster's park shape.
//
// nil in, nil out: an unparked session carries no park, and the field's presence
// (not its value) is what tells a reader this server can report parks at all. The
// input is reduced to the identifying keys so a Write's content never enters a
// manager's context through the roster.
func pendingQuestionOf(question *Question) *PendingQuestion {
	if question == nil {
		return nil
	}
	out := &PendingQuestion{
		RequestID: question.RequestID,
		Kind:      string(question.Kind),
		ToolName:  question.ToolName,
		ToolUseID: question.ToolUseID,
		Questions: question.Questions,
	}
	if !question.RequestedAt.IsZero() {
		out.RequestedAt = question.RequestedAt.UTC().Format(time.RFC3339)
	}
	for _, key := range []string{"command", "file_path"} {
		if value, ok := question.Input[key].(string); ok && value != "" {
			if out.Input == nil {
				out.Input = map[string]any{}
			}
			out.Input[key] = value
		}
	}
	return out
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

// Enrichment bounds.
//
// The sweep is file I/O: one registry snapshot for the whole call, then one
// bounded transcript read per row. Measured 2026-11-17 over 62 live sessions and
// 126 registry files, the unbounded form took 20.0 s — past the MCP client's
// timeout, which is how a manager lost the ability to list and message its
// workers. These two constants are what keep it finite.
const (
	// rosterEnrichConcurrency bounds how many rows are read at once.
	//
	// Eight is enough to turn that 20.0 s sweep into well under a second on a
	// local disk without opening a file descriptor per session: the fleet carries
	// hundreds of sessions, and one descriptor each would exhaust the default
	// limit on the machines that run the largest fleets.
	rosterEnrichConcurrency = 8

	// rosterEnrichRowTimeout bounds how long the sweep WAITS for one row.
	//
	// A transcript read is local file I/O, so a deadline cannot interrupt it
	// mid-parse; what this bounds is the row's share of the sweep when the file is
	// pathological — a multi-megabyte line, or a path on a stalled network mount.
	// The row is then served with its static fields and null read-time fields,
	// which AgentRow already documents as "not measured" rather than "nothing
	// there". Two seconds is an order of magnitude above the measured per-row cost
	// (~50 ms warm), so it only ever fires on a genuine outlier.
	rosterEnrichRowTimeout = 2 * time.Second
)

// enrich fills the read-time fields every row shares.
//
// The registry is read ONCE for the whole sweep and the transcript once per row,
// with bounded concurrency and a per-row deadline — see the constants above. The
// earlier form asked the registry three times per row and parsed each transcript
// tail twice, sequentially.
func (r *roster) enrich(ctx context.Context, rows []AgentRow) []AgentRow {
	entries := r.registrySnapshot(ctx)
	for i := range rows {
		applyRegistryStatus(&rows[i], entries)
		// `pending_permissions` is served as `[]` on EVERY row, never `null`: a
		// reader distinguishes "the server reports no policy parks" from "the
		// server predates the field" by presence, and a null would blur the two.
		if rows[i].PendingPermissions == nil {
			rows[i].PendingPermissions = []any{}
		}
	}
	if r.transcripts == nil {
		return rows
	}
	sem := make(chan struct{}, rosterEnrichConcurrency)
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(row *AgentRow) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			enrichFromTranscript(ctx, r.transcripts, row, entries)
		}(&rows[i])
	}
	wg.Wait()
	return rows
}

// registrySnapshot reads the registry once and indexes it by session id.
//
// nil means the registry could not be read, or none is wired: every row then
// carries a null status, which is the honest reading of "not measured".
func (r *roster) registrySnapshot(ctx context.Context) map[string]RegistryEntry {
	if r.registry == nil {
		return nil
	}
	all, err := r.registry.Entries(ctx)
	if err != nil {
		log.Printf(
			"WARNING: the session registry could not be read (%v) — rows carry no session status and no transcript read-time fields",
			err,
		)
		return nil
	}
	bySession := make(map[string]RegistryEntry, len(all))
	for _, entry := range all {
		if _, ok := bySession[entry.SessionID]; ok {
			// First wins, matching what Status returned when it scanned the same
			// entries in directory order.
			continue
		}
		bySession[entry.SessionID] = entry
	}
	return bySession
}

// applyRegistryStatus fills a row's status half from the snapshot.
func applyRegistryStatus(row *AgentRow, entries map[string]RegistryEntry) {
	entry, ok := entries[row.SessionID]
	if !ok {
		// Unlisted is a different fact from not-waiting, so both fields stay null.
		return
	}
	status := entry.Status
	row.SessionStatus = &status
	awaiting := status == "waiting"
	row.AwaitingInput = &awaiting
}

// enrichFromTranscript fills a row's read-time half, bounded by the row deadline.
func enrichFromTranscript(
	ctx context.Context,
	reader TranscriptReader,
	row *AgentRow,
	entries map[string]RegistryEntry,
) {
	entry, listed := entries[row.SessionID]
	if !listed || entry.Cwd == "" {
		// No registry entry means no cwd, hence no transcript path to derive.
		return
	}
	cwd := entry.Cwd

	rowCtx, cancel := context.WithTimeout(ctx, rosterEnrichRowTimeout)
	defer cancel()

	// The read runs in its own goroutine so the deadline can abandon it: a
	// blocking file read does not observe a context, and the sweep must not wait
	// on one row longer than the bound above.
	state := make(chan *TranscriptState, 1)
	go func() {
		result, err := reader.State(rowCtx, row.SessionID, &cwd)
		if err != nil {
			state <- nil
			return
		}
		state <- result
	}()

	select {
	case result := <-state:
		if result == nil {
			return
		}
		row.LastMessage = result.LastMessage
		row.CurrentToolCall = result.CurrentToolCall
	case <-rowCtx.Done():
		// The row keeps its static fields; the read-time ones stay null.
	}
}

// shortSessionID is the addressing suffix of a foreign row's agent id.
func shortSessionID(sessionID string) string {
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[:8]
}
