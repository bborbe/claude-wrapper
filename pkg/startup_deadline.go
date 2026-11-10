// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bborbe/claude-wrapper/pkg/supervisorconfig"
)

// The startup deadline bounds how long a child may rest in StateStarting.
//
// StateStarting is cleared in exactly one place — applyInit, on the child's
// `system`/`init` event. A child that never emits one is StateStarting for the
// life of its process: it holds its memory, occupies a spawn slot, and
// `reap-idle-headless.py` refuses it `wrapper-busy` forever because it is not
// `idle`. Measured 2026-11-10: nine such sessions held ~174 MiB each for nearly
// three hours after a daemon restart, and a 19-sid reap closed none of them.
//
// The deadline is therefore the only mechanism that can clear the state without
// an external trigger. It is deliberately narrower than "starting too long":
// only a child with an EMPTY model is terminated, because a populated model
// proves the child has done real work and a long start is a slow start, not a
// stuck one.
const (
	// DefaultMaxStartingSeconds is the built-in bound. It sits under the 300 s
	// the repair is measured against so that the sweeper's own granularity cannot
	// breach that measurement: worst case is one sweep interval past the bound.
	DefaultMaxStartingSeconds = 300
	// StartupSweepInterval is how often EnforceStartupDeadline should be called.
	// The worst-case termination lag is one interval past the bound.
	StartupSweepInterval = 30 * time.Second
)

// StartupConfigFile is the part of the supervisor config the startup deadline
// reads. Read per sweep rather than snapshotted at boot, like the spawn caps: a
// lowered bound takes effect on the next tick with no restart.
type StartupConfigFile struct {
	Startup struct {
		MaxStartingSeconds json.RawMessage `json:"maxStartingSeconds"`
	} `json:"startup"`
}

// LoadStartupConfig reads and parses the config file.
//
// An absent file is no config, which means the built-in default. An unreadable
// or unparseable one is logged and read as no config, as LoadSpawnConfig does:
// the default is still a bound, so the fleet stays bounded while the operator
// fixes the file.
func LoadStartupConfig(ctx context.Context, path string) StartupConfigFile {
	var file StartupConfigFile
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("WARNING: read %s failed (%v) — startup deadline falls back to %ds",
				path, err, DefaultMaxStartingSeconds)
		}
		return file
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("WARNING: parse %s failed (%v) — startup deadline falls back to %ds",
			path, err, DefaultMaxStartingSeconds)
		return StartupConfigFile{}
	}
	return file
}

// MaxStarting resolves `startup.maxStartingSeconds`.
//
// Absent means the default. `0` disables the deadline entirely — the same
// off-switch idiom the spawn caps use. A non-integer or negative value is a
// refusal to guess: it is logged and read as the default, because an operator
// typo must not silently remove the bound.
func (f StartupConfigFile) MaxStarting(ctx context.Context, path string) time.Duration {
	raw := strings.TrimSpace(string(f.Startup.MaxStartingSeconds))
	if raw == "" || raw == "null" {
		return DefaultMaxStartingSeconds * time.Second
	}
	var seconds int
	if err := json.Unmarshal(f.Startup.MaxStartingSeconds, &seconds); err != nil {
		log.Printf("WARNING: startup.maxStartingSeconds in %s is not an integer (%v) — falls back to %ds",
			path, err, DefaultMaxStartingSeconds)
		return DefaultMaxStartingSeconds * time.Second
	}
	if seconds < 0 {
		log.Printf("WARNING: startup.maxStartingSeconds in %s resolves to %d — falls back to %ds",
			path, seconds, DefaultMaxStartingSeconds)
		return DefaultMaxStartingSeconds * time.Second
	}
	if seconds == 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// shouldTerminateStarting reports whether a session has rested in StateStarting
// past bound without ever initialising.
//
// Two conditions, and the second is the one that keeps this safe:
//
//   - State is StateStarting. An idle or generating child is untouched, whatever
//     its age — state is derived from the event stream, so a long-running turn is
//     never mistaken for a stuck start.
//   - Model is empty. applyInit sets it from the init event, and apply() sets it
//     from any assistant message. A populated model is proof the child has done
//     real work, so a long start there is slow, not stuck, and must not be killed.
//
// A zero StartedAt is skipped rather than read as "infinitely old": a record
// without a start time cannot be aged, and guessing would kill a healthy child.
func shouldTerminateStarting(session Session, now time.Time, bound time.Duration) bool {
	if bound <= 0 {
		return false
	}
	if session.State != StateStarting {
		return false
	}
	if session.Model != "" {
		return false
	}
	if session.StartedAt.IsZero() {
		return false
	}
	return now.Sub(session.StartedAt) > bound
}

// EnforceStartupDeadline terminates every child that has rested in StateStarting
// past the configured bound without an init event, and reports how many it
// stopped.
//
// It never kills a child it cannot age or whose model is populated, and it skips
// a session with no pid rather than signalling pid 0 (which would signal the
// whole process group). Termination is a SIGKILL by pid, the same path Stop
// takes, so an adopted child — whose *exec.Cmd this process never held — is
// stopped too. The child's own watch goroutine settles the record to StateExited
// once the process is gone.
func (s *supervisor) EnforceStartupDeadline(ctx context.Context) int {
	return s.terminateStarting(ctx, s.startupBound(ctx))
}

// terminateStarting is EnforceStartupDeadline with the bound supplied, so the
// termination path can be exercised without reading the operator's config file.
func (s *supervisor) terminateStarting(ctx context.Context, bound time.Duration) int {
	if bound <= 0 {
		return 0
	}
	now := s.now.Now().Time()

	s.mutex.RLock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mutex.RUnlock()

	stopped := 0
	for _, id := range ids {
		s.mutex.RLock()
		c, ok := s.sessions[id]
		s.mutex.RUnlock()
		if !ok {
			continue
		}
		snapshot := c.snapshot()
		if !shouldTerminateStarting(*snapshot, now, bound) {
			continue
		}
		if c.pid <= 0 {
			log.Printf("WARNING: startup deadline: session %s rested in %q past %s with no pid to stop",
				id, snapshot.State, bound)
			continue
		}
		log.Printf("startup deadline: session %s rested in %q for %s with no init event — terminating pid %d",
			id, snapshot.State, now.Sub(snapshot.StartedAt).Round(time.Second), c.pid)
		if err := c.stop(ctx); err != nil {
			log.Printf("WARNING: startup deadline: stop %s failed: %v", id, err)
			continue
		}
		stopped++
	}
	return stopped
}

// startupBound resolves the configured deadline, falling back to the built-in
// default when the config path itself cannot be resolved.
func (s *supervisor) startupBound(ctx context.Context) time.Duration {
	path, err := supervisorconfig.Path(ctx, "", nil)
	if err != nil {
		log.Printf("WARNING: startup deadline: resolve config path failed (%v) — using default %ds",
			err, DefaultMaxStartingSeconds)
		return DefaultMaxStartingSeconds * time.Second
	}
	return LoadStartupConfig(ctx, path).MaxStarting(ctx, path)
}
