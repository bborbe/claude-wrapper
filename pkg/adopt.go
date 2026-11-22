// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"

	"github.com/bborbe/errors"
)

// AdoptReport is what one re-adoption pass did: the children it took over and
// the records it left behind, each with the reason it was not taken.
//
// A bare count cannot tell a clean restart from one that dropped rows — a
// successor that logged only "re-adopted 34" left the seven it pruned
// indistinguishable from seven that had already exited.
type AdoptReport struct {
	// Adopted is the id of every child this pass took over.
	Adopted []string
	// Skipped is one row per record this pass did not adopt.
	Skipped []AdoptSkip
}

// AdoptSkip names one record a re-adoption pass left behind.
type AdoptSkip struct {
	// ID is the record's own id. It is empty when the record carries none, in
	// which case Dir is the only name that identifies it.
	ID string
	// Dir is the store directory the record was read from.
	Dir string
	// Reason is why the record was not adopted.
	Reason string
}

// skip records one non-adoption and logs it, so the per-record line and the
// returned report can never disagree.
func (r *AdoptReport) skip(id, dir, reason string) {
	r.Skipped = append(r.Skipped, AdoptSkip{ID: id, Dir: dir, Reason: reason})
	log.Printf("job=adopt action=skip session=%q reason=%q dir=%s", id, reason, dir)
}

// SkippedSessions returns the name of every skipped record, falling back to the
// store directory where the record has no id. It is the summary line's payload.
func (r AdoptReport) SkippedSessions() []string {
	names := make([]string, 0, len(r.Skipped))
	for _, skipped := range r.Skipped {
		if skipped.ID != "" {
			names = append(names, skipped.ID)
			continue
		}
		names = append(names, skipped.Dir)
	}
	return names
}

// Adopt registers every child a previous daemon left running.
//
// It is what makes a restart cost a turn boundary rather than a turn. Each
// record names a pid and two endpoint paths; a pid that is still alive is a child
// that survived, and its state is rebuilt by replaying the event file from the
// beginning — state is derived from events, so a replay reconstructs exactly what
// the previous daemon reported, including a turn left open across the restart.
func (s *supervisor) Adopt(ctx context.Context) (AdoptReport, error) {
	report := AdoptReport{}
	records, err := s.store.List(ctx)
	if err != nil {
		return report, errors.Wrapf(ctx, err, "list session records failed")
	}
	for _, record := range records {
		if record.ID == "" {
			// A record with no id cannot be named, and Dir("") is the sessions
			// root — deriving a path from it would delete the whole store. It is
			// reported by the directory it was read from and left untouched.
			report.skip("", record.Dir, "record has no id")
			continue
		}
		if !processAlive(record.ChildPID) {
			// The child left while no daemon was watching. Its record has nothing
			// left to serve — no stream, no stdin — so keeping it would make every
			// later list report a session that cannot be reached.
			if removeErr := s.store.Remove(ctx, record.ID); removeErr != nil {
				log.Printf("WARNING: prune session %s failed: %v", record.ID, removeErr)
			}
			report.skip(record.ID, record.Dir, "child pid is not alive")
			continue
		}
		currentStart, stale := s.staleRecordStart(ctx, record)
		if stale {
			report.skip(record.ID, record.Dir, "record start time does not match the live pid")
			continue
		}
		files, openErr := s.store.Open(ctx, record.ID)
		if openErr != nil {
			// Already named by a WARNING and not a silent path: the record stays
			// on disk for the next pass, so it is not counted as a skip.
			log.Printf("WARNING: adopt session %s failed: %v", record.ID, openErr)
			continue
		}
		// Re-resolved from the same options the spawn used, because an adopted child
		// parks on the same policy the spawning daemon applied. A zero PolicyContext
		// is not "no opinion" — it carries no rules, and the decider escalates
		// everything it cannot match, so every adopted park would wait for a manager
		// and then be denied by the timeout. That is the opposite of the in-process
		// behaviour, and it would apply to every session after every restart.
		policy, policyErr := s.resolvePolicy(ctx, record.Options)
		if policyErr != nil {
			// The child is already running. Refusing to adopt would orphan it, which
			// is the failure this path exists to remove, so it is adopted with no
			// rules and the loss is named — an empty rule set escalates rather than
			// silently allowing.
			log.Printf("WARNING: adopt session %s: policy unavailable: %v", record.ID, policyErr)
		}
		c := &child{
			id:           record.ID,
			pid:          record.ChildPID,
			procStart:    currentStart,
			now:          s.now,
			stdin:        files.Stdin,
			eventsPath:   files.EventsPath,
			subscribers:  map[int]chan Event{},
			tailDone:     make(chan struct{}),
			policy:       policy,
			ledger:       s.ledger,
			exitRecorder: s.exitRecorder,
			ctx:          s.ctx,
			taskResolver: s.policy.TaskResolver,
			openCards:    s.policy.OpenCards,
			session: Session{
				ID:       record.ID,
				State:    StateStarting,
				Stall:    &Stall{},
				Controls: []Control{ControlMessage},
				Cwd:      record.Cwd,
				// Mode is what a board answer is checked against: a permission gate
				// releases only for a headless producer. Leaving it empty made every
				// adopted headless worker look like a tab worker, so an answer that
				// arrived during the restart gap was refused — the exact case this
				// change exists to make deliverable.
				Mode:      sessionMode(record.Options.Mode),
				Role:      record.Options.Role,
				StartedAt: record.StartedAt,
			},
		}
		if record.Question != nil {
			// The tool that asked is a grandchild of the child, so it survived the
			// restart too and is still polling for its answer. Restoring the question
			// is what keeps that answer deliverable.
			question := *record.Question
			c.session.Question = &question
			c.session.Controls = []Control{ControlAnswer, ControlMessage}
			s.pending.Register(question.RequestID, question)
		}
		s.mutex.Lock()
		s.sessions[record.ID] = c
		s.mutex.Unlock()
		//nolint:gosec // the child outlives every request; there is no request ctx to inherit
		go c.tail()
		go c.watch()
		report.Adopted = append(report.Adopted, record.ID)
	}
	return report, nil
}
