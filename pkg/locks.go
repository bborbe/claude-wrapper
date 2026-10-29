// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sync"

	"github.com/bborbe/errors"
)

// SessionLocks serializes every writer that can put a turn on one child.
//
// A sequencer that held its own lock would still be racing the plain message
// route: a turn written between two steps has its result consumed as the
// current step's close, and the next step — /compact or /exit — fires one turn
// early. Every writer therefore takes the same lock, keyed by session.
type SessionLocks struct {
	mutex  sync.Mutex
	gauges map[string]chan struct{}
}

// NewSessionLocks creates a lock set.
func NewSessionLocks() *SessionLocks {
	return &SessionLocks{gauges: map[string]chan struct{}{}}
}

// Acquire blocks until the session's lock is held or ctx is done, and returns
// the release func.
//
// Acquisition is ctx-aware on purpose: a second request for a session whose
// run has hours to go must give up when its own client disconnects, rather
// than holding an HTTP connection open until the first run finishes.
func (l *SessionLocks) Acquire(ctx context.Context, sessionID string) (func(), error) {
	gauge := l.gaugeFor(sessionID)
	select {
	case <-ctx.Done():
		return nil, errors.Wrapf(ctx, ctx.Err(), "acquiring lock for session %q", sessionID)
	case gauge <- struct{}{}:
		return func() { <-gauge }, nil
	}
}

// TryAcquire takes the lock only if it is free.
//
// A route that must answer promptly — POST …/message — uses this instead of
// Acquire: blocking behind a sequence would hold its response open for the
// length of the run, past the server's write deadline, so the turn would be
// delivered with no way to report it.
func (l *SessionLocks) TryAcquire(sessionID string) (func(), bool) {
	gauge := l.gaugeFor(sessionID)
	select {
	case gauge <- struct{}{}:
		return func() { <-gauge }, true
	default:
		return nil, false
	}
}

// gaugeFor returns the one-slot channel that guards a session.
//
// Entries are never pruned. Deleting on release would let a waiter holding the
// old channel and a newcomer holding a fresh one both believe they hold the
// session, which is the race this type exists to remove; the map is bounded by
// the number of children the process has ever been asked about.
func (l *SessionLocks) gaugeFor(sessionID string) chan struct{} {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	gauge, ok := l.gauges[sessionID]
	if !ok {
		gauge = make(chan struct{}, 1)
		l.gauges[sessionID] = gauge
	}
	return gauge
}
