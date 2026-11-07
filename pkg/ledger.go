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
