// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// progressStallFileName is the file the per-row progress clock is persisted to.
//
// It sits beside the flagged-rows cache because both are the tick's own state and
// both must survive a restart: the daemon restarts on every deploy, and a stall
// clock that reset on each restart could never reach its threshold.
const progressStallFileName = "progress-stall.json"

// progressStallThreshold is how long a row's `# Progress` section must be
// unchanged before the row is skipped as stalled.
//
// It matches sweepgate's IdleStuck window, so the two paths that judge "this row
// is doing nothing" agree on the bound. It is deliberately longer than any single
// tool call a working session runs without writing progress.
const progressStallThreshold = 30 * time.Minute

// progressStamp is one row's progress clock.
type progressStamp struct {
	// Hash is the `# Progress` section hash the clock was started for.
	Hash string `json:"hash"`
	// Since is when that hash was first seen unchanged.
	Since time.Time `json:"since"`
}

// progressStallTracker answers whether a row's progress has stalled.
//
// The map is loaded once per process and persisted on every change, so a restart
// resumes the clock rather than resetting it. It is the tick's own state: the
// sweepgate busy clock is a different consumer's file and is never touched here.
type progressStallTracker struct {
	dir string

	mu     sync.Mutex
	mem    map[string]progressStamp
	loaded bool
}

// newProgressStallTracker builds a tracker over stateDir. An empty dir keeps the
// clock in memory only, which is what a fixture without a state directory gets.
func newProgressStallTracker(dir string) *progressStallTracker {
	return &progressStallTracker{dir: dir}
}

// Stalled reports whether key's progress hash has been unchanged for at least
// progressStallThreshold as of now.
//
// First sight — no prior stamp — starts the clock and returns false, so a row is
// never skipped on the tick that first sees it (fail open). A changed hash
// restarts the clock. An empty hash is treated as an ordinary value: a page with
// no `# Progress` section stalls only if that absence is itself unchanged.
func (p *progressStallTracker) Stalled(key, hash string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadLocked()
	stamp, ok := p.mem[key]
	if !ok || stamp.Hash != hash {
		p.mem[key] = progressStamp{Hash: hash, Since: now}
		p.persistLocked()
		return false
	}
	return now.Sub(stamp.Since) >= progressStallThreshold
}

func (p *progressStallTracker) loadLocked() {
	if p.loaded {
		return
	}
	p.loaded = true
	p.mem = map[string]progressStamp{}
	if p.dir == "" {
		return
	}
	body, err := os.ReadFile(p.path()) // #nosec G304 -- daemon state dir, not request input
	if err != nil {
		return
	}
	// A corrupt file is not a failed tick: the clock is an optimisation across a
	// restart, so losing it costs one threshold's delay, exactly as if the row had
	// just been seen for the first time.
	_ = json.Unmarshal(body, &p.mem)
}

func (p *progressStallTracker) persistLocked() {
	if p.dir == "" {
		return
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return
	}
	body, err := json.Marshal(p.mem)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(p.dir, "*.json.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	// A successful rename removes the temp name, so this only cleans up a failed
	// write.
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, p.path())
}

func (p *progressStallTracker) path() string {
	return filepath.Join(p.dir, progressStallFileName)
}
