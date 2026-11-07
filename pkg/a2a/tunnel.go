// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// Tunnel pulls pushed statuses from an in-cluster relay.
//
// ⚠️ WHY A PULL AND NOT A LISTENER. The Mac cannot accept an inbound connection
// from the cluster at all: its firewall drops every port except the two
// something else already owns (measured 2026-11-04 from both cluster nodes). The
// Mac dialling out is the only direction that works, so the tunnel is inverted
// and this end becomes a client.
type Tunnel struct {
	RelayURL string
	Secret   string
	Registry *PushRegistry
	HTTP     *http.Client
	// PollWait is the long-poll window the relay holds the request open for.
	PollWait string
	// RetryDelay is the base backoff, capped at MaxRetryDelay.
	RetryDelay    time.Duration
	MaxRetryDelay time.Duration
	PullTimeout   time.Duration
}

// NewTunnel creates the puller, or returns an error when the configuration
// cannot work.
//
// ⚠️ A TUNNEL WITH NO SECRET IS REFUSED AT CONSTRUCTION, NOT STARTED AND LEFT TO
// 401: an empty secret produces a loop that retries forever and delivers
// nothing, with no error anywhere to say why.
func NewTunnel(relayURL, secret string, registry *PushRegistry) (*Tunnel, error) {
	if relayURL == "" {
		return nil, errors.Errorf(context.Background(), "no relay URL configured")
	}
	if secret == "" {
		return nil, errors.Errorf(
			context.Background(),
			"a relay URL requires a non-empty tunnel secret — an empty one produces a loop "+
				"that retries forever and delivers nothing",
		)
	}
	return &Tunnel{
		RelayURL:      strings.TrimRight(relayURL, "/"),
		Secret:        secret,
		Registry:      registry,
		HTTP:          &http.Client{},
		PollWait:      "25s",
		RetryDelay:    time.Second,
		MaxRetryDelay: 30 * time.Second,
		PullTimeout:   90 * time.Second,
	}, nil
}

// Run pulls until the context is cancelled.
//
// ⚠️ THE LOOP OWNS ITS OWN FAILURE HANDLING. A relay that is briefly unreachable
// must not end the loop: the far side may come and go, and a loop that exits on
// the first error leaves the channel silently dead.
func (t *Tunnel) Run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		delivered, err := t.PullOnce(ctx)
		if err != nil {
			failures++
			// Logged on the first failure and then on each doubling, so a
			// persistent fault stays visible without a 1 Hz drumbeat.
			if failures == 1 || failures&(failures-1) == 0 {
				log.Printf(
					"WARNING: a2a relay pull failed %d× in a row: %v", failures, err,
				)
			}
			delay := t.RetryDelay * time.Duration(1<<minInt(failures-1, 8))
			if delay > t.MaxRetryDelay {
				delay = t.MaxRetryDelay
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}
		failures = 0
		// ⚠️ AN EMPTY PULL MUST YIELD TO THE EVENT LOOP with a real timer, not a
		// resolved promise: a tight loop starves timers and sockets while burning
		// CPU.
		if delivered == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.RetryDelay):
			}
		}
	}
}

// PullOnce performs one long-poll and dispatches what it carries.
func (t *Tunnel) PullOnce(ctx context.Context) (int, error) {
	pullCtx, cancel := context.WithTimeout(ctx, t.PullTimeout)
	defer cancel()
	url := fmt.Sprintf("%s/relay/pull?wait=%s", t.RelayURL, t.PollWait)
	req, err := http.NewRequestWithContext(pullCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "build the relay pull failed")
	}
	req.Header.Set(TunnelSecretHeader, t.Secret)
	res, err := t.http().Do(req)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "the relay pull failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return 0, errors.Errorf(ctx, "the relay pull answered HTTP %d", res.StatusCode)
	}
	var payload struct {
		Items []struct {
			Token string          `json:"token"`
			Body  json.RawMessage `json:"body"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&payload); err != nil {
		return 0, errors.Wrapf(ctx, err, "decode the relay pull failed")
	}
	delivered := 0
	for _, item := range payload.Items {
		// An unknown token is dropped rather than forwarded: forwarding would let
		// the relay make this process file a status against an arbitrary worker.
		if item.Token == "" {
			continue
		}
		kind, body, err := parseStreamEnvelope(item.Body)
		if err != nil || kind != "statusUpdate" {
			continue
		}
		if t.Registry.DeliverByToken(item.Token, body) {
			delivered++
		}
	}
	return delivered, nil
}

func (t *Tunnel) http() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return http.DefaultClient
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
