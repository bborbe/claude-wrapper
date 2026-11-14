// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"time"
)

// JobLine renders the one log line shape every job ported into this service
// emits: `job=<name> action=<detail>`, followed by any key/value pairs.
//
// It exists because the log is the only evidence a 24 h observation has. A job
// that writes nothing on a quiet tick is indistinguishable from a job that is
// not running, and a job that writes free-form prose cannot be counted per job.
// The `action=` value carries the detail that matters — the sid, the card id,
// the PR — so a probe can join the line to the effect the job had instead of
// trusting that a line was printed at all.
//
// ⚠️ A line is not evidence that the job worked. Each ported handler moves a
// store of its own, and the acceptance criteria read both the line and the
// store: a handler that only logs `action=noop` satisfies the line and nothing
// else.
func JobLine(name string, action string, kv ...string) string {
	var b strings.Builder
	b.WriteString("job=")
	b.WriteString(name)
	b.WriteString(" action=")
	b.WriteString(action)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(" ")
		b.WriteString(kv[i])
		b.WriteString("=")
		b.WriteString(kv[i+1])
	}
	return b.String()
}

// RunTick runs tick once immediately, then once per interval, until ctx is
// done. It returns ctx.Err() on shutdown, which a caller running it in its own
// goroutine is expected to ignore: a cancelled tick loop is how the daemon
// stops, not a fault.
//
// The first tick runs before the first wait, so a job's effect is observable at
// process start rather than one interval later — the same shape FlaggedTick.Run
// uses, and the reason a freshly restarted daemon does not sit silent for the
// full interval.
//
// ⚠️ The select is the only place ctx is read. A tick that blocks holds the
// loop, so each handler's Tick must bound its own work; this helper cannot
// cancel a tick already in flight.
func RunTick(ctx context.Context, interval time.Duration, tick func(context.Context)) error {
	return RunTickWithGap(ctx, interval, time.Now, nil, tick)
}

// NowFunc is the clock a tick loop reads. It is injected rather than called
// directly so a test can drive a gap without sleeping for one.
type NowFunc func() time.Time

// RunTickWithGap is RunTick plus a report for an iteration that began late.
//
// A time.Ticker coalesces. When a tick blocks past its interval, or the process
// is suspended, the next tick fires at once and the loop looks exactly as it
// does after an ordinary wait — so the loop holds no record of the wait it did
// not take. Counting ticks cannot reveal that either: a skipped tick is simply
// absent, which reads the same as a tick that had nothing to do. This measures
// the wall clock between iterations instead, and reports the gap rather than
// the tick.
//
// A start is late when it is more than half an interval past the previous one.
// That tolerance is not jitter — the ticker delivers within scheduler noise of
// the cadence, so a start that far out is a wait the loop did not take.
//
// onLate is never called for the first tick: there is no previous tick to
// measure from. A nil onLate keeps the old behaviour, which is what RunTick
// passes.
func RunTickWithGap(
	ctx context.Context,
	interval time.Duration,
	now NowFunc,
	onLate func(gap time.Duration, since time.Time),
	tick func(context.Context),
) error {
	if now == nil {
		now = time.Now
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last time.Time
	for {
		started := now()
		if !last.IsZero() {
			if gap := started.Sub(last); gap > interval+interval/2 && onLate != nil {
				onLate(gap, last)
			}
		}
		last = started
		tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// expandedDir expands a leading `~` in a default store path.
//
// ⚠️ `filepath.Join("~/.claude/state", "x")` does NOT expand the tilde — it
// produces the relative path `~/.claude/state/x`, which creates a directory
// literally named `~` under the process's working directory. Every default path
// in this package is written with `~/`, so every one goes through here.
//
// It delegates to the package's existing expandHome (permissions_log.go) rather
// than a second implementation, and falls back to the raw path when the home
// directory cannot be resolved: a job writing to the wrong place is bad, but a
// job panicking on start is worse.
func expandedDir(dir string) string {
	expanded, err := expandHome(dir)
	if err != nil {
		return dir
	}
	return expanded
}
