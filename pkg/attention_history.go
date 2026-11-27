// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// AttentionHistoryPath is the store's any-state list route.
//
// ⚠️ It is NOT `GET /api/1.0/attention`, which returns open items only. The two answer
// different questions, and only this one answers "have I ever carded this key".
const AttentionHistoryPath = "/api/1.0/attention/history"

// CardHistoryLimit bounds one history read. The route is paginated by `limit`; a producer
// that has carded more than this many keys is past the point where one more card matters.
const CardHistoryLimit = 500

// NewBoardCardHistory creates a CardLookup that sees cards in ANY state.
//
// ⚠️ **Why this exists, and why `NewBoardCardLookup` cannot do the job.** That one is built
// on `BoardStore.Open`, which reads `GET /api/1.0/attention` — open items only. For "is my
// card still on the board" that is right. For "have I already carded this episode" it is
// wrong the moment the operator answers: the card closes, the lookup finds nothing, and the
// next post creates a duplicate. The in-process memory hides it until a restart, and then
// it does not.
//
// Measured 2026-11-26 on the stall escalation: cards `cedf31eb…` (17:58:22) and `a7f38710…`
// (18:21:08) carry the identical dedup key, the second posted after the daemon restarted at
// 18:19:08 — because the first had already been answered and closed.
func NewBoardCardHistory(baseURL string, client *http.Client) CardLookup {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &boardCardHistory{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

type boardCardHistory struct {
	baseURL string
	client  *http.Client
}

// ExistingCard returns the item id of ANY card with this producer and dedup key — open,
// answered or closed — or "" when there is none.
//
// A read failure is returned as an error rather than an empty result: the caller must fail
// closed and post nothing, because "the store did not answer" is not "no card exists", and
// treating the two alike is exactly how the duplicate got posted.
func (b *boardCardHistory) ExistingCard(
	ctx context.Context,
	producerID string,
	dedupKey string,
) (string, error) {
	url := b.baseURL + AttentionHistoryPath + "?limit=" + strconv.Itoa(CardHistoryLimit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "build attention history request failed")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "read attention history failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", errors.Errorf(ctx, "attention history answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", errors.Wrapf(ctx, err, "read attention history body failed")
	}
	items, err := decodeHistoryItems(ctx, body)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "decode attention history failed")
	}
	for _, item := range items {
		if item.ProducerID == producerID && item.DedupKey == dedupKey {
			return item.ItemID, nil
		}
	}
	return "", nil
}

// decodeHistoryItems accepts the route's bare JSON array, and also tolerates an
// `{"items":[...]}` envelope.
func decodeHistoryItems(ctx context.Context, body []byte) ([]BoardItem, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []BoardItem
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, errors.Wrapf(ctx, err, "unmarshal bare array")
		}
		return items, nil
	}
	var envelope struct {
		Items []BoardItem `json:"items"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, errors.Wrapf(ctx, err, "unmarshal items envelope")
	}
	return envelope.Items, nil
}
