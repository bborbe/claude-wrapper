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
	//
	// The window governs ONE class of row only, and since a terminal ledger
	// status became an exemption in `recentlySpawned` the two classes have
	// different worst-case decision ages:
	//
	//   - STARTING row (ledger status non-terminal, or no ledger entry at all):
	//     90 minutes, not 60, because the row cache a tick decides on can be up
	//     to 2 × predicateTimeout + one interval old (a read that started just
	//     before a spawn, then a full read and an interval after it) —
	//     2 × 25 + 15 = 65 minutes today. A window shorter than that lets a
	//     stale `none` row be opened a second time. The cost of the extra 30
	//     minutes over the old 60: such a row is resumed that much later.
	//   - FINISHED row (ledger status `done` or `error`): no window at all. Its
	//     worst-case decision age is the next tick — at most one interval
	//     (15 minutes) after the ledger records the end, plus that tick's own
	//     read. Nothing is gained by holding it longer: the duplicate-open
	//     hazard this constant covers exists only while a session may still be
	//     registering.
	recentlySpawnedWindow = 90 * time.Minute

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
	// reasonUnconsidered marks a row the tick never evaluated because an earlier
	// row's caps or rate refusal ended the opening pass. It exists so such a row
	// is still accounted for: the alternative is dropping it, which makes the
	// counts under-report the fleet and leaves a row with no trace at all.
	reasonUnconsidered = "unconsidered"
)

// FlaggedTickResult is one tick's outcome, and the whole of what it logs.
type FlaggedTickResult struct {
	// Opened holds the key of every row this tick opened.
	Opened []string
	// Refused holds `key: reason` for every row the tick would not open.
	Refused []string
	// Skipped holds `key: reason` for every row that was eligible but not due.
	Skipped []string
	// Considered is how many rows the tick was handed. It is the number the
	// three buckets must add up to; without it a dropped row is invisible.
	Considered int
}

// Line renders the summary line a tick writes.
//
// `rows=` is the field the liveness measurement pairs against a reaper close: a
// count alone cannot be matched to a session, so the keys are part of the line.
//
// `refused_by_reason=` / `skipped_by_reason=` exist because counts alone are
// undiagnosable, and because a single truncated list is worse than a count: the
// five-entry `reasons=` field this replaces measured 2026-11-16 on a tick with
// `refused=9 skipped=31` printed five `agent-set-flag` entries, so the skip
// reasons — and four refusals — were truncated out of existence. A per-class
// count cannot be truncated: every class that has a member is printed.
//
// `considered=` is the number of rows the tick was handed, so
// `opened + refused + skipped == considered` is checkable. Without it a row the
// loop dropped reads exactly like a row that was never in the set.
//
// Detail() carries the per-row names. The two lines together are the whole of
// what a tick logs.
func (r FlaggedTickResult) Line() string {
	line := fmt.Sprintf(
		"flagged-tick opened=%d refused=%d skipped=%d considered=%d rows=%s",
		len(r.Opened), len(r.Refused), len(r.Skipped), r.Considered,
		strings.Join(r.Opened, ","),
	)
	if counts := formatCounts(r.Refused); counts != "" {
		line += " refused_by_reason=" + counts
	}
	if counts := formatCounts(r.Skipped); counts != "" {
		line += " skipped_by_reason=" + counts
	}
	return line
}

// Detail renders the untruncated per-row reasons, or "" when there are none.
//
// It is a second line rather than another field on Line() because it is bounded
// by the row count, not by a constant: a 50-row fleet names 50 rows. Splitting
// the two keeps the summary greppable and short while every row stays
// recoverable — the property the capped `reasons=` field could not provide.
func (r FlaggedTickResult) Detail() string {
	if len(r.Refused) == 0 && len(r.Skipped) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"flagged-tick detail refused=%s skipped=%s",
		strings.Join(r.Refused, "; "), strings.Join(r.Skipped, "; "),
	)
}

// formatCounts renders `reason:count` pairs, sorted so the line is stable across
// runs of the same tick.
func formatCounts(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	counts := make(map[string]int, len(entries))
	for _, entry := range entries {
		counts[reasonClass(entry)]++
	}
	classes := make([]string, 0, len(counts))
	for class := range counts {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	parts := make([]string, 0, len(classes))
	for _, class := range classes {
		parts = append(parts, fmt.Sprintf("%s:%d", class, counts[class]))
	}
	return strings.Join(parts, "; ")
}

// reasonClass is the guard name an entry belongs to.
//
// An entry is `key: reason`, and a caps/rate refusal carries the cap's own error
// after its guard name (`key: caps refused: maxConcurrent reached`), so the class
// is the second field cut at the next `: `. Row keys are vault/kind/name or a
// session id and never contain `: `, which is what makes the first cut safe.
func reasonClass(entry string) string {
	_, rest, ok := strings.Cut(entry, ": ")
	if !ok {
		return entry
	}
	class, _, _ := strings.Cut(rest, ": ")
	return class
}

// FlaggedTick reopens flagged rows that have gone dark.
//
// It is model-free by construction: the eligibility predicate is a script, the
// reopen is the wrapper's own spawn paths, and nothing in the path asks a model
// to decide anything. That is the whole point — the close side (launchd
// `idle-reaper`) is deterministic, so the reopen side has to be too, or a flagged
// row stays dark whenever the model leg does not run.
type FlaggedTick struct {
	Source FlaggedRowSource
	// Reopener opens one row through the spawn path the row's mode names. It is a
	// separate collaborator rather than a TabSpawner because a headless row must be
	// RESUMED, and the tab path refuses `resume` by construction.
	Reopener FlaggedReopener
	Caps     SpawnCaps
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

	// openedMutex guards opened.
	openedMutex sync.Mutex
	// opened records, in process, when this tick opened a never-opened row, keyed
	// by labelKey(row name). ⚠️ The ledger is not enough on its own: a tab spawn
	// files its ledger row only once the session registers (an 8 s deadline), and
	// a ledger outage files nothing — either way the next tick would find no
	// record and open the same row again. Bounded by the number of distinct task
	// names this daemon ever opened fresh; expired entries are ignored by the
	// window check, not pruned.
	opened map[string]time.Time
}

// rememberOpened records a never-opened row this process just opened.
func (t *FlaggedTick) rememberOpened(row FlaggedRow, at time.Time) {
	t.openedMutex.Lock()
	defer t.openedMutex.Unlock()
	if t.opened == nil {
		t.opened = map[string]time.Time{}
	}
	t.opened[labelKey(row.Name)] = at
}

// mergeOpened folds this process's own opens into the ledger-derived map.
func (t *FlaggedTick) mergeOpened(recent map[string]time.Time) {
	t.openedMutex.Lock()
	defer t.openedMutex.Unlock()
	for key, at := range t.opened {
		if prev, ok := recent[key]; !ok || at.After(prev) {
			recent[key] = at
		}
	}
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
	mutex   sync.RWMutex
	rows    []FlaggedRow
	lastErr error
	// readFor is how long the most recent read attempt took, successful or not.
	// It is carried here rather than in the result because the read happens off
	// the tick's goroutine: the tick reports the cost of the read it is serving,
	// which is the number the read-cost measurement is about.
	readFor time.Duration
	set     bool
}

// store records one read.
//
// A FAILED read never discards the last good row set. That is the point of the
// cache: the read is bounded by predicateTimeout, a kill is an expected outcome of
// that bound, and one killed read must cost freshness rather than every later tick.
//
// Before this, the error was stored alongside the rows and tickAndLog returned
// early on it, so a single kill disabled EVERY subsequent tick until a read
// succeeded. On 2026-11-16 that turned one killed read into 18 consecutive dead
// ticks and four operator-flagged rows opening 2 h 01 m late.
//
// A failed FIRST read leaves set=false on purpose: there is no good row set yet, so
// the tick keeps logging `skipped` rather than fabricating an empty one.
func (c *flaggedRowCache) store(rows []FlaggedRow, err error, took time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.readFor = took
	if err != nil {
		c.lastErr = err
		return
	}
	c.rows, c.lastErr, c.set = rows, nil, true
}

// snapshot returns the row set the tick should serve, how long the read behind it
// took, whether there is a row set at all, and the last read's error when the rows
// are stale (the error is last because ST1008 requires it).
func (c *flaggedRowCache) snapshot() (
	rows []FlaggedRow, readFor time.Duration, ok bool, stale error,
) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.rows, c.readFor, c.set, c.lastErr
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
		started := t.now()
		rows, err := t.readRows(ctx)
		// Measure even a failed read: a kill at predicateTimeout is the worst case
		// the cost measurement exists to catch, so dropping it would bias the p95
		// down exactly on the slow reads.
		cache.store(rows, err, t.now().Sub(started))
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// tickAndLog runs one tick against the cached row set and writes its line.
func (t *FlaggedTick) tickAndLog(ctx context.Context, cache *flaggedRowCache) {
	rows, readFor, ok, stale := cache.snapshot()
	if !ok {
		// No good row set yet. Name the failure when there was one — a first read
		// that always fails would otherwise log `skipped: predicate has not
		// returned yet` forever and hide its own error.
		if stale != nil {
			t.logf("flagged-tick read failed: %v (%s, no good row set yet — skipping)",
				stale, readFor)
		}
		t.logf("flagged-tick skipped: predicate has not returned yet")
		return
	}
	if stale != nil {
		// The read failed; the tick still runs on the last good row set. The
		// failure gets its own line, deliberately distinct from `flagged-tick
		// failed` (which means no tick ran): a stale tick must be visible in the
		// log without being counted as a dead one, or a 24 h measurement cannot
		// tell "served stale rows" from "opened nothing".
		t.logf("flagged-tick read failed: %v (%s, serving %d rows from the last good read)",
			stale, readFor, len(rows))
	}
	result := t.tickFromRows(ctx, rows)
	// read_duration= is the read-cost measurement: the cost of the most recent read
	// ATTEMPT, including a failed one — a read killed at the bound is the slow case
	// this exists to catch, so reporting only successful reads would bias the p95
	// down exactly where it matters. It is emitted on the tick line, not the detail
	// line, so a grep for it counts ticks.
	t.logf("%s read_duration=%s", result.Line(), readFor.Round(time.Second))
	// The detail line is logged separately so it carries its own timestamp: a
	// newline embedded in one Printf would leave the second line unprefixed and
	// harder to pair with the tick it belongs to.
	if detail := result.Detail(); detail != "" {
		t.logf("%s", detail)
	}
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
	ordered := orderFlaggedRows(rows)
	result.Considered = len(ordered)
	recent := t.recentlySpawned(ctx)
	t.mergeOpened(recent)
	now := t.now()
	// halted is the fleet-level refusal that ended this tick's opening pass. It
	// is a reason string, not a bool, because the rows it leaves behind are still
	// recorded — as unconsidered — so the buckets add up to Considered.
	halted := ""
	for _, row := range ordered {
		if halted != "" {
			result.Refused = append(result.Refused, row.Key()+": "+halted)
			continue
		}
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
		// of the row, so the next row would be refused for the same reason.
		//
		// ⚠️ It does NOT end the loop. Ending it dropped every later row out of all
		// three buckets, so `opened + refused + skipped` under-reported the fleet and
		// a row could vanish with no trace at all — the accounting defect this tick
		// had measured 2026-11-16. The remaining rows are now recorded as
		// unconsidered, which is the honest description of what happened to them.
		if err := t.Caps.CheckWorker(ctx, row.Mode, false); err != nil {
			halted = reasonUnconsidered
			result.Refused = append(result.Refused, row.Key()+": "+reasonCapsRefused+": "+err.Error())
			continue
		}
		if err := t.Caps.ReserveStart(ctx); err != nil {
			halted = reasonUnconsidered
			result.Refused = append(result.Refused, row.Key()+": "+reasonRateRefused+": "+err.Error())
			continue
		}
		if err := t.Reopener.Reopen(ctx, row); err != nil {
			result.Refused = append(result.Refused, row.Key()+": spawn failed: "+err.Error())
			continue
		}
		if neverOpened(row) {
			t.rememberOpened(row, now)
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

// recentlySpawned maps session id — and, for a never-opened row, labelKey(task
// name) — to its ledger spawn time.
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
		// A spawn the ledger has CLOSED is not a spawn to stay away from.
		//
		// The window exists so a row whose session has not yet registered is not
		// opened a second time, and that hazard is gone once the session has
		// ended. Keeping a closed spawn in this map suppresses a row whose
		// worker finished its turn WITHOUT finishing the task: the row is dark,
		// eligible, and untouched for the rest of the window. Measured
		// 2026-11-16 17:47 — 30 of 33 ABSENT rows suppressed, the oldest at 84
		// minutes, every one of their ledger entries already terminal.
		//
		// ⚠️ One direction only, like every reader of this field: a terminal
		// status proves the session ENDED; every other value, an absent one
		// included, proves nothing and still suppresses.
		if terminalLedgerStatuses[row.Status] {
			continue
		}
		at, parseErr := time.Parse(time.RFC3339, row.SpawnedAt)
		if parseErr != nil {
			continue
		}
		out[row.SessionID] = at
		// A never-opened row has no session id to look up, so its spawn is also
		// filed under its label — the task name both spawn paths record. Without
		// this the next tick, which can come before the worker writes its
		// `claude_session_id`, would open the same row a second time.
		if row.Label != "" {
			key := labelKey(row.Label)
			if prev, ok := out[key]; !ok || at.After(prev) {
				out[key] = at
			}
		}
	}
	return out
}

// labelKey is the recently-spawned map's key for a spawn found by label.
//
// The prefix keeps a label from ever colliding with a session id. The key carries
// no vault — the ledger row does not record one — so two same-named tasks in
// different vaults suppress each other for one window. That errs toward not
// opening, never toward a duplicate.
func labelKey(label string) string {
	return "label:" + label
}

// neverOpened reports whether the row is a flagged row that has never had a
// session: the predicate read its liveness as `none` because there was no id to
// probe, and routed it to a fresh open (`create`) or the operator's own
// workspace (`direct`).
//
// ⚠️ The empty session id is load-bearing. Under `--no-liveness` the predicate
// reports `none` for EVERY row, including ones that do have a session — and
// opening those fresh would start a second conversation beside a live one.
//
// ⚠️ Tasks only. For a goal or topic the predicate returns `create` whenever its
// manager chain is live OR unreadable (it routes to `open-manager` only on a
// proven-dead chain), so admitting a goal/topic `none`+`create` row would open a
// duplicate manager beside a live one.
func neverOpened(row FlaggedRow) bool {
	if row.Kind != "task" || row.Liveness != "none" || row.SessionID != "" {
		return false
	}
	return row.Branch == "create" || row.Branch == "direct"
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
	// Eligible: ABSENT (a session that died — resume it) or never opened (no
	// session at all — open it fresh, see neverOpened). Operator ruling
	// 2026-11-15: flagged rows open automatically; before this, a flagged row that
	// had never had a session was skipped on every tick, because only ABSENT was
	// admitted and such a row reads `none`.
	//
	// ⚠️ This used to skip only LIVE, which made `none` and `UNKNOWN` eligible
	// too — a row the predicate could not classify would have been opened, and a
	// row already running would have been opened a second time. The spec says
	// "for each ABSENT row"; anything the predicate could not prove absent is
	// left alone. `none` is what `--no-liveness` reports for every row, so the
	// permissive form would have reopened the entire fleet.
	fresh := neverOpened(row)
	if row.Liveness != "ABSENT" && !fresh {
		return reasonNotAbsent, false, true
	}
	if row.Branch == "jump" {
		return reasonAlreadyLive, false, true
	}
	key := row.SessionID
	if fresh {
		key = labelKey(row.Name)
	}
	if at, ok := recent[key]; ok && now.Sub(at) < recentlySpawnedWindow {
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
