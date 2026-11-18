// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"fmt"
	"io"
)

// CardFunc builds the card payload for a tick, or nil when the invariant does
// not fire.
type CardFunc func(ctx context.Context, prepared Prepared) ([]byte, error)

// Tick runs one tick and writes what the reference writes.
//
// stdout is a Monitor's event stream: a no-change tick prints nothing there. The
// tick file is written on EVERY tick, including a no-change one, because it is
// the operator-facing table. Every failure path writes to stderr only and never
// emits a CHANGE or VERDICT line.
func Tick(
	ctx context.Context,
	cfg Config,
	stdout, stderr io.Writer,
	card CardFunc,
) int {
	if ctx.Err() != nil {
		return ExitFailed
	}
	prepared, err := Prepare(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: subject unreadable/unparseable: %v\n", err)
		return ExitFailed
	}
	if ctx.Err() != nil {
		return ExitFailed
	}

	// The basis is announced on EVERY tick, armed or not: a correct run that
	// prints nothing is uncitable.
	fmt.Fprintf(stderr, "park: %s\n", prepared.ParkBasis)

	statePath, err := StatePath(ctx, cfg, cfg.Subject)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: subject unreadable/unparseable: %v\n", err)
		return ExitFailed
	}
	stored, failReason, _ := LoadStored(ctx, statePath)
	changed := failReason != "" || stored != prepared.Digest

	if card != nil {
		tickCard(ctx, cfg, prepared, card, stderr)
	}

	frame, err := Frame(ctx, cfg, prepared, changed)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: could not render frame (%v)\n", err)
		return ExitFailed
	}

	tickPath, err := TickPath(ctx, cfg, cfg.Subject)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: could not write tick file (%v)\n", err)
		return ExitFailed
	}
	// The tick file is written even on a no-change tick: the file IS the table.
	if err := WriteTickFile(ctx, tickPath, frame); err != nil {
		fmt.Fprintf(stderr, "fail-open: could not write tick file (%v)\n", err)
		return ExitFailed
	}

	if changed {
		fmt.Fprintln(stdout, frame)
		reason := failReason
		if reason == "" {
			reason = "digest differs"
		}
		fmt.Fprintf(stdout, "VERDICT change (%s)\n", reason)
	}

	state := State{
		Topic:      cfg.Subject,
		Digest:     prepared.Digest,
		Members:    len(prepared.Snapshot.Members),
		BusySince:  prepared.Busy,
		RecordedAt: cfg.Now.Now().Time().Format("2006-01-02T15:04:05-07:00"),
	}
	if err := SaveStored(ctx, statePath, state); err != nil {
		// Never ExitChange here: that would send the host down a change-wake
		// path for a tick that rendered nothing.
		fmt.Fprintf(stderr, "fail-open: could not record state (%v)\n", err)
		return ExitFailed
	}

	if changed {
		return ExitChange
	}
	return ExitNoChange
}

// tickCard writes the card payload, or removes the file when the payload is nil.
//
// A failed side-channel write is named on stderr and never read as a verdict;
// the tick itself still completes.
func tickCard(
	ctx context.Context,
	cfg Config,
	prepared Prepared,
	card CardFunc,
	stderr io.Writer,
) {
	payload, err := card(ctx, prepared)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: could not write card file (%v)\n", err)
		return
	}
	path, err := CardPath(ctx, cfg, cfg.Subject)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: could not write card file (%v)\n", err)
		return
	}
	if err := WriteCardFile(ctx, path, payload); err != nil {
		fmt.Fprintf(stderr, "fail-open: could not write card file (%v)\n", err)
	}
}
