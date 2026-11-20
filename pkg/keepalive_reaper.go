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

// ReaperJobName is the `job=` value every reaper line carries.
const ReaperJobName = "reaper"

// Reaper is one kind of session the ReaperJob closes. Each existing behaviour
// (the idle close, the terminal-task close, the manager reap, the drive reap, the
// stood-down close) becomes a Reaper that calls that existing code UNCHANGED —
// the merge is an adapter, not a rewrite.
type Reaper interface {
	// Kind is the `action=` value: `manager-reap`, `drive-reap`, `stood-down-close`,
	// `terminal-task` or `idle-close`.
	Kind() string
	// Reap examines its population and returns one result per session it closed or
	// examined-and-refused. A `kept` result is emitted ONLY for a candidate it
	// EXAMINED and refused (never for every live session, which would explode the log).
	Reap(ctx context.Context) ([]ReaperResult, error)
}

// ReaperResult is one reaper's verdict for one session.
type ReaperResult struct {
	// Kind is the `action=` value the line carries (copied from the Reaper).
	Kind string
	// SessionID is the session the verdict is about.
	SessionID string
	// Closed is true when the reaper closed the session, false when it examined and
	// left it (`kept`).
	Closed bool
	// Reason is the refusal reason when Closed is false.
	Reason string
}

// ReaperJob runs every reaper once per tick and logs one line per result.
//
// It is the thin wrapper around the Reaper set — one interval, one registration
// point, one log line format — the same shape as KeepaliveJob.
type ReaperJob struct {
	// Reapers are the five close kinds.
	Reapers []Reaper
	// Interval is how often the tick runs. Zero means DefaultKeepaliveInterval (15m).
	Interval time.Duration
	// Logf writes the job's lines. Nil means log.Printf.
	Logf func(line string)
}

// Run ticks until ctx is done.
func (j *ReaperJob) Run(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultKeepaliveInterval
	}
	return RunTick(ctx, interval, j.TickAndLog)
}

// TickAndLog runs every reaper once and writes one line per result.
//
// ⚠️ A reaper's error is logged and the tick CONTINUES — the five kinds are
// independent, so one broken store must not stop the other four.
func (j *ReaperJob) TickAndLog(ctx context.Context) {
	for _, reaper := range j.Reapers {
		if reaper == nil {
			continue
		}
		results, err := reaper.Reap(ctx)
		if err != nil {
			j.logf(JobLine(ReaperJobName, reaper.Kind(), "result", "error", "reason", err.Error()))
			continue
		}
		for _, result := range results {
			j.logf(reaperLine(result))
		}
	}
}

// reaperLine renders one result as `job=reaper action=<kind> result=closed|kept id=<sid>`.
func reaperLine(result ReaperResult) string {
	kv := []string{"result", reaperResultWord(result)}
	if result.SessionID != "" {
		kv = append(kv, "id", result.SessionID)
	}
	if !result.Closed && result.Reason != "" {
		kv = append(kv, "reason", result.Reason)
	}
	return JobLine(ReaperJobName, result.Kind, kv...)
}

// reaperResultWord maps a result to its `result=` word.
func reaperResultWord(result ReaperResult) string {
	if result.Closed {
		return string(KeepaliveResultClosed)
	}
	return string(KeepaliveResultKept)
}

// Tick runs every reaper once and returns all results (the test / injection form).
func (j *ReaperJob) Tick(ctx context.Context) ([]ReaperResult, error) {
	if len(j.Reapers) == 0 {
		return nil, errors.New(ctx, "reaper has no reapers configured")
	}
	all := []ReaperResult{}
	for _, reaper := range j.Reapers {
		if reaper == nil {
			continue
		}
		results, err := reaper.Reap(ctx)
		if err != nil {
			return all, errors.Wrapf(ctx, err, "reaper %s failed", reaper.Kind())
		}
		all = append(all, results...)
	}
	return all, nil
}

func (j *ReaperJob) logf(line string) {
	if j.Logf != nil {
		j.Logf(line)
		return
	}
	log.Printf("%s", line)
}
