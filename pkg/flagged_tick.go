// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

const (
	// DefaultFlaggedTickInterval is how often the reopen tick runs.
	//
	// 15 minutes is the bound the fleet's liveness expectation is written in: a
	// flagged row that goes dark must be back inside one tick, not inside one
	// model round. The fleet round's model leg was measured at 54 of 100 rounds
	// never running (2026-11-11), so the interval has to hold without a model.
	DefaultFlaggedTickInterval = 15 * time.Minute

	// maxRowsPerTick is the operator's 2026-11-07 ruling (open item e6576cc2):
	// flagged rows open to a hard cap of 50, Direct (`role: human`) rows first.
	//
	// ⚠️ This is NOT `spawn.maxHeadless: 50`. On a tab spawn the resolved mode is
	// `interactive`, so the mode axis that binds through SpawnCaps is
	// `spawn.maxInteractive` — a tick relying on maxHeadless to enforce the
	// ruling would never reach 50. It is a per-tick ROW budget, a different axis
	// from SpawnCaps, which bounds what is live and how fast sessions may start.
	maxRowsPerTick = 50

	// recentlySpawnedWindow is how long a row is left alone after its session
	// was spawned.
	//
	// Without it a row whose session has not yet registered reads ABSENT and is
	// opened a second time. The window is far longer than registration takes,
	// which is the point: it is a safety margin, not a measurement.
	//
	// ⚠️ AC2's mutation check edits THIS value to `0` and expects the
	// `recently-spawned` case to flip to opened, failing the run. It must stay a
	// package-level constant for that mutation to be a code change rather than
	// test input.
	recentlySpawnedWindow = 60 * time.Minute

	// predicateTimeout bounds one predicate read.
	//
	// ⚠️ Twice raised from measurement, not estimate. At 2 minutes the first live
	// tick was killed (`run python3 failed: signal: killed`); at 10 minutes a tick
	// was killed again on 2026-11-12 10:15. The predicate walks every configured
	// vault's task pages, so its cost tracks fleet size and keeps growing.
	//
	// 25 minutes is deliberately larger than the tick interval: the loop is
	// sequential, so an overrun delays the NEXT tick rather than overlapping it,
	// and a slow read is a liveness delay while a killed read is a tick that did
	// nothing at all. The bound exists to abandon a hung read, never a slow one.
	predicateTimeout = 25 * time.Minute
)

// The refusal and skip reasons, named so the log line is greppable and so a
// test asserts on the same string the operator reads.
const (
	reasonBlocked         = "blocked"
	reasonNotAbsent       = "not-absent"
	reasonAlreadyLive     = "already-live"
	reasonRecentlySpawned = "recently-spawned"
	reasonRowBudget       = "row budget"
	reasonCapsRefused     = "caps refused"
	reasonRateRefused     = "rate refused"
)

// FlaggedTickResult is one tick's outcome, and the whole of what it logs.
type FlaggedTickResult struct {
	// Opened holds the key of every row this tick opened.
	Opened []string
	// Refused holds `key: reason` for every row the tick would not open.
	Refused []string
	// Skipped holds `key: reason` for every row that was eligible but not due.
	Skipped []string
}

// Line renders the one log line a tick writes.
//
// `rows=` is the field the liveness measurement pairs against a reaper close: a
// count alone cannot be matched to a session, so the keys are part of the line
// rather than a second line a caller might not write.
//
// `reasons=` exists because counts alone are undiagnosable. Measured 2026-11-12:
// the deployed tick logged `opened=0 refused=17..33` across ten ticks while the
// predicate offered twelve eligible ABSENT rows, and nothing in the line said
// why — the reason strings were already computed per row and then dropped here.
// Capped so a large fleet cannot produce a megabyte line; the head of the list
// carries the cause because the first failure is what stops a tick.
func (r FlaggedTickResult) Line() string {
	line := fmt.Sprintf(
		"flagged-tick opened=%d refused=%d skipped=%d rows=%s",
		len(r.Opened), len(r.Refused), len(r.Skipped), strings.Join(r.Opened, ","),
	)
	if reasons := summariseReasons(r); reasons != "" {
		line += " reasons=" + reasons
	}
	return line
}

// maxLoggedReasons bounds the reasons field.
const maxLoggedReasons = 5

func summariseReasons(r FlaggedTickResult) string {
	all := make([]string, 0, len(r.Refused)+len(r.Skipped))
	all = append(all, r.Refused...)
	all = append(all, r.Skipped...)
	if len(all) == 0 {
		return ""
	}
	if len(all) > maxLoggedReasons {
		all = all[:maxLoggedReasons]
	}
	return strings.Join(all, "; ")
}

// FlaggedTick reopens flagged rows that have gone dark.
//
// It is model-free by construction: the eligibility predicate is a script, the
// spawn is the same TabSpawner the roster's `spawn_agent` already uses, and
// nothing in the path asks a model to decide anything. That is the whole point —
// the close side (launchd `idle-reaper`) is deterministic, so the reopen side
// has to be too, or a flagged row stays dark whenever the model leg does not run.
type FlaggedTick struct {
	Source  FlaggedRowSource
	Spawner TabSpawner
	Caps    SpawnCaps
	// Ledger supplies the spawn times the recently-spawned guard reads. A nil
	// ledger disables that guard rather than refusing every row.
	Ledger LedgerReader
	Now    libtime.CurrentDateTimeGetter
	// Interval defaults to DefaultFlaggedTickInterval.
	Interval time.Duration
	// ReadTimeout bounds one predicate read, defaulting to predicateTimeout.
	// It is a field rather than only a constant so a test can drive the
	// abandonment path without waiting the production two minutes.
	ReadTimeout time.Duration
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
}

// flaggedRowCache holds the most recent predicate read.
//
// It exists because the predicate measured 18.8 MINUTES wall against 75 seconds
// of CPU (2026-11-12): `flagged-rows.py` probes liveness by spawning a subprocess
// per row, and those probes time out. Reading it synchronously on the tick meant
// one slow read cost an entire tick — the loop's period became read + interval
// (~34 min) instead of the 15 minutes SC1 names.
//
// With the cache the tick always fires on schedule against the last good row set,
// and a slow read costs freshness rather than a tick.
type flaggedRowCache struct {
	mutex sync.RWMutex
	rows  []FlaggedRow
	err   error
	set   bool
}

func (c *flaggedRowCache) store(rows []FlaggedRow, err error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.rows, c.err, c.set = rows, err, true
}

func (c *flaggedRowCache) snapshot() (rows []FlaggedRow, err error, ok bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.rows, c.err, c.set
}

// Run ticks until the context ends.
//
// The first tick fires immediately rather than after one interval: a daemon that
// restarts after a deploy should reopen what went dark during the restart, and
// waiting a full interval to do it is a liveness gap the restart itself created.
// Until the first read returns the tick logs `skipped` rather than blocking.
func (t *FlaggedTick) Run(ctx context.Context) error {
	interval := t.Interval
	if interval <= 0 {
		interval = DefaultFlaggedTickInterval
	}
	cache := &flaggedRowCache{}
	go t.refreshLoop(ctx, cache, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		t.tickAndLog(ctx, cache)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// refreshLoop keeps the row set warm, off the tick's critical path.
func (t *FlaggedTick) refreshLoop(
	ctx context.Context, cache *flaggedRowCache, interval time.Duration,
) {
	for {
		rows, err := t.readRows(ctx)
		cache.store(rows, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// tickAndLog runs one tick against the cached row set and writes its line.
func (t *FlaggedTick) tickAndLog(ctx context.Context, cache *flaggedRowCache) {
	rows, err, ok := cache.snapshot()
	if !ok {
		t.logf("flagged-tick skipped: predicate has not returned yet")
		return
	}
	if err != nil {
		t.logf("flagged-tick failed: %v", err)
		return
	}
	t.logf("%s", t.tickFromRows(ctx, rows).Line())
}

func (t *FlaggedTick) logf(format string, args ...any) {
	if t.Logf != nil {
		t.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Tick runs one reopen pass and reports what it did.
//
// Every guard is a named branch with its own reason string, because the log line
// is the only evidence a 24 h measurement has: `refused=3` with no reasons would
// be indistinguishable from a tick that opened nothing because it was broken.
func (t *FlaggedTick) Tick(ctx context.Context) (FlaggedTickResult, error) {
	rows, err := t.readRows(ctx)
	if err != nil {
		return FlaggedTickResult{}, err
	}
	return t.tickFromRows(ctx, rows), nil
}

// tickFromRows is the decision pass, separated from the read so the running loop
// can feed it a cached row set.
func (t *FlaggedTick) tickFromRows(
	ctx context.Context, rows []FlaggedRow,
) FlaggedTickResult {
	var result FlaggedTickResult
	recent := t.recentlySpawned(ctx)
	now := t.now()
	for _, row := range orderFlaggedRows(rows) {
		if len(result.Opened) >= maxRowsPerTick {
			result.Refused = append(result.Refused, row.Key()+": "+reasonRowBudget)
			continue
		}
		if reason, refused, skipped := decision(row, recent, now); refused || skipped {
			if skipped {
				result.Skipped = append(result.Skipped, row.Key()+": "+reason)
				continue
			}
			result.Refused = append(result.Refused, row.Key()+": "+reason)
			continue
		}
		// A cap or rate refusal ENDS the tick's opening rather than being retried
		// against every remaining row: the refusal is a property of the fleet, not
		// of the row, so the next row would be refused for the same reason and the
		// log would fill with duplicate noise.
		if err := t.Caps.CheckWorker(ctx, row.Mode, false); err != nil {
			result.Refused = append(result.Refused, row.Key()+": "+reasonCapsRefused+": "+err.Error())
			break
		}
		if err := t.Caps.ReserveStart(ctx); err != nil {
			result.Refused = append(result.Refused, row.Key()+": "+reasonRateRefused+": "+err.Error())
			break
		}
		if _, err := t.Spawner.Spawn(ctx, request(row)); err != nil {
			result.Refused = append(result.Refused, row.Key()+": spawn failed: "+err.Error())
			continue
		}
		result.Opened = append(result.Opened, row.Key())
	}
	return result
}

// readRows bounds one predicate read.
//
// The real script takes ~2 minutes; a hung read would otherwise stall the single
// goroutine and every later tick with it.
func (t *FlaggedTick) readRows(ctx context.Context) ([]FlaggedRow, error) {
	timeout := t.ReadTimeout
	if timeout <= 0 {
		timeout = predicateTimeout
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rows, err := t.Source.Rows(readCtx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read flagged rows")
	}
	return rows, nil
}

// request builds the spawn the row asks for.
//
// SpawnerSessionID stays empty on purpose: the tick is not a session, and
// attributing its spawns to one would make a fleet round appear to have opened a
// row it never touched.
func request(row FlaggedRow) TabSpawnRequest {
	return TabSpawnRequest{
		Label:      row.Name,
		Vault:      row.Vault,
		Role:       row.Role,
		ModeSource: "flagged-tick",
	}
}

// recentlySpawned maps session id to its ledger spawn time.
//
// A ledger read that fails is an empty map, not an error: the guard is a safety
// margin, and refusing every row because the store was down would turn a store
// outage into a fleet-wide stall.
func (t *FlaggedTick) recentlySpawned(ctx context.Context) map[string]time.Time {
	if t.Ledger == nil {
		return map[string]time.Time{}
	}
	rows, err := t.Ledger.Rows(ctx)
	if err != nil {
		t.logf("flagged-tick: ledger read failed, recently-spawned guard inactive: %v", err)
		return map[string]time.Time{}
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		at, parseErr := time.Parse(time.RFC3339, row.SpawnedAt)
		if parseErr != nil {
			continue
		}
		out[row.SessionID] = at
	}
	return out
}

func (t *FlaggedTick) now() time.Time {
	if t.Now == nil {
		return time.Now()
	}
	return t.Now.Now().Time()
}

// decision answers whether a row is opened this tick.
//
// Order matters. Provenance is checked before liveness so an agent-set flag is
// reported as REFUSED rather than skipped — the two are different operator
// questions, and collapsing them would hide a provenance defect behind a
// perfectly healthy LIVE row.
func decision(
	row FlaggedRow,
	recent map[string]time.Time,
	now time.Time,
) (reason string, refused bool, skipped bool) {
	if len(row.Refused) > 0 {
		return row.Refused[0], true, false
	}
	if len(row.BlockedBy) > 0 {
		return reasonBlocked, false, true
	}
	// Only ABSENT is eligible.
	//
	// ⚠️ This used to skip only LIVE, which made `none` and `UNKNOWN` eligible
	// too — a row the predicate could not classify would have been opened, and a
	// row already running would have been opened a second time. The spec says
	// "for each ABSENT row"; anything the predicate could not prove absent is
	// left alone. `none` is what `--no-liveness` reports for every row, so the
	// permissive form would have reopened the entire fleet.
	if row.Liveness != "ABSENT" {
		return reasonNotAbsent, false, true
	}
	if row.Branch == "jump" {
		return reasonAlreadyLive, false, true
	}
	if at, ok := recent[row.SessionID]; ok && now.Sub(at) < recentlySpawnedWindow {
		return reasonRecentlySpawned, false, true
	}
	return "", false, false
}

// orderFlaggedRows puts Direct (`role: human`) rows first.
//
// The operator ruling of 2026-11-07 is explicit about the order, and it is not
// cosmetic: under the row budget the ordering decides which rows are opened at
// all, so an agent row that sorted first would displace a Direct one every tick.
//
// ⚠️ This re-sort is required, not incidental. The predicate already sorts by
// `kind` with topics and goals ahead of tasks, so preserving its order would let
// a manager row jump ahead of a human one. The sort is stable so rows within a
// role keep the predicate's own order.
func orderFlaggedRows(rows []FlaggedRow) []FlaggedRow {
	out := make([]FlaggedRow, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Role == "human" && out[j].Role != "human"
	})
	return out
}
