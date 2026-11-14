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
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// AttentionPath is attention-controller's create-card route.
const AttentionPath = "/api/1.0/attention"

// AttentionPostRequest is one condition card the watchdog posts.
//
// It carries the fields the store's schema requires of a producer that asks
// nothing: `producer_kind: cron` and `answer_mechanism: ack` say "this is a
// report, not a question", and `liveness_ref` names the owner so the store's
// prune rule does not treat the card as an abandoned ask.
type AttentionPostRequest struct {
	ProducerID      string
	ProducerKind    string
	LivenessRef     string
	DedupKey        string
	InterruptClass  string
	Payload         string
	AnswerMechanism string
}

// AttentionPoster posts a condition card and returns the store's item id.
type AttentionPoster interface {
	Post(ctx context.Context, request AttentionPostRequest) (string, error)
}

// NewAttentionPoster creates a poster over the attention store.
func NewAttentionPoster(baseURL string, client *http.Client) AttentionPoster {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &attentionPoster{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

type attentionPoster struct {
	baseURL string
	client  *http.Client
}

// Post creates one card and returns its item id.
func (p *attentionPoster) Post(
	ctx context.Context,
	request AttentionPostRequest,
) (string, error) {
	body := map[string]string{
		"producer_id":      request.ProducerID,
		"producer_kind":    request.ProducerKind,
		"liveness_ref":     request.LivenessRef,
		"dedup_key":        request.DedupKey,
		"interrupt_class":  request.InterruptClass,
		"payload":          request.Payload,
		"answer_mechanism": request.AnswerMechanism,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "marshal attention card failed")
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.baseURL+AttentionPath, bytes.NewReader(encoded),
	)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "build attention card request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "post attention card failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", errors.Errorf(ctx, "attention store answered %d", resp.StatusCode)
	}
	var item struct {
		ItemID string `json:"item_id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&item); err != nil {
		return "", errors.Wrapf(ctx, err, "decode attention card response failed")
	}
	if item.ItemID == "" {
		return "", errors.Errorf(ctx, "attention store returned no item id")
	}
	return item.ItemID, nil
}

// OpenCardReader answers "which sessions have an open attention card naming
// them".
//
// An open card means the session is waiting on the operator, and a wake would
// either answer that question or bury it — so the card is checked BEFORE the
// pane guard, exactly as `pane-wake.py` orders it.
type OpenCardReader interface {
	// OpenSessionIDs returns the producer ids of every open card.
	//
	// An error is a store that could not be read; the caller must fail closed and
	// skip every session rather than assume no card exists.
	OpenSessionIDs(ctx context.Context) (map[string]struct{}, error)
}

// NewBoardOpenCards creates an OpenCardReader over the attention store.
func NewBoardOpenCards(store BoardStore) OpenCardReader {
	return &boardOpenCards{store: store}
}

type boardOpenCards struct {
	store BoardStore
}

// OpenSessionIDs reads the open items and keeps their producer ids.
func (b *boardOpenCards) OpenSessionIDs(ctx context.Context) (map[string]struct{}, error) {
	items, err := b.store.Open(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ProducerID == "" {
			continue
		}
		ids[item.ProducerID] = struct{}{}
	}
	return ids, nil
}
