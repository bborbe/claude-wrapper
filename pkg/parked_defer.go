// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/bborbe/errors"
)

// DeferWriter records a parked-until deadline as the row's own `defer_date`.
//
// ⚠️ WHY THE WRITE LIVES HERE AND NOT ON THE WORKER. The worker already declares
// the wait — its closer carries `⏰ Ends:` and `ClassifyParked` parses the clock
// out of it — and the tick already holds the row on that declaration. The only
// thing missing was that nothing ever wrote the field down, so the row stayed in
// the board's active view and the next tick reopened it to rediscover the same
// wait. Measured 2026-11-26 on `seibert-brogrammers` *Lower lib-k8s Default CPU
// Request to 10m*: 8 opens in 11 h, every one ending `turn closed with nothing
// pending`, with no `defer_date` on the row at any point.
//
// ⚠️ The field is the whole mechanism. `DefaultVaultAPITaskQuery` is
// `flag=true&status=in_progress&due=now`, so a future `defer_date` removes the
// row from the tick's own input; when the date passes the row comes back, flag
// intact, and pops up again. No new skip rule, no new eligibility branch — the
// write is the fix.
type DeferWriter interface {
	// WriteDefer sets the row's `defer_date` to deadline.
	WriteDefer(ctx context.Context, row FlaggedRow, deadline time.Time) error
}

// VaultCLIDeferWriter shells `vault-cli task defer`.
//
// ⚠️ Shelling out rather than writing frontmatter directly is the same choice
// `spawn_bind.go` makes for `claude_session_id`: vault-cli owns the ownership
// rule and the frontmatter shape, and a second writer in this repo would drift
// from it. It also means the deferred row is written by exactly the actor the
// operator would have used by hand.
type VaultCLIDeferWriter struct {
	Runner CommandRunner
}

// WriteDefer sets the row's `defer_date` through vault-cli.
func (w VaultCLIDeferWriter) WriteDefer(
	ctx context.Context,
	row FlaggedRow,
	deadline time.Time,
) error {
	if w.Runner == nil {
		return errors.Errorf(ctx, "command runner is not configured")
	}
	if row.Vault == "" || row.Name == "" {
		return errors.Errorf(ctx, "row %q carries no vault or name to defer", row.Key())
	}
	// ⚠️ The full RFC3339 instant, never the date-only form: a `later (on <time>)`
	// declaration names a clock, and cutting it to a date would surface the row up
	// to a day early or late depending on the offset.
	_, err := w.Runner.Run(
		ctx,
		"vault-cli",
		"--vault", row.Vault,
		"task", "defer", row.Name, deadline.Format(time.RFC3339),
	)
	return errors.Wrapf(ctx, err, "defer row %s", row.Key())
}
