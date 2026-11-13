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
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
