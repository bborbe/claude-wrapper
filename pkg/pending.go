// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sync"

	"github.com/bborbe/errors"
)

// Answer is a decision for a pending request, discriminated by the kind of the
// request it answers.
//
// Behavior carries the permission verdict; Answers carries a question's
// selection, keyed by the question text. Which field a caller fills is decided
// by the pending request's kind, and a mismatch is refused rather than coerced.
type Answer struct {
	Kind     AnswerKind
	Behavior string
	Answers  map[string]string
}

// PendingAnswer is the registry's read view of one request: what the child is
// parked on, and the decision once one has been recorded.
type PendingAnswer struct {
	Question Question
	Answer   Answer
	Answered bool
}

// Pending holds the requests a child has raised and is blocked on.
//
// It lives in the wrapper process, not in the MCP tool process: the tool is a
// child of claude, so it reaches this registry over the wrapper's own HTTP
// surface. That indirection is why the question is visible to a REST caller at
// all — the tool is the only thing claude talks to, and it forwards.
type Pending interface {
	Register(requestID string, question Question)
	Resolve(ctx context.Context, requestID string, answer Answer) error
	Peek(requestID string) (PendingAnswer, error)
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
	question Question
	answer   Answer
	answered bool
}

// Register records a request with its payload. Registering an id twice is a
// no-op so a retried tool call cannot reset a decision already made.
func (p *pending) Register(requestID string, question Question) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if _, ok := p.waiters[requestID]; ok {
		return
	}
	question.RequestID = requestID
	p.waiters[requestID] = &waiter{question: question}
}

// Resolve records an answer for a request.
//
// An answer whose kind does not match the pending request is refused with
// ErrAnswerShapeMismatch and leaves the request unanswered: recording it would
// report success for a decision the child can never consume.
func (p *pending) Resolve(ctx context.Context, requestID string, answer Answer) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	entry, ok := p.waiters[requestID]
	if !ok {
		return errors.Wrapf(ctx, ErrAnswerUnsupported, "no pending request %q", requestID)
	}
	if entry.answered {
		return errors.Wrapf(ctx, ErrAnswerUnsupported, "request %q already answered", requestID)
	}
	if entry.question.Kind != answer.Kind {
		return errors.Wrapf(
			ctx,
			ErrAnswerShapeMismatch,
			"answer kind %q does not match pending kind %q for request %q",
			answer.Kind,
			entry.question.Kind,
			requestID,
		)
	}
	entry.answer = answer
	entry.answered = true
	return nil
}

// Peek reports the pending request and whether it has been answered, without
// blocking.
//
// Non-blocking on purpose: the MCP tool polls this over HTTP, and a blocking
// long-poll would tie up a server goroutine per pending prompt.
func (p *pending) Peek(requestID string) (PendingAnswer, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	entry, ok := p.waiters[requestID]
	if !ok {
		return PendingAnswer{}, nil
	}
	return PendingAnswer{
		Question: entry.question,
		Answer:   entry.answer,
		Answered: entry.answered,
	}, nil
}

// Forget drops a request without answering it.
func (p *pending) Forget(requestID string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	delete(p.waiters, requestID)
}
