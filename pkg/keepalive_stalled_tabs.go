// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// StalledTab is one stalled tab worker's reading, already resolved by the caller.
//
// ⚠️ **The verdict words are `scripts/stalled-tab-restart.py`'s own**, not this
// package's: `restart`, `carded`, `capped`, `held`, `skipped`, `cannot-tell`.
// The step's table maps each to "what you do", and every one of them is
// report-and-hand-over — the leg never restarts anything.
type StalledTab struct {
	// SessionID is the stalled session the snapshot row named.
	SessionID string
	// Task is the task page the row carried, used as the card's subject.
	Task string
	// Verdict is the script's first output line for this row.
	Verdict string
	// Token is the script's line 2 for a `skipped` verdict — the refusal word.
	Token string
}

// StalledTabsProcedure REPORTS stalled tab workers and hands their restart to the
// operator.
//
// ⚠️ **It restarts nothing, and that is the source's own ruling.** The operator
// ruled 2026-11-12 that `--allow-busy-lapse` stays their own judgement, so the
// script's answered path is the only call site that ever passes it — reached from
// an operator's `ANSWERED:`, never from this tick. `restart-worker.py` refuses
// `busy-target` and a stalled session is `busy` by definition, so the only lift is
// a flag this leg must never pass.
//
// ⚠️ **`--dry-run` is mandatory on this path, not caution.** Without it the script
// acts on any row reading `idle` at call time, which makes the forbidden
// unattended restart reachable through the snapshot-to-call window.
//
// This replaces `WakeMissProcedure`: `commands/steps/keepalive.md` enumerates the
// eighth procedure as the stalled-tab worker, and the operator confirmed that
// reading on 2026-11-20 (attention card 8537f401).
type StalledTabsProcedure struct {
	// Rows reads the stalled rows and their verdicts.
	Rows func(ctx context.Context) ([]StalledTab, error)
}

// Name is the `action=` value this procedure writes.
func (p *StalledTabsProcedure) Name() string { return "stalled-tabs" }

// Run reports the first stalled row, so the line names a session rather than a
// count, and maps the script's verdict onto this tick's closed effect set.
func (p *StalledTabsProcedure) Run(ctx context.Context) (KeepaliveEffect, error) {
	if p.Rows == nil {
		return KeepaliveEffect{}, errors.New(ctx, "stalled-tabs has no row source")
	}
	rows, err := p.Rows(ctx)
	if err != nil {
		return KeepaliveEffect{}, errors.Wrap(ctx, err, "read stalled tabs failed")
	}
	if len(rows) == 0 {
		return KeepaliveEffect{Result: KeepaliveResultNone}, nil
	}
	return StalledTabEffect(rows[0]), nil
}

// StalledTabEffect maps one stalled row's script verdict onto the tick's closed
// effect set.
//
// ⚠️ **`restart` and `carded` are BOTH `kept`.** `restart` means the operator
// answered and the real restart already ran — the tick did not do it, so claiming
// `restarted` would attribute an act this leg never performed. `carded` means a
// card is posted or already open, which is likewise a report.
func StalledTabEffect(row StalledTab) KeepaliveEffect {
	effect := KeepaliveEffect{SessionID: row.SessionID}
	switch row.Verdict {
	case "restart":
		effect.Result = KeepaliveResultKept
		effect.Reason = "handed-over"
	case "carded":
		effect.Result = KeepaliveResultKept
		effect.Reason = "carded"
	case "capped":
		effect.Result = KeepaliveResultCapped
	case "held":
		effect.Result = KeepaliveResultSkipped
		effect.Reason = "hold"
	case "skipped":
		effect.Result = KeepaliveResultSkipped
		effect.Reason = row.Token
		if effect.Reason == "" {
			effect.Reason = "skipped"
		}
	case "cannot-tell":
		effect.Result = KeepaliveResultSkipped
		effect.Reason = "cannot-tell"
	default:
		// A word this tick does not know is a procedure/script drift, never a
		// quiet fleet: report it as undecidable rather than defaulting to `kept`.
		effect.Result = KeepaliveResultSkipped
		effect.Reason = "cannot-tell"
	}
	return effect
}

// ParseStalledTabRows reads the step's per-row output: `<sid8>\t<verdict>[\t<token>]`.
//
// ⚠️ The session id printed is TRUNCATED to 8 characters by the step's own driver,
// so this parser keeps it as printed — a caller that needs the full id must take it
// from the snapshot row, not from this output.
func ParseStalledTabRows(ctx context.Context, output string) ([]StalledTab, error) {
	rows := []StalledTab{}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 {
			continue
		}
		verdict := fields[1]
		if !isStalledVerdict(verdict) {
			continue
		}
		row := StalledTab{SessionID: fields[0], Verdict: verdict}
		if len(fields) > 2 {
			row.Token = fields[2]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// isStalledVerdict reports whether word is one of the script's six verdicts.
func isStalledVerdict(word string) bool {
	switch word {
	case "restart", "carded", "capped", "held", "skipped", "cannot-tell":
		return true
	default:
		return false
	}
}
