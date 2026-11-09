// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// LedgerPath is the attention-controller route the spawn ledger is served from
// and written to.
const LedgerPath = "/api/1.0/session-ledger"

// ledgerTimeout bounds one ledger round trip.
//
// The store is local but not fast: it serves a body measured in megabytes and
// shares the controller with the heartbeat build, so a five-second bound sat
// inside the observed tail and refused healthy reads. Fifteen seconds matches
// the node client's own SESSION_LEDGER_TIMEOUT_MS — the two halves of one count
// must give up at the same point, or the failure depends on which store was
// slower.
const ledgerTimeout = 15 * time.Second

// LedgerRow is one row of the spawn ledger, on the wire.
//
// The field names are attention-controller's, not this repo's: `session_id` is
// the identity and the only sound key (`agent_id` restarts at 0 on every
// supervisor reconnect, so hundreds of rows share one value), and the spawner
// is spelled `spawner_session_id` on the endpoint while the file ledger the node
// server still writes spells it `parent_session`.
type LedgerRow struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	Label     string `json:"label"`
	Cwd       string `json:"cwd"`
	Mode      string `json:"mode"`
	Launcher  string `json:"launcher"`
	// SpawnerSessionID is the session that started this one — the join that makes
	// a worker spawned by another session visible here at all.
	SpawnerSessionID string `json:"spawner_session_id"`
	Location         string `json:"location"`
	// PaneID is the WezTerm pane a tab worker runs in, and the only handle a
	// follow-up message can be typed into. Empty for a headless worker.
	PaneID string `json:"pane_id,omitempty"`
	// Role is the resolved role the spawn ran under.
	//
	// A tab spawn's role is what explains its window and colour, so a row without
	// it cannot answer "why is this worker pink".
	Role string `json:"role,omitempty"`
	// ModeSource names the chain step that decided the spawn mode, so a mode that
	// surprised the caller can be traced to the env, the config file or the
	// built-in default rather than guessed at.
	ModeSource string `json:"mode_source,omitempty"`
	// ResumedFrom is the session id this one continues, or empty for a fresh
	// conversation.
	ResumedFrom string `json:"resumed_from,omitempty"`
	// SpawnedAt is REQUIRED by the store — a POST without it is refused 400
	// ("validate SpawnedAt failed: spawned_at is unset"), measured 2026-11-07.
	// The wire name is `spawned_at`, not `created_at`: the file ledger and the
	// endpoint spell the same instant differently, and the endpoint's spelling
	// is the one that validates.
	SpawnedAt string `json:"spawned_at"`
	// Status is the spawn's outcome as the ledger records it: `running` while the
	// server that spawned the session still holds it, and `done` or `error` once
	// that server closed the record.
	//
	// ⚠️ It is trustworthy in ONE direction only, and that asymmetry is the whole
	// reason the heartbeat poster reads it. A terminal status proves the session
	// ENDED; every other value proves nothing, because nothing closes a record
	// whose server went away (measured 2026-10-06: 1110 of 1372 records still
	// asserted `running`). So a reader may skip on a terminal status and must
	// never skip on the absence of one — see `terminalLedgerStatuses`.
	Status string `json:"status"`
	// Result is the child's own terminal `result` event, carried verbatim, absent
	// on a spawn row and on every row whose turn never closed.
	//
	// ⚠️ OPAQUE, for the same reason attention-controller carries it as
	// `json.RawMessage`: the store never reads inside the value and this process
	// does not either, so typing it would only invite a second interpretation of a
	// shape the child owns. It is also the only place an error can be carried —
	// the store has no `error` column, and a patch naming one is refused outright
	// (`DisallowUnknownFields`), which is why the error half of a terminal verdict
	// is written as a synthetic result object.
	Result json.RawMessage `json:"result,omitempty"`
	// EndedAt is when the turn that produced the verdict ended, absent on a spawn
	// row and cleared again when the next turn opens.
	//
	// ⚠️ A POINTER, so "still mid-turn" and "ended at the zero instant" stay
	// distinguishable on the wire — the same rule the store applies to its own
	// `ended_at`. Empty is omitted rather than written as `""`, because the store
	// parses the value as an RFC3339 instant and an empty string is a 400.
	EndedAt *string `json:"ended_at,omitempty"`
}

// LedgerPatch is a partial update to one ledger row: the fields to change, and
// nothing else.
//
// ⚠️ **A map of raw fields rather than a struct, and that is the store's own
// contract rather than a preference.** The two writes this client makes are
// opposites — a turn END sets `ended_at` and `result`, while a new turn CLEARS
// them — and `null` is how the store spells "clear" (`SessionLedgerPatch.Apply`
// deletes the key rather than storing a null). A struct with `omitempty` cannot
// express that distinction: an absent key and a null key are different writes.
//
// ⚠️ **PATCH, never POST.** A POST replaces the whole row, so a terminal write
// through it would blank every field the spawn recorded — label, cwd, mode,
// role, spawner. That is the defect the node client grew a PATCH to remove, and
// it is why `recordStop` refuses to re-post an existing row.
type LedgerPatch map[string]json.RawMessage

// TerminalLedgerPatch is the verdict a turn end records: the status, the child's
// own `result` event, and when the turn ended.
//
// `result` is omitted only when the caller has none, because writing an empty
// value would assert a result the child never produced.
func TerminalLedgerPatch(
	status string,
	result json.RawMessage,
	endedAt string,
) LedgerPatch {
	patch := LedgerPatch{
		"status":   json.RawMessage(strconv.Quote(status)),
		"ended_at": json.RawMessage(strconv.Quote(endedAt)),
	}
	if len(result) > 0 {
		patch["result"] = result
	}
	return patch
}

// TurnStartedLedgerPatch re-opens a row whose turn has just been written.
//
// ⚠️ **The re-open is not bookkeeping — without it a live worker reads dead.** A
// terminal row is what stops the heartbeat poster stamping a session
// (`terminalLedgerStatuses`), and a headless worker in this wrapper outlives its
// turn: it keeps its child, its stdin and its reachability, and the next turn is
// one `send` away. Leaving `done` behind would make every reader that trusts the
// ledger treat a reachable worker as finished. `null` clears `ended_at` and
// `result` on the store side, which is what "this turn has no verdict yet" means
// on the wire.
func TurnStartedLedgerPatch() LedgerPatch {
	return LedgerPatch{
		"status":   json.RawMessage(`"running"`),
		"ended_at": json.RawMessage("null"),
		"result":   json.RawMessage("null"),
	}
}

// LedgerReader reads and writes the spawn ledger.
type LedgerReader interface {
	// Rows returns every row the store serves, or a nil slice when the read could
	// not run.
	//
	// nil is "could not read it" and is never folded into an empty slice: an
	// empty list is a positive claim that the fleet has spawned nothing, while a
	// transport failure or a non-200 is the probe failing to run. A caller that
	// collapses the two turns a store outage into a confident empty roster.
	Rows(ctx context.Context) ([]LedgerRow, error)
	// Post writes one row, attributing it to spawnerSessionID.
	//
	// It never fails a caller on a store outage — the spawn it describes has
	// already happened — but it returns the error so the caller may log it. Every
	// call writes one stdout line naming the method, the path and the session id:
	// that line is the only thing that attributes the row to THIS process, since
	// the store writes a row whoever POSTs it.
	Post(ctx context.Context, spawnerSessionID string, row LedgerRow) error
	// Patch writes a partial update to the row for sessionID.
	//
	// It is the write a turn end needs and a POST cannot be: the spawn row is
	// owned by the spawn, and a full replace would blank the fields it recorded.
	// A row that does not exist answers ErrLedgerRowAbsent — a normal outcome for
	// a child this process adopted without ever having spawned it, and never
	// folded into success, because a verdict written nowhere is the defect this
	// method exists to remove.
	Patch(ctx context.Context, sessionID string, patch LedgerPatch) error
}

// NewLedgerClient creates a LedgerReader backed by attention-controller.
func NewLedgerClient(
	baseURL string,
	httpClient *http.Client,
	now libtime.CurrentDateTimeGetter,
) LedgerReader {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: ledgerTimeout}
	}
	return &ledgerClient{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient, now: now}
}

type ledgerClient struct {
	baseURL    string
	httpClient *http.Client
	now        libtime.CurrentDateTimeGetter
}

// Rows reads the whole ledger.
func (c *ledgerClient) Rows(ctx context.Context) ([]LedgerRow, error) {
	var rows []LedgerRow
	if err := readJSONArray(ctx, c.httpClient, c.baseURL, LedgerPath, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Post writes one row.
func (c *ledgerClient) Post(
	ctx context.Context,
	spawnerSessionID string,
	row LedgerRow,
) error {
	if c.baseURL == "" {
		return errors.Errorf(ctx, "no attention-controller endpoint configured")
	}
	if row.Location == "" {
		// Every row this process writes is a local one; the endpoint requires the
		// field and the file ledger never carried it.
		row.Location = "local"
	}
	if row.SpawnerSessionID == "" {
		row.SpawnerSessionID = spawnerSessionID
	}
	if row.SpawnedAt == "" {
		row.SpawnedAt = c.now.Now().Time().UTC().Format(time.RFC3339)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal ledger row failed")
	}
	// The attribution line, written BEFORE the POST so a store that refuses the
	// row still leaves evidence that this process tried — a row alone proves
	// nothing, because the store writes one whoever POSTs it.
	log.Printf(
		"claude-wrapper: POST %s session_id=%s spawner_session_id=%s",
		LedgerPath, row.SessionID, row.SpawnerSessionID,
	)
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+LedgerPath, bytes.NewReader(encoded),
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "build ledger post failed")
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(request)
	if err != nil {
		return errors.Wrapf(ctx, err, "post ledger failed")
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain so the connection is reusable; the body carries no field this caller
	// reads.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= http.StatusMultipleChoices {
		return errors.Errorf(ctx, "ledger post answered %d", resp.StatusCode)
	}
	return nil
}

// Patch writes a partial update to one row.
func (c *ledgerClient) Patch(
	ctx context.Context,
	sessionID string,
	patch LedgerPatch,
) error {
	if c.baseURL == "" {
		return errors.Errorf(ctx, "no attention-controller endpoint configured")
	}
	if sessionID == "" {
		return errors.Errorf(ctx, "ledger patch without a session id")
	}
	if len(patch) == 0 {
		// An empty patch is a 200 that changed nothing, which reads as a written
		// verdict to the caller. Refusing it keeps "the write landed" honest.
		return errors.Errorf(ctx, "ledger patch for session %s carries no fields", sessionID)
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal ledger patch failed")
	}
	// The attribution line, written BEFORE the request for the same reason Post
	// writes one: the store answers a row whoever PATCHes it, so only this line
	// says which process wrote the verdict.
	log.Printf(
		"claude-wrapper: PATCH %s/%s session_id=%s status=%s",
		LedgerPath, sessionID, sessionID, patchStatus(patch),
	)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPatch,
		c.baseURL+LedgerPath+"/"+url.PathEscape(sessionID),
		bytes.NewReader(encoded),
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "build ledger patch failed")
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(request)
	if err != nil {
		return errors.Wrapf(ctx, err, "patch ledger failed")
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain so the connection is reusable; the body carries no field this caller
	// reads beyond the status code.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return errors.Wrapf(
			ctx, ErrLedgerRowAbsent, "ledger row for session %s does not exist", sessionID,
		)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return errors.Errorf(ctx, "ledger patch answered %d", resp.StatusCode)
	}
	return nil
}

// patchStatus renders the status a patch carries, for the attribution line.
//
// It reads the raw field rather than taking a parameter so the log cannot drift
// from what was actually sent; an unparseable value is reported as-is instead of
// being dropped from the line.
func patchStatus(patch LedgerPatch) string {
	raw, ok := patch["status"]
	if !ok {
		return "-"
	}
	status, err := strconv.Unquote(string(raw))
	if err != nil {
		return string(raw)
	}
	return status
}
