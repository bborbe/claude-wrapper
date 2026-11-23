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

	// spawnNotLandedWindow is how long after a spawn the tick waits before
	// declaring that the spawn never landed. A child that exits without writing
	// the row's `claude_session_id` inside this window is `spawn-not-landed` and
	// is retried once, rather than held for the full 90-min recently-spawned
	// window. Measured 2026-11-17: a spawn that never landed was held silently
	// for 2 h 20 min because nothing reported the failed first spawn.
	spawnNotLandedWindow = 10 * time.Minute

	// idleNudgeWindow is how long a nudged session is left alone before it may be
	// nudged again. A worker that closes its turn at once — nothing left it can do
	// — would otherwise be prodded on every tick, spending a turn each time.
	idleNudgeWindow = 30 * time.Minute

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
	reasonBlocked   = "blocked"
	reasonNotAbsent = "not-absent"
	// reasonChainUnreadable marks a never-opened goal or topic whose manager-chain
	// probe could not read the registry. A failed read is not evidence of absence,
	// so the row is skipped rather than opened — the same rule the predicate
	// follows when it refuses to prove a chain dead.
	reasonChainUnreadable = "chain-unreadable"
	// reasonNotAbsentDead is `not-absent` for a row whose session the LEDGER
	// reports as ended. It exists because `not-absent` is a claim about the
	// session ("the predicate could not prove it absent"), and when the ledger
	// says the session already ended that claim is false. Measured 2026-11-18:
	// 19 of 39 rows skipped as `not-absent` had no entry in the wrapper's own
	// roster, so the reason was misreporting the fleet's state.
	//
	// ⚠️ It changes the LABEL only, never the decision. Treating these rows as
	// eligible was the other candidate and was declined (operator, 2026-11-18):
	// `decision`'s conservatism is deliberate — the permissive form reopens the
	// entire fleet — so a terminal ledger status alone must not make a row
	// eligible. What it must do is stop the line asserting the session is alive.
	reasonNotAbsentDead   = "not-absent-dead"
	reasonAlreadyLive     = "already-live"
	reasonRecentlySpawned = "recently-spawned"
	// reasonSpawnNotLanded marks a spawn whose child exited inside the 10-min
	// window without writing the row's claude_session_id. It is retried once.
	reasonSpawnNotLanded = "spawn-not-landed"
	reasonRowBudget      = "row budget"
	reasonCapsRefused    = "caps refused"
	reasonRateRefused    = "rate refused"
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
	// Binds holds `key=<action>:<sid>` for every row this tick opened whose
	// endpoint bind wrote a page. It is the tick-path artifact for the endpoint's
	// own write, and the only one that NAMES the session: without it a
	// tick-driven open leaves nothing a reader can check, so a bind criterion
	// could only be satisfied by a hand-run POST /api/1.0/open.
	Binds []string
	// Nudged holds the key of every row whose idle, already-held child this tick
	// sent a new turn to. It is not Opened: no process was spawned.
	Nudged []string
	// Refused holds `key: reason` for every row the tick would not open.
	Refused []string
	// Skipped holds `key: reason` for every row that was eligible but not due.
	Skipped []string
	// Considered is how many rows the tick was handed. It is the number the
	// three buckets must add up to; without it a dropped row is invisible.
	Considered int
	// ResumeDelays holds `key=<delay>` for every row this tick opened, where the
	// delay is measured from the ledger's own `ended_at` — the instant the turn
	// that produced the stop closed. It is the per-resume measurement: the row's
	// dead time between becoming able to continue and being resumed.
	//
	// A row with no `ended_at` is ABSENT rather than zero. An absent entry means
	// the delay was not measurable; a zero would read as an instant resume, which
	// is the one reading this measurement exists to distinguish.
	ResumeDelays []string
	// ResumeDelaysWaited holds `key=<delay>` for rows this tick opened that it
	// had held back at least once after their stop — the "nothing to do" class.
	// Their delay is measured from the last skip, not from `ended_at`, so it
	// bounds the real wait to one tick instead of counting idle time.
	//
	// It is a SEPARATE field, never folded into ResumeDelays: the population the bar applies to is
	// the could-continue rows, and a p90 over a population that mixes the two
	// cannot be compared to the two-minute bar the criterion names.
	ResumeDelaysWaited []string
	// LiveCountSource names the channel the spawn caps counted from this tick
	// (`ledger`, `registry`, `heartbeat` or `none`). Empty when the caps cannot
	// report one.
	//
	// It exists because a `caps refused` row is only diagnosable if the line says
	// WHICH channel was asked: measured 2026-11-17, 46 rows refused across 12
	// controller outages, every one reading "the live-worker count could not be
	// taken" with no way to tell whether a fallback was even attempted.
	LiveCountSource string
	// RowsSource names where this tick's eligible rows came from — `vault-api`
	// since the source swap. A tick that scans pages cannot emit it.
	RowsSource string
	// ProvenanceCounts counts this tick's considered rows by provenance class —
	// `operator`, `agent`, `unrecorded`. It is what makes the three states
	// distinguishable in ONE line: a refusal token marks only the refused rows, and
	// an operator-set row is opened with no provenance marker of its own. Rows whose
	// source computed no class (the exec source, a fixture) are not counted.
	ProvenanceCounts map[Provenance]int
}

// liveCountSourceReporter is the narrow half of SpawnCaps the tick logs.
//
// Optional on purpose: a test fake that only enforces caps must not have to
// implement a reporting method it does not care about, and a caps value that does
// not report one simply leaves the field off the line.
type liveCountSourceReporter interface {
	LiveCountSource(ctx context.Context) string
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
	if len(r.Nudged) > 0 {
		line += fmt.Sprintf(" nudged=%d nudged_rows=%s", len(r.Nudged), strings.Join(r.Nudged, ","))
	}
	// `binds_rows=` carries the endpoint's own page write, per opened row, and
	// names the session it bound. A tick that opened a manager but wrote no page
	// has no entry here, so a missing bind is visible rather than inferred from
	// the absence of a line.
	if len(r.Binds) > 0 {
		line += fmt.Sprintf(" binds=%d binds_rows=%s", len(r.Binds), strings.Join(r.Binds, ","))
	}
	if counts := formatCounts(r.Refused); counts != "" {
		line += " refused_by_reason=" + counts
	}
	if counts := formatCounts(r.Skipped); counts != "" {
		line += " skipped_by_reason=" + counts
	}
	if r.LiveCountSource != "" {
		line += " live_count_source=" + r.LiveCountSource
	}
	// `rows_source=` names where this tick's eligible rows came from. It is the
	// provenance marker for the source swap: a tick that still scans pages cannot
	// emit it, so a no-op build is distinguishable from a real one in the log.
	if r.RowsSource != "" {
		line += " rows_source=" + r.RowsSource
	}
	// `provenance=` names all three states in one line. The refusal tokens below it
	// mark only the refused rows; without this field an operator-set row is visible
	// only as an `opened=` entry with no provenance of its own, so a reader cannot
	// tell "the operator set this" from "nothing checked".
	if counts := formatProvenance(r.ProvenanceCounts); counts != "" {
		line += " provenance=" + counts
	}
	// `resume_delay=` is the per-resume measurement, and it sits on the summary
	// line for the same reason `rows=` does: a row's dead time has to be readable
	// next to the tick that ended it, or the delay cannot be attributed to a tick.
	if len(r.ResumeDelays) > 0 {
		line += " resume_delay=" + strings.Join(r.ResumeDelays, ",")
	}
	// Emitted separately so a reader can compute the could-continue p90 without
	// the idle rows: the two-minute bar applies to `resume_delay=` alone.
	if len(r.ResumeDelaysWaited) > 0 {
		line += " resume_delay_waited=" + strings.Join(r.ResumeDelaysWaited, ",")
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

// formatProvenance renders the three provenance classes in a fixed order so the
// line is stable across runs of the same tick. A class with no rows is omitted.
func formatProvenance(counts map[Provenance]int) string {
	if len(counts) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, class := range AvailableProvenances {
		if count := counts[class]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", class, count))
		}
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
	// RowsSource names the source in the tick line (`vault-api`), so a reader can
	// tell a tick that asked the index from one that scanned pages.
	RowsSource string
	// Open is the open decision and executor. When set, every row that passes the
	// tick's gates is handed to OpenTick as one batch, so the tick and
	// POST /api/1.0/open cannot route the same row two ways. The daemon always
	// sets it.
	Open OpenExecutor
	// Reopener is the per-row fallback for callers that wire no Open executor
	// (the pre-006 unit fixtures). It never runs in the daemon, where Open is set.
	Reopener FlaggedReopener
	Caps     SpawnCaps
	// Chain answers whether a never-opened goal or topic already has a live
	// manager session. Nil disables goal/topic admission — the tick then opens
	// tasks only, which is the pre-2026-11-19 behaviour and what a test that
	// predates this field gets.
	Chain ManagerChainProbe
	// Ledger supplies the spawn times the recently-spawned guard reads. A nil
	// ledger disables that half of the guard rather than refusing every row.
	Ledger LedgerReader
	// Parked is the park gate. A nil gate is inactive, which is what a test that
	// predates it gets and what the daemon gets when the state directories cannot
	// be resolved.
	Parked ParkedHold
	// Sessions is the wrapper's own durable child table — the half of the
	// recently-spawned guard that still answers while the attention-controller is
	// down. Nil resolves it from the state directory, which `Home` and `Getenv`
	// scope: a spec that leaves both zero reads the host's real `~/.local/state`,
	// so every spec sets them.
	Sessions SessionStore
	// Home overrides the home directory the state directory resolves under, and
	// Getenv overrides the environment lookup (defaulting to os.Getenv). Both exist
	// so a spec cannot silently read the host's child table.
	Home   string
	Getenv func(string) string
	// CacheDir is where the last good row set is persisted between restarts.
	// Empty resolves it from the daemon's state directory, which `Home` and
	// `Getenv` scope, exactly as the session store resolves its own.
	CacheDir string
	Now      libtime.CurrentDateTimeGetter
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
	// spawnNotLandedRetried records, per label, that a `spawn-not-landed` retry
	// has already been issued. It makes the retry exactly once: without it every
	// tick inside the 90-min window would retry the same failed spawn again.
	// Guarded by openedMutex.
	spawnNotLandedRetried map[string]bool

	// skippedMutex guards skipped.
	skippedMutex sync.Mutex
	// skipped records, in process, when this tick last held a row back, keyed by
	// session id. It is what separates the two resume-delay classes: a row with
	// no entry here was resumable at its stop (the could-continue class), while
	// a row with one only became able somewhere between that skip and its open.
	//
	// ⚠️ In process only, and that is deliberate. A restart empties it, so the
	// first resume after a restart is measured from `ended_at` and lands in the
	// could-continue class — the honest reading, since a restart genuinely does
	// cost that row the restart's duration.
	skipped map[string]time.Time

	// Idle re-drives a row whose child this process holds idle. Nil disables the
	// branch: such a row is skipped `not-absent`, as before.
	Idle FlaggedIdleNudger
	// nudgedMutex guards nudged.
	nudgedMutex sync.Mutex
	// nudged maps a session id to when this process last nudged it.
	nudged map[string]time.Time
}

// idleNudgeDue reports whether a not-absent row should be nudged now.
func (t *FlaggedTick) idleNudgeDue(ctx context.Context, row FlaggedRow, now time.Time) bool {
	if t.Idle == nil {
		return false
	}
	t.nudgedMutex.Lock()
	at, ok := t.nudged[row.SessionID]
	t.nudgedMutex.Unlock()
	if ok && now.Sub(at) < idleNudgeWindow {
		return false
	}
	return t.Idle.HoldsIdleChild(ctx, row)
}

// rememberNudged records a session this process just nudged.
func (t *FlaggedTick) rememberNudged(sessionID string, at time.Time) {
	t.nudgedMutex.Lock()
	defer t.nudgedMutex.Unlock()
	if t.nudged == nil {
		t.nudged = map[string]time.Time{}
	}
	t.nudged[sessionID] = at
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

// spawnNotLandedDue reports whether the row's spawn never landed: this tick
// opened it more than spawnNotLandedWindow ago, has not retried it yet, and the
// child table shows its child has exited while the row still has no session id.
//
// The exit fact is read from the child table's pid, never from the cached row —
// the row cache can be 65 minutes old, so a cached `recently-spawned` reading
// cannot tell a slow start from a dead one.
func (t *FlaggedTick) spawnNotLandedDue(ctx context.Context, row FlaggedRow) bool {
	if t.Sessions == nil || row.SessionID != "" {
		return false
	}
	key := labelKey(row.Name)
	t.openedMutex.Lock()
	at, opened := t.opened[key]
	retried := t.spawnNotLandedRetried[key]
	t.openedMutex.Unlock()
	if !opened || retried {
		return false
	}
	if t.now().Sub(at) < spawnNotLandedWindow {
		return false
	}
	records, err := t.Sessions.List(ctx)
	if err != nil {
		return false
	}
	matched := false
	for _, record := range records {
		if record.Options.Label != row.Name {
			continue
		}
		matched = true
		// Any live child is a slow start, not a failed spawn; the normal
		// recently-spawned guard covers it. Checking every matching record, not
		// the first in directory order, stops a retry beside a live child.
		if pidAlive(record.ChildPID) {
			return false
		}
	}
	return matched
}

// rememberSpawnNotLanded records that the row's single retry has been issued.
func (t *FlaggedTick) rememberSpawnNotLanded(row FlaggedRow) {
	t.openedMutex.Lock()
	defer t.openedMutex.Unlock()
	if t.spawnNotLandedRetried == nil {
		t.spawnNotLandedRetried = map[string]bool{}
	}
	t.spawnNotLandedRetried[labelKey(row.Name)] = true
}

// rememberSkipped records that this tick held the row back, at `at`.
func (t *FlaggedTick) rememberSkipped(row FlaggedRow, at time.Time) {
	if row.SessionID == "" {
		return
	}
	t.skippedMutex.Lock()
	defer t.skippedMutex.Unlock()
	if t.skipped == nil {
		t.skipped = map[string]time.Time{}
	}
	t.skipped[row.SessionID] = at
}

// skippedAt returns when this tick last held the row back, if it did.
func (t *FlaggedTick) skippedAt(sessionID string) (time.Time, bool) {
	t.skippedMutex.Lock()
	defer t.skippedMutex.Unlock()
	at, ok := t.skipped[sessionID]
	return at, ok
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
	// persistedAt is the read time of a row set loaded from disk, and fromDisk
	// marks that no read has landed yet in this process. Together they let the
	// first tick after a start say what it is serving instead of pretending the
	// set is fresh — and stop saying it the moment a real read replaces it.
	persistedAt time.Time
	fromDisk    bool
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
	c.fromDisk = false
}

// seedFromDisk primes the cache with the row set a previous process persisted.
//
// It never overwrites a row set a read has already landed: the tick may load the
// file while the first read is in flight, and a fresh read is always the better
// answer. An empty set is accepted deliberately — a predicate that legitimately
// found no eligible rows is a real answer, and skipping the first tick for it
// would cost exactly the dead tick this exists to remove.
func (c *flaggedRowCache) seedFromDisk(rows []FlaggedRow, readAt time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.set {
		return
	}
	c.rows, c.persistedAt, c.fromDisk, c.set = rows, readAt, true, true
}

// persistedAge reports when the row set on disk was read, and whether the cache
// is still serving it. It is false as soon as a real read lands.
func (c *flaggedRowCache) persistedAge() (time.Time, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.persistedAt, c.fromDisk
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
	stateDir := t.flaggedCacheDir(ctx)
	t.seedCacheFromDisk(cache, stateDir, interval)
	go t.refreshLoop(ctx, cache, interval, stateDir)
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

// flaggedCacheDir resolves the directory the row set is persisted in.
//
// Empty means "not persisted": a daemon whose state directory cannot be resolved
// keeps the pre-existing behaviour (one skipped tick per start) rather than
// inventing a location. It is logged once, at start, so a silent loss of the
// optimisation is visible.
func (t *FlaggedTick) flaggedCacheDir(ctx context.Context) string {
	if t.CacheDir != "" {
		return t.CacheDir
	}
	sessions, err := SessionRoot(ctx, "", t.Home, t.Getenv)
	if err != nil {
		t.logf("flagged-tick: state dir unavailable, row cache is not persisted: %v", err)
		return ""
	}
	// The sessions root, deliberately: it is the one directory the daemon already
	// owns and resolves identically in both flag and default form. A file there
	// cannot be mistaken for a session — the store lists directories only.
	return sessions
}

// cacheMaxAge bounds how old a persisted row set may be and still be served.
//
// It is the same envelope the tick's own decision age already assumes: a read
// that started just before a spawn, then a full read and an interval after it —
// 2 × predicateTimeout + one interval, which is what `recentlySpawnedWindow`
// (90 minutes) is sized against. A set older than that is refused, so a daemon
// that was down for hours cannot decide the fleet's state on a stale reading.
func (t *FlaggedTick) cacheMaxAge(interval time.Duration) time.Duration {
	timeout := t.ReadTimeout
	if timeout <= 0 {
		timeout = predicateTimeout
	}
	return 2*timeout + interval
}

// seedCacheFromDisk primes the cache with the previous process's last good read.
//
// A refused set is logged with its age and the bound, so "the cache existed but
// was too old" is distinguishable from "there was no cache" in the same log.
func (t *FlaggedTick) seedCacheFromDisk(
	cache *flaggedRowCache, stateDir string, interval time.Duration,
) {
	if stateDir == "" {
		return
	}
	rows, readAt, ok := loadFlaggedCache(stateDir)
	if !ok {
		return
	}
	age := t.now().Sub(readAt)
	if age < 0 || age > t.cacheMaxAge(interval) {
		t.logf("flagged-tick cache refused: %d rows read %s ago, older than the %s bound",
			len(rows), age.Truncate(time.Second), t.cacheMaxAge(interval))
		return
	}
	cache.seedFromDisk(rows, readAt)
	t.logf("flagged-tick cache restored: %d rows read %s ago", len(rows), age.Truncate(time.Second))
}

// refreshLoop keeps the row set warm, off the tick's critical path.
func (t *FlaggedTick) refreshLoop(
	ctx context.Context, cache *flaggedRowCache, interval time.Duration, stateDir string,
) {
	for {
		started := t.now()
		rows, err := t.readRows(ctx)
		// Measure even a failed read: a kill at predicateTimeout is the worst case
		// the cost measurement exists to catch, so dropping it would bias the p95
		// down exactly on the slow reads.
		cache.store(rows, err, t.now().Sub(started))
		if err == nil {
			// Persist only a successful read, and treat a failed write as a lost
			// optimisation rather than a tick failure: the in-process cache still
			// serves this process, and only the next start pays for it.
			if saveErr := saveFlaggedCache(ctx, stateDir, rows, started); saveErr != nil {
				t.logf("flagged-tick cache save failed: %v", saveErr)
			}
		}
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
	if at, fromDisk := cache.persistedAge(); fromDisk {
		// The first tick after a start. Naming the set and its age is what makes
		// the restart cheap AND visible: the tick opens on the last good rows
		// instead of dying, and a set old enough to be wrong is a number the
		// operator can read rather than a silent assumption.
		t.logf("flagged-tick served persisted rows: %d rows read %s ago (no read since start)",
			len(rows), t.now().Sub(at).Truncate(time.Second))
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

// rowAction is what the pre-cap guards decide for one row.
type rowAction int

const (
	// rowEligible means the row may proceed to the caps and the reopener.
	rowEligible rowAction = iota
	// rowSkipped means the row is eligible but not due; it is a skip, not a
	// refusal, and it spends no reservation.
	rowSkipped
	// rowRefused means the predicate's own provenance verdict refuses the row.
	rowRefused
	// rowNudge means the row reads not-absent only because this process holds its
	// child idle; it gets a new turn on that child, and spends no reservation.
	rowNudge
)

// classify applies the guards that run before any cap is consulted: the
// eligibility decision, then the park gate.
//
// The order is load-bearing. Provenance is checked before liveness so an
// agent-set flag is reported as REFUSED rather than skipped, and the park gate
// runs only for a row the decision would otherwise open. A hold is a SKIP, not
// a refusal: the row is eligible but the observable its last session parked on
// has not moved, so it must not spend a reservation, must not halt the opening
// pass, and must not count against the per-tick row budget.
func (t *FlaggedTick) classify(
	ctx context.Context,
	row FlaggedRow,
	recent map[string]time.Time,
	dead map[string]struct{},
	childLive map[string]bool,
	now time.Time,
) (string, rowAction, bool) {
	// Provenance and blocked_by are checked FIRST, exactly as `decision` orders
	// them: a refused or blocked row must not spend a registry probe (rowFreshness)
	// or a retry (spawnNotLandedDue) it will never use, and must report its own
	// reason rather than `chain-unreadable`.
	if len(row.Refused) > 0 {
		return row.Refused[0], rowRefused, false
	}
	if len(row.BlockedBy) > 0 {
		return reasonBlocked, rowSkipped, false
	}
	fresh, chainReason, chainSkip := t.rowFreshness(ctx, row)
	if chainSkip {
		return chainReason, rowSkipped, false
	}
	// A spawn that never landed is retried once, before the recently-spawned
	// guard can hold it for the full 90-min window. The retry is REMEMBERED by
	// the caller only after Reopen actually succeeds: a row parked, or refused by
	// caps/rate, must not burn its one retry without a spawn.
	retryNotLanded := t.spawnNotLandedDue(ctx, row)
	if retryNotLanded {
		t.logf("flagged-tick: spawn-not-landed row=%s — retrying once", row.Name)
	}
	if reason, refused, skipped := decision(row, recent, dead, childLive, now, fresh, retryNotLanded); refused || skipped {
		if refused {
			return reason, rowRefused, false
		}
		skipReason, skipAction := t.classifySkip(ctx, row, reason, now)
		return skipReason, skipAction, false
	}
	if t.Parked != nil {
		if reason, held := t.Parked.Hold(ctx, row); held {
			return reason, rowSkipped, false
		}
	}
	return "", rowEligible, retryNotLanded
}

// rowFreshness decides whether a row with no session should be opened fresh.
//
// A task is fresh under neverOpened. A goal or topic is NOT: the predicate
// returns `create` for a live manager chain as well as a dead one, so the tick
// probes the live registry itself. Live → skip (a duplicate manager would start
// beside it); unreadable → skip (a failed read is not evidence of absence);
// dead → open.
func (t *FlaggedTick) rowFreshness(
	ctx context.Context,
	row FlaggedRow,
) (fresh bool, reason string, skip bool) {
	if neverOpened(row) {
		return true, "", false
	}
	if !neverOpenedManager(row) {
		return false, "", false
	}
	if t.Chain == nil {
		return false, reasonChainUnreadable, true
	}
	live, err := t.Chain.ChainLive(ctx, row)
	if err != nil {
		return false, reasonChainUnreadable, true
	}
	if live {
		return false, reasonAlreadyLive, true
	}
	return true, "", false
}

// isNotAbsentReason reports whether the reason is one of the two
// "the predicate could not prove the session gone" variants. Both must reach
// the idle-nudge - see the comment inside classifySkip.
func isNotAbsentReason(reason string) bool {
	return reason == reasonNotAbsent || reason == reasonNotAbsentDead
}

// classifySkip turns a not-absent skip into a nudge when this process holds the
// row's child idle. The park gate still applies: a parked row is not nudged.
func (t *FlaggedTick) classifySkip(
	ctx context.Context, row FlaggedRow, reason string, now time.Time,
) (string, rowAction) {
	// BOTH not-absent reasons must reach the idle-nudge. reasonNotAbsentDead is
	// the same condition reported truthfully - the predicate could not prove the
	// session absent, and the ledger says the session ended - and a session that
	// finished its turn but is still held idle is EXACTLY what the nudge exists
	// for. Gating on reasonNotAbsent alone silently disabled the nudge for every
	// terminal-status session, which is the whole population it was built for: a
	// finished turn writes a terminal ledger status, so those rows would skip
	// without a nudge and stay dark until a restart. Caught in review 2026-11-19.
	if !isNotAbsentReason(reason) || !t.idleNudgeDue(ctx, row, now) {
		return reason, rowSkipped
	}
	if t.Parked != nil {
		if parkReason, held := t.Parked.Hold(ctx, row); held {
			return parkReason, rowSkipped
		}
	}
	return "", rowNudge
}

// nudgeRow sends the row's work turn to its idle child and records the outcome.
func (t *FlaggedTick) nudgeRow(
	ctx context.Context, row FlaggedRow, now time.Time, result *FlaggedTickResult,
) {
	if err := t.Idle.Nudge(ctx, row); err != nil {
		result.Refused = append(result.Refused, row.Key()+": nudge failed: "+err.Error())
		return
	}
	t.rememberNudged(row.SessionID, now)
	result.Nudged = append(result.Nudged, row.Key())
}

// countProvenance tallies the considered rows by provenance class. A row whose
// source computed no class is skipped rather than guessed at.
func countProvenance(result *FlaggedTickResult, rows []FlaggedRow) {
	for _, row := range rows {
		if row.Provenance == "" {
			continue
		}
		if result.ProvenanceCounts == nil {
			result.ProvenanceCounts = map[Provenance]int{}
		}
		result.ProvenanceCounts[row.Provenance]++
	}
}

// tickFromRows is the decision pass, separated from the read so the running loop
// can feed it a cached row set.
func (t *FlaggedTick) tickFromRows(
	ctx context.Context, rows []FlaggedRow,
) FlaggedTickResult {
	var result FlaggedTickResult
	ordered := orderFlaggedRows(rows)
	result.Considered = len(ordered)
	// Counted before any gate: the three states are a property of the row set, and a
	// reader must be able to tell an operator-set row (opened, no marker of its own)
	// from an agent-set or unattributed one (refused) in the same line.
	countProvenance(&result, ordered)
	// Named once per tick, before any refusal: a `caps refused` row is
	// undiagnosable without it.
	if reporter, ok := t.Caps.(liveCountSourceReporter); ok {
		result.LiveCountSource = reporter.LiveCountSource(ctx)
	}
	result.RowsSource = t.RowsSource
	// One ledger read serves both the recently-spawned guard and the resume-delay
	// measurement: two reads of the same store in one tick could disagree, and the
	// measurement would then describe a fleet state the decision never saw.
	ledger := t.ledgerRows(ctx)
	recent := recentlySpawned(ledger)
	// The child table answers when the ledger cannot, so a controller outage does
	// not empty the double-open guard.
	t.mergeChildSpawnTimes(ctx, recent)
	t.mergeOpened(recent)
	ended := endedAtBySession(ledger)
	// Which sessions the ledger says are over. A terminal status alone does not
	// admit a row — `childLive` must also prove the process gone. See decision().
	dead := endedSessions(ledger)
	// One session-store read per tick: a 50-row batch must not List 50 times.
	childLive := t.childLiveness(ctx)
	now := t.now()
	// halted is the fleet-level refusal that ended this tick's opening pass. It
	// is a reason string, not a bool, because the rows it leaves behind are still
	// recorded — as unconsidered — so the buckets add up to Considered.
	halted := ""
	// pending is the batch the tick has decided to open. Every gate above has
	// already run; nothing is added after a cap or rate refusal. The batch is
	// opened in one OpenTick call so the tick cannot route a row differently from
	// POST /api/1.0/open.
	var pending []FlaggedRow
	var pendingRetry []bool
	for _, row := range ordered {
		if halted != "" {
			result.Refused = append(result.Refused, row.Key()+": "+halted)
			continue
		}
		if len(pending)+len(result.Nudged) >= maxRowsPerTick {
			result.Refused = append(result.Refused, row.Key()+": "+reasonRowBudget)
			continue
		}
		reason, action, retryNotLanded := t.classify(ctx, row, recent, dead, childLive, now)
		switch action {
		case rowSkipped:
			result.Skipped = append(result.Skipped, row.Key()+": "+reason)
			// Remember WHEN this row was last held back, so a later open can tell
			// "it could continue at its stop" from "it only became able later".
			// Without this the two classes are indistinguishable and the delay
			// measurement mixes them — see ResumeDelaysWaited.
			t.rememberSkipped(row, now)
			continue
		case rowRefused:
			result.Refused = append(result.Refused, row.Key()+": "+reason)
			continue
		case rowNudge:
			t.nudgeRow(ctx, row, now, &result)
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
		// The third argument is the row's own provenance verdict, never a constant.
		// `decision` has already refused every row carrying a `Refused` entry, so
		// what arrives here is operator-flagged or legacy-operator-opened and counts
		// as operator-named (operator ruling 2026-11-17): it may open up to the hard
		// limit. Passing `false` refused every one of them at the soft cap — the
		// fleet stood at 51 live against 50 allowed on 2026-11-18 00:58 and 28 rows
		// went unconsidered.
		// The cap is charged against the mode the row will ACTUALLY open in, which
		// follows its role, not its stored `mode:`. A `role: agent` row stored
		// `interactive` opens headless (matrix Rule 2), so charging it to the
		// interactive sub-cap refused it — and halted the batch — whenever that cap
		// sat at its hard limit, which is exactly the stale-mode row this change
		// exists to route correctly.
		if err := t.Caps.CheckWorker(ctx, row.CapMode(), row.OperatorNamed()); err != nil {
			halted = reasonUnconsidered
			result.Refused = append(result.Refused, row.Key()+": "+reasonCapsRefused+": "+err.Error())
			continue
		}
		// The start slot is reserved exactly once per row, by whichever opener runs.
		// With an executor wired, the executor's own caps guard reserves it
		// (enforceSpawnCaps → ReserveStart) as it opens the row; reserving here too
		// spent two slots per tick-opened row, so a batch of N counted as 2N against
		// the start rate and roughly doubled the tick's runtime past its 15-minute
		// interval. On the legacy path (no executor) nothing else reserves, so the
		// tick keeps doing it and keeps halting on a rate refusal as documented.
		if t.Open == nil {
			if err := t.Caps.ReserveStart(ctx); err != nil {
				halted = reasonUnconsidered
				result.Refused = append(result.Refused, row.Key()+": "+reasonRateRefused+": "+err.Error())
				continue
			}
		}
		pending = append(pending, row)
		pendingRetry = append(pendingRetry, retryNotLanded)
	}
	t.openPending(ctx, pending, pendingRetry, ended, now, &result)
	return result
}

// openPending opens the batch the tick admitted, once through OpenTick when an
// executor is wired.
func (t *FlaggedTick) openPending(
	ctx context.Context,
	pending []FlaggedRow,
	retry []bool,
	ended map[string]time.Time,
	now time.Time,
	result *FlaggedTickResult,
) {
	if len(pending) == 0 {
		return
	}
	if t.Open == nil {
		t.reopenPending(ctx, pending, retry, ended, now, result)
		return
	}
	rows := make([]OpenRow, len(pending))
	for i, row := range pending {
		rows[i] = openRowFor(row)
	}
	verdicts := t.Open.OpenTick(ctx, rows)
	for i, row := range pending {
		if i >= len(verdicts) {
			// The executor stopped early (a cancelled context). The row was
			// admitted but never opened, so it is accounted for, not dropped.
			result.Refused = append(result.Refused, row.Key()+": "+reasonUnconsidered)
			continue
		}
		t.recordOpenVerdict(ctx, row, retry[i], verdicts[i], ended, now, result)
	}
}

// recordOpenVerdict folds one open verdict into the tick's three buckets.
//
// A jump or live verdict is the decision's "already reachable" answer; on the
// tick path the executor never activates a pane, so it is a skip under the
// existing already-live reason. A refusal keeps its reason token so the summary
// line's refused_by_reason= counts it under the same token the open log names.
func (t *FlaggedTick) recordOpenVerdict(
	ctx context.Context,
	row FlaggedRow,
	retryNotLanded bool,
	verdict OpenVerdict,
	ended map[string]time.Time,
	now time.Time,
	result *FlaggedTickResult,
) {
	switch verdict.Branch {
	case OpenBranchResume, OpenBranchCreate:
		t.commitOpened(ctx, row, retryNotLanded, now)
		result.Opened = append(result.Opened, row.Key())
		if verdict.Bind != nil {
			result.Binds = append(result.Binds, fmt.Sprintf(
				"%s=%s:%s", row.Key(), verdict.Bind.Action, verdict.Bind.SessionID,
			))
		}
		t.recordResumeDelay(result, row, ended, now)
	case OpenBranchJump, OpenBranchLive, OpenBranchYours:
		result.Skipped = append(result.Skipped, row.Key()+": "+reasonAlreadyLive)
		t.rememberSkipped(row, now)
	case OpenBranchRefused:
		result.Refused = append(result.Refused, row.Key()+": "+verdict.Reason+": "+verdict.Message)
	default:
		result.Refused = append(result.Refused, row.Key()+": spawn failed: open returned branch "+verdict.Branch)
	}
}

// reopenPending is the per-row fallback for a tick with no Open executor.
func (t *FlaggedTick) reopenPending(
	ctx context.Context,
	pending []FlaggedRow,
	retry []bool,
	ended map[string]time.Time,
	now time.Time,
	result *FlaggedTickResult,
) {
	for i, row := range pending {
		if err := t.Reopener.Reopen(ctx, row); err != nil {
			result.Refused = append(result.Refused, row.Key()+": spawn failed: "+err.Error())
			continue
		}
		t.commitOpened(ctx, row, retry[i], now)
		result.Opened = append(result.Opened, row.Key())
		t.recordResumeDelay(result, row, ended, now)
	}
}

// commitOpened records a successful open: the retry (only now that the spawn
// succeeded), the in-process open time, and the park baseline. Extracted from
// tickFromRows so its branch count stays under the cognitive-complexity gate.
func (t *FlaggedTick) commitOpened(
	ctx context.Context,
	row FlaggedRow,
	retryNotLanded bool,
	now time.Time,
) {
	if retryNotLanded {
		t.rememberSpawnNotLanded(row)
	}
	if neverOpened(row) || neverOpenedManager(row) {
		t.rememberOpened(row, now)
	}
	// The reopen succeeded, so the park gate may commit the baseline a movement
	// produced. This is the only place it is committed — a row refused by caps or
	// rate above never reaches here and keeps its old fingerprint, so it retries.
	if t.Parked != nil {
		t.Parked.Opened(ctx, row)
	}
}

// recordResumeDelay records one row this tick just opened into exactly one of
// the two delay populations.
//
// A row the tick never held back is measured from the ledger's own `ended_at` —
// the instant the turn that produced the stop closed — and goes to
// ResumeDelays. A row it DID hold back goes to ResumeDelaysWaited instead,
// measured from that last skip; see the branch below for why the two differ.
//
// A row with no ended_at — never stopped, or a spawn row whose turn never closed
// — is omitted rather than recorded as zero: a zero would read as an instant
// resume, which is the one reading this measurement exists to distinguish. A
// future ended_at is skipped for the same reason, since clock skew would
// otherwise print a negative delay.
func (t *FlaggedTick) recordResumeDelay(
	result *FlaggedTickResult,
	row FlaggedRow,
	ended map[string]time.Time,
	now time.Time,
) {
	at, ok := ended[row.SessionID]
	if !ok || at.After(now) {
		return
	}
	// The tick held this row back at least once after its stop, so it did NOT
	// become able at the stop — it became able somewhere between that last skip
	// and this open. Measuring from `ended_at` would count the whole idle wait as
	// resume latency, which is what made the p90 unreadable against the two-minute bar: measured
	// 2026-11-18, the all-rows p90 was 16.1 min against a could-continue p90 of
	// 16.2 min, with single rows reaching 21 h. Reporting it from the skip bounds
	// the real delay to one tick and keeps it out of the could-continue population.
	if skipped, had := t.skippedAt(row.SessionID); had && skipped.After(at) && !skipped.After(now) {
		result.ResumeDelaysWaited = append(result.ResumeDelaysWaited,
			row.Key()+"="+now.Sub(skipped).Round(time.Second).String())
		return
	}
	result.ResumeDelays = append(result.ResumeDelays,
		row.Key()+"="+now.Sub(at).Round(time.Second).String())
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

// ledgerRows reads the ledger once for a tick.
//
// A nil result means the read could not run. Every consumer degrades to its
// empty-map behaviour rather than refusing rows: the store is a safety margin
// here, not a gate, and turning a store outage into a fleet-wide stall is the
// failure this shape exists to avoid.
func (t *FlaggedTick) ledgerRows(ctx context.Context) []LedgerRow {
	if t.Ledger == nil {
		return nil
	}
	rows, err := t.Ledger.Rows(ctx)
	if err != nil {
		// ⚠️ Both consumers go dark on this failure, not just the guard: a tick
		// that opened rows while the store was down logs no `resume_delay=` at
		// all. Naming only the guard would leave an operator reading this line
		// during an outage with no idea the metric is dark too.
		t.logf("flagged-tick: ledger read failed, recently-spawned guard and "+
			"resume_delay measurement inactive: %v", err)
		return nil
	}
	return rows
}

// endedSessions returns the session ids whose ledger status is terminal.
//
// It exists so a skip can be labelled truthfully: `not-absent` asserts the
// predicate could not prove the session gone, and a terminal ledger status is
// proof that it is. Unlike recentlySpawned, this does NOT skip terminal rows —
// they are the whole point.
func endedSessions(rows []LedgerRow) map[string]struct{} {
	// LAST ROW WINS, per the ledger's own contract. "Any terminal row" is wrong:
	// a session that is resumed keeps its id, and its later row re-opens the
	// status to `running`. Scanning for any terminal row would then mark a LIVE
	// session dead and label its skip not-absent-dead, which is the exact lie
	// this function exists to stop. Caught in review 2026-11-19.
	last := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.SessionID == "" {
			continue
		}
		last[row.SessionID] = row.Status
	}
	out := make(map[string]struct{}, len(last))
	for sessionID, status := range last {
		if terminalLedgerStatuses[status] {
			out[sessionID] = struct{}{}
		}
	}
	return out
}

// childLiveness maps a session id to whether its child process is still alive,
// read once per tick from the wrapper's own session store.
//
// It exists for decision()'s "provably gone" test. A terminal ledger entry is
// not proof of death: a session whose turn closed keeps running until it exits
// (measured 2026-11-23 — `done` at 19:14:19, pid alive until 20:13:30). The
// child table is the only source that answers "is the process still there".
//
// A session ABSENT from the map is not provably gone — only an explicit `false`
// admits a row. A registry-only session has already been proved live by the
// registry, and a missing record proves nothing.
//
// A nil Sessions (a fixture that predates the field) yields an empty map, which
// admits nothing; that is the conservative direction.
func (t *FlaggedTick) childLiveness(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if t.Sessions == nil {
		return out
	}
	records, err := t.Sessions.List(ctx)
	if err != nil {
		return out
	}
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		out[record.ID] = pidAlive(record.ChildPID)
	}
	return out
}

// endedAtBySession maps a session id to the instant its last turn ended, which
// is the start of clock for the resume-delay measurement.
//
// It is a package function rather than a method, and `recentlySpawned` lost its
// receiver in the same change, so that ONE ledger read can feed both. That shape
// matches the rest of this file (`formatCounts`, `reasonClass`, `orderFlaggedRows`,
// `labelKey`, `neverOpened` are all package-level), and neither function holds
// state: the alternative is two reads of the same store in one tick, which could
// disagree and leave the measurement describing a fleet state the decision never
// saw.
//
// ⚠️ It must NOT skip terminal ledger rows, unlike recentlySpawned below. A
// closed row is precisely the one being resumed: `ended_at` is written by the
// turn end that closed it, so skipping terminal rows would empty the map for
// exactly the rows this measurement exists for, and the metric would read as
// "never measurable" on a fleet where it should be at its largest.
func endedAtBySession(rows []LedgerRow) map[string]time.Time {
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		if row.SessionID == "" || row.EndedAt == nil {
			continue
		}
		at, parseErr := time.Parse(time.RFC3339, *row.EndedAt)
		if parseErr != nil {
			continue
		}
		if prev, ok := out[row.SessionID]; !ok || at.After(prev) {
			out[row.SessionID] = at
		}
	}
	return out
}

// recentlySpawned maps session id — and, for a never-opened row, labelKey(task
// name) — to its ledger spawn time.
func recentlySpawned(rows []LedgerRow) map[string]time.Time {
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

// mergeChildSpawnTimes adds the spawn times of the wrapper's own live children.
//
// This is what keeps the double-open guard alive while the attention-controller
// is down: the durable per-session `record.json` is on disk, so it answers when
// the ledger cannot. A record whose child pid is gone is not a spawn to stay away
// from, and a record with no pid check would suppress a row forever.
func (t *FlaggedTick) mergeChildSpawnTimes(ctx context.Context, out map[string]time.Time) {
	store := t.Sessions
	if store == nil {
		dir, err := SessionRoot(ctx, "", t.Home, t.Getenv)
		if err != nil {
			t.logf("flagged-tick: session state dir unavailable, recently-spawned guard is ledger-only: %v", err)
			return
		}
		store = NewSessionStore(dir)
	}
	records, err := store.List(ctx)
	if err != nil {
		t.logf("flagged-tick: child table read failed, recently-spawned guard is ledger-only: %v", err)
		return
	}
	for _, record := range records {
		if record.ID == "" || !pidAlive(record.ChildPID) {
			continue
		}
		if prev, ok := out[record.ID]; !ok || record.StartedAt.After(prev) {
			out[record.ID] = record.StartedAt
		}
	}
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

// neverOpenedManager reports whether the row is a goal or topic with no session
// at all, on the predicate's `create` branch.
//
// ⚠️ Unlike neverOpened this is NOT sufficient to open: the predicate returns
// `create` for a goal or topic whose manager chain is live OR unreadable, so the
// tick probes the registry (see rowFreshness) before treating it as fresh.
func neverOpenedManager(row FlaggedRow) bool {
	if row.Kind != "goal" && row.Kind != "topic" {
		return false
	}
	if row.Liveness != "none" || row.SessionID != "" {
		return false
	}
	// ⚠️ `ResolveBranch` returns `open-manager` for a goal or topic whose chain is
	// PROVEN dead — it never returns `create` for these kinds. Requiring `create`
	// alone meant this branch never matched a real source row, so every flagged
	// goal and topic was skipped as `not-absent` and the feature opened nothing.
	// Measured 2026-11-21: two flagged never-opened rows, `branch: open-manager`,
	// `liveness: none`, `session_id: null` — the tick logged `opened=0` and
	// `not-absent:15` on every cycle.
	return row.Branch == "create" || row.Branch == "open-manager"
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
	dead map[string]struct{},
	childLive map[string]bool,
	now time.Time,
	fresh bool,
	retryNotLanded bool,
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
	if row.Liveness != "ABSENT" && !fresh {
		if reason, skip := skipNotAbsent(row, dead, childLive); skip {
			return reason, false, true
		}
	}
	if row.Branch == "jump" {
		return reasonAlreadyLive, false, true
	}
	key := row.SessionID
	if fresh {
		key = labelKey(row.Name)
	}
	if !retryNotLanded {
		if at, ok := recent[key]; ok && now.Sub(at) < recentlySpawnedWindow {
			return reasonRecentlySpawned, false, true
		}
		// A row RE-CREATED for a missing transcript still carries its old session
		// id, so `key` misses it; the fresh spawn filed its ledger row under the
		// label. Check the label too, or such a row whose worker never re-stamps
		// the page is re-created again on every tick.
		if at, ok := recent[labelKey(row.Name)]; ok && now.Sub(at) < recentlySpawnedWindow {
			return reasonRecentlySpawned, false, true
		}
	}
	return "", false, false
}

// skipNotAbsent decides whether a row whose liveness is not ABSENT is skipped,
// and with which reason. It returns skip=false only when the row is provably
// gone — the ledger says its session ended AND the session store holds a child
// record whose pid is dead.
//
// A terminal ledger entry alone is NOT proof of death: a session whose turn
// closed keeps running and reads LIVE until it exits (measured 2026-11-23 —
// `done` at 19:14:19, pid alive until 20:13:30). And a missing child record
// proves nothing — a registry-only session has already been proved live by the
// registry — so only an explicit dead pid admits the row.
func skipNotAbsent(
	row FlaggedRow,
	dead map[string]struct{},
	childLive map[string]bool,
) (reason string, skip bool) {
	if _, ended := dead[row.SessionID]; !ended {
		return reasonNotAbsent, true
	}
	// UNKNOWN means the liveness read failed. The ledger's terminal status does
	// not override that, so the row stays skipped — the permissive form would
	// reopen the whole fleet under `--no-liveness`.
	if row.Liveness == "UNKNOWN" {
		return reasonNotAbsentDead, true
	}
	if alive, known := childLive[row.SessionID]; known && !alive {
		return "", false
	}
	return reasonNotAbsentDead, true
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
