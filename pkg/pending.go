// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sync"

	"github.com/bborbe/errors"
)

// Pending holds the permission requests a child has raised and is blocked on.
//
// It lives in the wrapper process, not in the MCP tool process: the tool is a
// child of claude, so it reaches this registry over the wrapper's own HTTP
// surface. That indirection is why the question is visible to a REST caller at
// all — the tool is the only thing claude talks to, and it forwards.
type Pending struct {
	mutex   sync.Mutex
	waiters map[string]chan string
}

// NewPending creates an empty registry.
func NewPending() *Pending {
	return &Pending{waiters: map[string]chan string{}}
}

// Register records a question and returns the channel its answer will arrive on.
func (p *Pending) Register(requestID string) chan string {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if existing, ok := p.waiters[requestID]; ok {
		return existing
	}
	ch := make(chan string, 1)
	p.waiters[requestID] = ch
	return ch
}

// Resolve delivers an answer to the waiting tool call.
func (p *Pending) Resolve(ctx context.Context, requestID string, behavior string) error {
	p.mutex.Lock()
	ch, ok := p.waiters[requestID]
	delete(p.waiters, requestID)
	p.mutex.Unlock()
	if !ok {
		return errors.Wrapf(ctx, ErrAnswerUnsupported, "no pending request %q", requestID)
	}
	ch <- behavior
	return nil
}

// Wait blocks until the request is answered, the context ends, or the answer
// channel is closed. The caller decides what an unanswered request means.
func (p *Pending) Wait(ctx context.Context, requestID string) (string, error) {
	p.mutex.Lock()
	ch, ok := p.waiters[requestID]
	p.mutex.Unlock()
	if !ok {
		return "", errors.Wrapf(ctx, ErrAnswerUnsupported, "no pending request %q", requestID)
	}
	select {
	case behavior := <-ch:
		return behavior, nil
	case <-ctx.Done():
		return "", errors.Wrapf(ctx, ctx.Err(), "waiting for answer to %q", requestID)
	}
}

// Forget drops a request without answering it.
func (p *Pending) Forget(requestID string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	delete(p.waiters, requestID)
}
