// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/bborbe/claude-wrapper/pkg/fleetsnapshot"
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

	// The rows file is the SAME classification as data, written beside the frame
	// so a reader can serve it without re-running Prepare — that scan is minutes.
	// A failure here does not fail the tick: the frame is the primary artifact
	// and the rows are an optimisation, so this degrades to "no cache" rather
	// than to a subject the operator cannot read.
	if rowsPath, err := RowsPath(ctx, cfg, cfg.Subject); err == nil {
		if data, err := json.Marshal(rowsFrom(cfg.Now.Now().Time(), prepared)); err == nil {
			if err := writeFileAtomic(ctx, rowsPath, data); err != nil {
				fmt.Fprintf(stderr, "sweep-gate: could not write rows file (%v)\n", err)
			}
		}
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
		Topic:  cfg.Subject,
		Digest: prepared.Digest,
		// len(Tasks), not len(Members): the reference writes the TRACKED task
		// count here (`sweep-gate.py:1839` `"members": len(tracked)`). The field
		// name is misleading but the state file is shared with the Python that
		// owns its format, so a different number is a silent format divergence.
		Members:    len(prepared.Tasks),
		BusySince:  prepared.Busy,
		RecordedAt: cfg.Now.Now().Time().Format("2006-01-02T15:04:05-07:00"),
	}
	if err := SaveStored(ctx, statePath, state); err != nil {
		// Never ExitChange here: that would send the host down a change-wake
		// path for a tick that rendered nothing.
		fmt.Fprintf(stderr, "fail-open: could not record state (%v)\n", err)
		return ExitFailed
	}

	persistFleetSnapshot(ctx, cfg, prepared, stderr)
	if changed {
		return ExitChange
	}
	return ExitNoChange
}

// persistFleetSnapshot writes the round's previous-sweep baseline — the store
// `stall_count` accumulates against — taken over from the sweep reader.
//
// It runs LAST, after the frame and the rows, because it is the sweep's final
// write and must describe the same tick the operator just saw.
//
// ⚠️ A failed LOAD is passed through to Persist, which refuses: overwriting a
// baseline the round could not read would reset every counter on the next round
// and make every session read first-seen. A failed WRITE is named on stderr and
// never fails the tick — the frame is the primary artifact.
func persistFleetSnapshot(ctx context.Context, cfg Config, prepared Prepared, stderr io.Writer) {
	snapPath, err := FleetSnapshotPath()
	if err != nil {
		fmt.Fprintf(stderr, "sweep-gate: fleet snapshot skipped (%v)\n", err)
		return
	}
	prev, loadErr := fleetsnapshot.Load(ctx, snapPath)
	snap := FleetSnapshot(prepared, cfg.Now.Now().Time().Format(time.RFC3339), prev)
	if err := fleetsnapshot.Persist(ctx, snapPath, snap, loadErr); err != nil {
		fmt.Fprintf(stderr, "sweep-gate: could not persist fleet snapshot (%v)\n", err)
	}
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
