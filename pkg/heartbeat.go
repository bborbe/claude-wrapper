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
)

// HeartbeatPath is attention-controller's session-heartbeat route.
const HeartbeatPath = "/api/1.0/session-heartbeat"

// DefaultHeartbeatInterval is how often the poster stamps every live session.
//
// Thirty seconds against the store's sixty-second window is one whole missed
// tick of slack: a single failed round must not make a live session read as
// gone.
const DefaultHeartbeatInterval = 30 * time.Second

// heartbeatTimeout bounds one heartbeat POST. It matches the store's other
// clients so a slow store fails the same way on every path.
const heartbeatTimeout = 15 * time.Second

// HeartbeatRow is one row of the session-heartbeat store.
//
// `source` is `mcp-timer` because that is the only value the store treats as
// proof of a self-posting producer; `task` and `vault` are deliberately absent
// and both empty, which is legal — a session with no task anchor is a real
// state, while declaring one without the other is refused.
type HeartbeatRow struct {
	SessionID string `json:"session_id"`
	Location  string `json:"location"`
	State     string `json:"state"`
	Source    string `json:"source"`
	// Live is the STORE's verdict — `now - at` inside the staleness window — and
	// is only meaningful on a read. It is omitempty so a POST carries the same
	// body it always did: the store computes freshness itself and a
	// caller-supplied verdict is exactly what it refuses to take.
	Live bool `json:"live,omitempty"`
}

// HeartbeatClient writes heartbeat rows.
type HeartbeatClient interface {
	Post(ctx context.Context, row HeartbeatRow) error
}

// HeartbeatReader reads heartbeat rows.
type HeartbeatReader interface {
	// Rows returns every row the store serves, or nil when the read could not
	// run.
	//
	// nil is "could not read it" and is never folded into an empty slice: an
	// empty list is a positive claim that nothing is live anywhere, while a
	// transport failure or a non-200 is the probe failing to run. Collapsing the
	// two turns a store outage into a confident "nothing is live" — the direction
	// that silently hides live workers.
	Rows(ctx context.Context) ([]HeartbeatRow, error)
}

// HeartbeatStore is the read-and-write surface. Consumers take whichever half
// they need: the poster only writes, liveness only reads.
type HeartbeatStore interface {
	HeartbeatClient
	HeartbeatReader
}

// NewHeartbeatClient creates a HeartbeatStore backed by attention-controller.
func NewHeartbeatClient(baseURL string, httpClient *http.Client) HeartbeatStore {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: heartbeatTimeout}
	}
	return &heartbeatClient{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
}

type heartbeatClient struct {
	baseURL    string
	httpClient *http.Client
}

// Post writes one row and logs the attribution line.
func (c *heartbeatClient) Post(ctx context.Context, row HeartbeatRow) error {
	if c.baseURL == "" {
		return errors.Errorf(ctx, "no attention-controller endpoint configured")
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal heartbeat failed")
	}
	// The attribution line. A row alone proves nothing: the store writes one
	// whoever POSTs it, so a hand-run curl reproduces it identically — this line
	// is what ties the row to THIS process.
	log.Printf("claude-wrapper: POST %s session_id=%s", HeartbeatPath, row.SessionID)
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+HeartbeatPath, bytes.NewReader(encoded),
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "build heartbeat request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(request)
	if err != nil {
		return errors.Wrapf(ctx, err, "post heartbeat failed")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= http.StatusMultipleChoices {
		return errors.Errorf(ctx, "heartbeat post answered %d", resp.StatusCode)
	}
	return nil
}

// Rows reads the whole heartbeat store.
func (c *heartbeatClient) Rows(ctx context.Context) ([]HeartbeatRow, error) {
	var rows []HeartbeatRow
	if err := readJSONArray(ctx, c.httpClient, c.baseURL, HeartbeatPath, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// HeartbeatPoster stamps every live session on a timer.
//
// It exists because liveness today depends on the session's OWN supervisor
// posting: a session started without one — `--strict-mcp-config`, no supervisor
// entry — has no row at all, so it reads as not-live to every reader that
// consults the store. A central poster makes liveness independent of which
// server spawned what.
type HeartbeatPoster interface {
	// Run stamps every live session, then repeats until ctx is cancelled.
	Run(ctx context.Context) error
}

// NewHeartbeatPoster creates a HeartbeatPoster.
//
// interval is a parameter rather than a constant so a test can drive the loop
// without waiting thirty seconds; production passes DefaultHeartbeatInterval.
// No clock is injected here, and that is deliberate rather than an omission: the
// store stamps `at` from its OWN clock and ignores a caller-supplied one, so a
// session cannot declare itself immortal. This process therefore has no time to
// report, and adding a clock it never reads would be a dependency pretending to
// be a measurement.
func NewHeartbeatPoster(
	registry SessionRegistry,
	client HeartbeatClient,
	interval time.Duration,
) HeartbeatPoster {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	return &heartbeatPoster{registry: registry, client: client, interval: interval}
}

type heartbeatPoster struct {
	registry SessionRegistry
	client   HeartbeatClient
	interval time.Duration
}

// Run posts once immediately and then on every tick, until ctx is cancelled.
//
// The first round is immediate rather than one interval later: a session that
// starts and reads live within 60 s must not wait a full 30 s for its first
// stamp on top of the store's own lag.
func (p *heartbeatPoster) Run(ctx context.Context) error {
	p.round(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.round(ctx)
		}
	}
}

// round stamps every live session once.
func (p *heartbeatPoster) round(ctx context.Context) {
	if p.registry == nil || p.client == nil {
		return
	}
	entries, err := p.registry.Live(ctx)
	if err != nil {
		// An unreadable registry is logged and skipped, never fatal: the daemon
		// serves more than this loop, and a transient read failure must not take
		// it down.
		log.Printf("WARNING: heartbeat round skipped — the session registry could not be read: %v", err)
		return
	}
	for _, entry := range entries {
		// One POST per live session against a 15 s client timeout, so a stalled
		// store turns this loop into a long wait. Stop at the first cancellation
		// instead of grinding through every remaining entry.
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := p.client.Post(ctx, HeartbeatRow{
			SessionID: entry.SessionID,
			Location:  "local",
			State:     "idle",
			Source:    "mcp-timer",
		}); err != nil {
			log.Printf("WARNING: heartbeat for session %s failed: %v", entry.SessionID, err)
		}
	}
}
