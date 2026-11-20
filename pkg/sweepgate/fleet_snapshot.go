// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg/fleetsnapshot"
)

// FleetSnapshotPath is the store's path: `FLEET_SNAPSHOT_PATH` when set, else
// `~/.claude/state/fleet-snapshot.json`.
//
// The env override is the same one the Python honours, and it is load-bearing
// for testing: without it a replay would have to write the LIVE snapshot to seed
// a previous round.
//
// An empty path means the home directory could not be resolved. The caller names
// that on stderr rather than skipping silently — a store that quietly stops being
// written reads exactly like a fleet with nothing to persist.
func FleetSnapshotPath() (string, error) {
	if path := os.Getenv("FLEET_SNAPSHOT_PATH"); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(context.Background(), err,
			"resolve home dir for the fleet snapshot path failed")
	}
	return filepath.Join(home, ".claude", "state", "fleet-snapshot.json"), nil
}

// FleetSnapshot projects the prepared tracked set onto the SESSION-keyed fleet
// snapshot the round diffs against.
//
// The two shapes are different questions and both are needed: `Prepared` is
// task-keyed (one row per tracked task, which is what the frame renders), while
// the fleet snapshot is session-keyed (one row per session, which is what
// `stall_count` accumulates against across rounds). A task carrying several
// session ids contributes one entry per id, which is why the projection is a
// fan-out rather than a rename.
//
// ⚠️ **This MERGES into `prev`, it does not replace it.** The store is
// fleet-wide, but `Tick` runs once per configured SUBJECT — so a replace would
// leave only the last subject's sessions in the file, and `stall_count` could
// never accumulate past 1: each subject's tick would erase the others' counters
// before the next round read them. Carrying `prev` forward is what makes the
// store fleet-wide when the writer is per-subject.
//
// `prev` is the snapshot the caller LOADED. A caller that never loaded one
// passes the zero value and the merge is a no-op — correct for a fresh install,
// and the reason the load guard lives on Persist rather than here.
func FleetSnapshot(
	prepared Prepared,
	sweptAt string,
	prev fleetsnapshot.Snapshot,
) fleetsnapshot.Snapshot {
	out := fleetsnapshot.Snapshot{
		SweptAt:  sweptAt,
		Sessions: make(map[string]fleetsnapshot.Entry, len(prev.Sessions)+len(prepared.Tasks)),
	}
	// Every other subject's sessions survive this tick untouched. Their counters
	// are advanced only by their own subject's tick, which is what keeps one
	// increment per round.
	//
	// ⚠️ **A carried entry whose session is no longer busy has its counter RESET.**
	// The tick is now the only writer and never drops a row, so without this a
	// finished session keeps its last `stall_count` forever and reads as stalled
	// indefinitely — the count is a fact about a session that is no longer doing
	// anything. A still-busy entry is left alone: its own subject's tick is what
	// advances it, and incrementing here would count the same round twice.
	for id, entry := range prev.Sessions {
		carried := entry
		if status := prepared.Registry[id].Status; status != "busy" && status != "shell" {
			carried.Status = status
			carried.StallCount = 0
		}
		out.Sessions[id] = carried
	}
	for _, task := range prepared.Tasks {
		for _, sid := range task.SessionIDs {
			if sid == "" {
				continue
			}
			next := fleetsnapshot.Entry{
				Name:     task.Name,
				Status:   prepared.Registry[sid].Status,
				TaskFile: task.Name,
				// RFC3339Nano, not RFC3339: a task file rewritten twice inside one
				// second must still read as ADVANCED, or the counter increments on
				// a session that is moving.
				TaskMtime: task.Mtime.Format(time.RFC3339Nano),
			}
			next.StallCount = nextStallCount(prev.Sessions[sid], next)
			out.Sessions[sid] = next
		}
	}
	return out
}

// nextStallCount is the cross-round counter the round's `stalled` verdict reads.
//
// It counts CONSECUTIVE sweeps in which the session was busy and its task file
// did not move. Every other case resets to 0 — a session that is not busy cannot
// be stalled, and a status change or an advancing mtime is evidence it moved.
func nextStallCount(prev, next fleetsnapshot.Entry) int {
	if next.Status != "busy" && next.Status != "shell" {
		return 0
	}
	if prev.Status != next.Status {
		return 0
	}
	if prev.TaskMtime != next.TaskMtime {
		return 0
	}
	return prev.StallCount + 1
}
