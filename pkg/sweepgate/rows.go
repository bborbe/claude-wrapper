// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"time"

	"github.com/bborbe/errors"
)

// Row is one tracked task's classified state, for a caller that wants DATA
// rather than a rendered frame.
//
// It exists so the fleet round can read the sweep's verdicts instead of
// re-deriving them from the same inputs — the defect that let a live session
// read UNKNOWN and a working worker read stalled. Before this, the only way to
// get a bucket out of the gate was to render the table and parse it, which no
// consumer did.
type Row struct {
	// Name is the task name, the row's identity in the vault.
	Name string `json:"name"`
	// Session is the session the task is anchored to, empty when none.
	Session string `json:"session"`
	// Bucket is the bucket KEY — `progressing`, `waiting`, `hold`, `done` and so
	// on. It is the stable machine-readable half; Label is for humans.
	Bucket string `json:"bucket"`
	// Label is the rendered status cell, glyph included.
	Label string `json:"label"`
	// Status and Phase are the task's own frontmatter values, carried so a
	// consumer can tell why a row landed where it did without a second read.
	Status string `json:"status"`
	Phase  string `json:"phase"`
}

// ClassifiedRows resolves the subject's tracked set and returns one row per
// task, classified.
//
// ⚠️ **It classifies through Bucket, the SAME function the rendered frame uses,
// and that is the point rather than a convenience.** Classify alone is the
// ordinary precedence *before* the overlays: it would report a held session with
// whatever bucket its status produced and a deferred one with its original
// label. A consumer reading those would disagree with the frame an operator
// reads — two answers to one question, which is the class of defect this whole
// change exists to remove. Going through Bucket means the data and the frame
// cannot drift without a test failing.
//
// A prepare failure returns the error and no rows. An empty slice would read as
// "nothing is tracked", which is the direction that hides a whole subject.
func ClassifiedRows(ctx context.Context, cfg Config) ([]Row, error) {
	prepared, err := Prepare(ctx, cfg)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "prepare sweep rows failed")
	}
	return rowsFrom(cfg.Now.Now().Time(), prepared), nil
}

// rowsFrom classifies an already-prepared set.
//
// Split out from ClassifiedRows so the classification can be asserted without a
// vault: the invariant that matters is that rows go through Bucket and therefore
// carry the hold and defer overlays, and that is testable from a Prepared alone.
func rowsFrom(now time.Time, prepared Prepared) []Row {
	rows := make([]Row, 0, len(prepared.Tasks))
	for _, task := range prepared.Tasks {
		label, key := Bucket(now, prepared.Holds, task)
		rows = append(rows, Row{
			Name:    task.Name,
			Session: task.Session,
			Bucket:  key,
			Label:   label,
			Status:  task.Status,
			Phase:   task.Phase,
		})
	}
	return rows
}
