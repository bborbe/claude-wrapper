// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/bborbe/errors"
)

// ParkEventDue is `park-event.py due --json`'s top level.
//
// ⚠️ **An object, not a bare list, and the script says why:** *"a list cannot say
// whether the store was read, so an unreadable store and an empty one would
// render identically."* `Store` is the field that carries that distinction, and
// collapsing it is how a missing store reports as a quiet fleet.
type ParkEventDue struct {
	// Store is `readable`, `absent`, or `unreadable`.
	Store string `json:"store"`
	// Reason names why the read resolved nothing, when it did.
	Reason string `json:"reason"`
	// Due is one record per watch whose re-check time has passed.
	Due []ParkEventWatch `json:"due"`
}

// ParkEventWatch is one due watch.
type ParkEventWatch struct {
	// Slug is the watch's own name.
	Slug string `json:"slug"`
	// Tasks maps session id → task name. `due` names the TASK because
	// `/supervisor:open --task "<name>"` is what resumes it; a bare session id
	// leaves the round with nothing to open.
	Tasks map[string]string `json:"tasks"`
	// Sessions are the parked members.
	Sessions []string `json:"sessions"`
}

// DueTasks returns the distinct task names across every due watch, sorted.
//
// ⚠️ **Several sessions on one watch are still ONE act.** The script groups them
// into one record for that reason; iterating sessions instead would open the same
// task once per member.
//
// ⚠️ **Sorted, because `Tasks` is a map.** Go randomises map iteration, so an
// unsorted result would pick a different first task on every tick — the same
// watch would resume a different task each time, and the log would not be
// reproducible. The sort is what makes "the first due task" a stable answer.
func (d ParkEventDue) DueTasks() []string {
	seen := map[string]struct{}{}
	tasks := []string{}
	for _, watch := range d.Due {
		for _, task := range watch.Tasks {
			if task == "" {
				continue
			}
			if _, ok := seen[task]; ok {
				continue
			}
			seen[task] = struct{}{}
			tasks = append(tasks, task)
		}
	}
	sort.Strings(tasks)
	return tasks
}

// NewParkEventDue builds the park-watch read over `park-event.py due --json`.
//
// ⚠️ **`store=absent` and `store=readable` with nothing due are different facts.**
// Absent means the directory does not exist; readable means the store was read
// and the negative is real. Both yield `due=false` here, because neither licenses
// an act — but an UNREADABLE store is an error, never a quiet tick.
func NewParkEventDue(run KeepaliveScriptRunner) func(ctx context.Context) (task string, due bool, err error) {
	return func(ctx context.Context) (string, bool, error) {
		if run == nil {
			return "", false, errors.New(ctx, "park-event read has no script runner")
		}
		output, code, err := run(ctx, "due", "--json")
		if err != nil {
			return "", false, errors.Wrap(ctx, err, "park-event due failed")
		}
		if code != 0 {
			return "", false, errors.Errorf(ctx, "park-event due exited %d", code)
		}
		var parsed ParkEventDue
		if unmarshalErr := json.Unmarshal([]byte(output), &parsed); unmarshalErr != nil {
			return "", false, errors.Wrap(ctx, unmarshalErr, "park-event due printed unparseable JSON")
		}
		if parsed.Store == "unreadable" {
			// A store that could not be read is not a fleet with nothing parked.
			return "", false, errors.Errorf(ctx, "park-event store unreadable: %s", parsed.Reason)
		}
		tasks := parsed.DueTasks()
		if len(tasks) == 0 {
			return "", false, nil
		}
		return tasks[0], true, nil
	}
}
