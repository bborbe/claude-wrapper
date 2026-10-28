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
type Pending interface {
	Register(requestID string)
	Resolve(ctx context.Context, requestID string, behavior string) error
	Peek(requestID string) (string, bool, error)
	Forget(requestID string)
}

// NewPending creates an empty registry.
func NewPending() Pending {
	return &pending{waiters: map[string]*waiter{}}
}

type pending struct {
	mutex   sync.Mutex
	waiters map[string]*waiter
}

type waiter struct {
	behavior string
	answered bool
}

// Register records a request. Registering an id twice is a no-op so a retried
// tool call cannot reset a decision already made.
func (p *pending) Register(requestID string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if _, ok := p.waiters[requestID]; ok {
		return
	}
	p.waiters[requestID] = &waiter{}
}

// Resolve records an answer for a request.
func (p *pending) Resolve(ctx context.Context, requestID string, behavior string) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	entry, ok := p.waiters[requestID]
	if !ok {
		return errors.Wrapf(ctx, ErrAnswerUnsupported, "no pending request %q", requestID)
	}
	if entry.answered {
		return errors.Wrapf(ctx, ErrAnswerUnsupported, "request %q already answered", requestID)
	}
	entry.behavior = behavior
	entry.answered = true
	return nil
}

// Peek reports whether the request has been answered, without blocking.
//
// Non-blocking on purpose: the MCP tool polls this over HTTP, and a blocking
// long-poll would tie up a server goroutine per pending prompt.
func (p *pending) Peek(requestID string) (string, bool, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	entry, ok := p.waiters[requestID]
	if !ok {
		return "", false, nil
	}
	return entry.behavior, entry.answered, nil
}

// Forget drops a request without answering it.
func (p *pending) Forget(requestID string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	delete(p.waiters, requestID)
}
