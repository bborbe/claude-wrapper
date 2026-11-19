// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"time"

	"github.com/bborbe/errors"
)

// reserve claims id for one resume spawn, refusing when a live child or another
// in-flight spawn already holds it. The claim is held across startChild, which
// runs outside the lock, so two concurrent resumes of one conversation cannot
// both pass the check. A holder that is provably stale — its recorded child is
// gone and the pid was reused — is released first, so a dead session is
// resumable rather than refused on a holder nobody holds.
func (s *supervisor) reserve(ctx context.Context, id string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.starting == nil {
		s.starting = map[string]struct{}{}
	}
	if _, busy := s.starting[id]; busy {
		return errors.Wrapf(ctx, ErrSessionLive, "session %q is already being resumed", id)
	}
	if c, ok := s.sessions[id]; ok && !c.isExited() {
		if !s.releaseIfProvablyStale(c) {
			return errors.Wrapf(ctx, ErrSessionLive, "session %q is still running", id)
		}
	}
	s.starting[id] = struct{}{}
	return nil
}

func (s *supervisor) release(id string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	delete(s.starting, id)
}

// processStartTolerance absorbs the rounding between the source that recorded a
// start time and the one that re-reads it. A genuine reuse differs by at least
// the dead child's lifetime, so the tolerance never hides one.
const processStartTolerance = 2 * time.Second

// pidReused reports whether the process now at pid is provably not the child
// whose start time was recorded. An unknown time on either side never proves
// reuse: the caller keeps holding, because releasing a live child is the
// two-writers corruption this guard exists to prevent.
func pidReused(recorded, current time.Time, currentOK bool) bool {
	if !currentOK || recorded.IsZero() || current.IsZero() {
		return false
	}
	d := current.Sub(recorded)
	if d < 0 {
		d = -d
	}
	return d > processStartTolerance
}

// recordedChildStart is the start time the record proves for its child. A record
// written before ChildStartedAt existed falls back to StartedAt: the child was
// started immediately before the record was written, so its own start time is at
// or just before StartedAt — and a reused pid's process starts after it.
func recordedChildStart(record SessionRecord) time.Time {
	if !record.ChildStartedAt.IsZero() {
		return record.ChildStartedAt
	}
	return record.StartedAt
}

// adoptedHolderProvablyStale reports whether this child is an adopted one whose
// recorded process is gone even though its pid is alive — the kernel reused the
// number. A spawned child is settled by cmd.Wait() and never needs this check;
// only an adopted child's liveness rests on the pid alone.
func (c *child) adoptedHolderProvablyStale() bool {
	c.mutex.RLock()
	adopted := c.cmd == nil
	exited := c.session.State == StateExited
	pid := c.pid
	recorded := c.procStart
	c.mutex.RUnlock()
	if !adopted || exited || pid <= 0 {
		return false
	}
	current, ok := procStartTime(pid)
	return pidReused(recorded, current, ok)
}

// newSessionRecord builds the durable record for a freshly spawned child, and
// returns the child's own start time so the in-memory child carries the same
// identity the record proves.
func (s *supervisor) newSessionRecord(id string, pid int, options SpawnOptions) (SessionRecord, time.Time) {
	startedAt := s.now.Now().Time().UTC()
	childStart, _ := procStartTime(pid)
	return SessionRecord{
		ID:             id,
		ChildPID:       pid,
		Cwd:            options.Cwd,
		Options:        options,
		StartedAt:      startedAt,
		ChildStartedAt: childStart,
	}, childStart
}

// releaseIfProvablyStale settles c and reports true when c is an adopted holder
// whose recorded child is provably gone — the pid was reused by an unrelated
// process. It reports false, leaving c untouched, when the holder is or may
// still be live.
func (s *supervisor) releaseIfProvablyStale(c *child) bool {
	if !c.adoptedHolderProvablyStale() {
		return false
	}
	c.reap(nil)
	return true
}

// staleRecordStart reads the live pid's start time for record and reports whether
// it proves the recorded child is gone (the pid was reused). When stale, the
// record is removed and the caller skips it.
func (s *supervisor) staleRecordStart(ctx context.Context, record SessionRecord) (time.Time, bool) {
	current, ok := procStartTime(record.ChildPID)
	if !pidReused(recordedChildStart(record), current, ok) {
		return current, false
	}
	if removeErr := s.store.Remove(ctx, record.ID); removeErr != nil {
		log.Printf("WARNING: prune stale session %s failed: %v", record.ID, removeErr)
	}
	return current, true
}
