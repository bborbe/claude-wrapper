// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/claude-wrapper/pkg/fleetsnapshot"
)

// FleetSnapshotPath is the store's path: `FLEET_SNAPSHOT_PATH` when set, else
// `~/.claude/state/fleet-snapshot.json`.
//
// The env override is the same one the Python honours, and it is load-bearing
// for testing: without it a replay would have to write the LIVE snapshot to seed
// a previous round.
func FleetSnapshotPath() string {
	if path := os.Getenv("FLEET_SNAPSHOT_PATH"); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "state", "fleet-snapshot.json")
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
// `prev` is the snapshot the caller LOADED. It is a value, not a pointer, so a
// caller that never loaded one passes the zero value and every counter starts at
// zero — correct for a fresh install, and the reason the load guard lives on
// Persist rather than here.
func FleetSnapshot(
	prepared Prepared,
	sweptAt string,
	prev fleetsnapshot.Snapshot,
) fleetsnapshot.Snapshot {
	out := fleetsnapshot.Snapshot{
		SweptAt:  sweptAt,
		Sessions: make(map[string]fleetsnapshot.Entry, len(prepared.Tasks)),
	}
	for _, task := range prepared.Tasks {
		for _, sid := range task.SessionIDs {
			if sid == "" {
				continue
			}
			next := fleetsnapshot.Entry{
				Name:      task.Name,
				Status:    prepared.Registry[sid].Status,
				TaskFile:  task.Name,
				TaskMtime: task.Mtime.Format(time.RFC3339),
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
