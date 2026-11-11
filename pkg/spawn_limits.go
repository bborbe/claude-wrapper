// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/claude-wrapper/pkg/supervisorconfig"
)

// The fleet-wide spawn caps, ported from the node supervisor's `spawn-mode.mjs`,
// `start-rate.mjs` and `worker-sessions.mjs`.
//
// Two populations and one arrival bound:
//
//   - `spawn.maxWorkerSoft` / `spawn.maxWorkerHard` bound live WORKER
//     sessions. The soft cap refuses an ordinary spawn; the hard cap refuses
//     every spawn, including one the operator named by hand. The band between
//     them is where an operator-named task may still open.
//   - `spawn.maxManagersSoft` / `spawn.maxManagersHard` bound live MANAGER sessions,
//     a separate and much smaller population.
//   - `spawn.maxStartsPerMinute` bounds how fast sessions may be OPENED. It is an
//     arrival bound, not a population bound, and a concurrency cap cannot see
//     the burst it exists to prevent.
//
// The config file is read PER SPAWN rather than snapshotted at boot. The node
// reloads the manager pair on mtime; its worker pair's boot snapshot was a known
// staleness defect (a lowered cap ignored until the operator reconnects), and
// reading per spawn makes a lowered cap take effect on the next call with no
// restart.
const (
	// DefaultMaxConcurrent is the soft worker cap an absent key resolves to.
	//
	// Raised 20 → 60 on 2026-11-10 with the seven-axis split: the headless and
	// interactive populations now bind on their own sub-caps (`spawn_axes.go`), so
	// the worker total is a parent rather than the only control.
	DefaultMaxConcurrent = 60
	// DefaultMaxConcurrentHard is the hard worker cap an absent key resolves to.
	//
	// 90, raised with the soft cap. The two move together because the band between
	// them is the operator-named allowance, and a soft cap of 60 under a ceiling
	// still at 50 would not be a ceiling at all.
	DefaultMaxConcurrentHard = 90
	// DefaultMaxManagers is the soft manager cap an absent key resolves to.
	//
	// 12, on the operator's final 2026-11-10 ruling, replacing the 2026-10-10
	// design's 3. The children have to nest under it: topic 3 + goal 9 = 12 ≤ 12
	// soft, 5 + 13 = 18 ≤ 20 hard.
	DefaultMaxManagers = 12
	// DefaultMaxManagersHard is the hard manager cap an absent key resolves to.
	DefaultMaxManagersHard = 20
	// DefaultMaxStartsPerMinute is the arrival rate an absent key resolves to.
	//
	// 4 is chosen against the measured incident rather than rounded: 22 sessions
	// started in 2.8 minutes (~8/min) took the backend down, so a default at or
	// above that number would not prevent the incident it is named for.
	DefaultMaxStartsPerMinute = 4
	// MaxStartDelay is how long a start may wait before it is refused instead.
	//
	// A constant, not a config key: the rate is the operator's number, the ceiling
	// is what a caller can tolerate, and a second key would let the two disagree.
	MaxStartDelay = 5 * time.Minute
	// StartRateStateFile records the last reserved start slot. The node supervisor
	// writes the same file, so the two daemons pace one shared queue.
	StartRateStateFile = "start-rate.json"
	// isoMillis is JavaScript's `toISOString` shape, which the node writes into
	// the shared state file.
	isoMillis = "2006-01-02T15:04:05.000Z07:00"
)

// Environment overrides, named exactly as the node supervisor names them.
const (
	EnvMaxConcurrent      = "SUPERVISOR_MAX_CONCURRENT"
	EnvMaxConcurrentHard  = "SUPERVISOR_MAX_CONCURRENT_HARD"
	EnvMaxStartsPerMinute = "SUPERVISOR_MAX_STARTS_PER_MINUTE"
)

// The chain step that decided a limit, as a refusal reports it.
const (
	LimitSourceEnv     = "env"
	LimitSourceConfig  = "config"
	LimitSourceDefault = "default"
)

// ManagerFleetFull opens a hard-cap manager refusal. Exported so a caller can
// recognise it without restating the phrase.
const ManagerFleetFull = "the manager fleet is FULL"

// SpawnCaps enforces the fleet-wide caps before a spawn creates anything.
type SpawnCaps interface {
	// CheckWorker enforces the worker axes: the mode sub-cap, the worker total and
	// the session backstop. `mode` is the RESOLVED spawn mode (`headless` or
	// `interactive`); an empty mode is a worker that is neither locally — a cluster
	// or a2a worker — and then only the worker and session axes apply.
	// operatorNamed is the CALLER's assertion and is never inferred here.
	CheckWorker(ctx context.Context, mode string, operatorNamed bool) error
	// CheckManager enforces the manager axes: the kind sub-cap, the manager total
	// and the session backstop. `kind` is the manager kind the caller resolved
	// (`topic`, `goal`, `fleet` or `unresolved`); `roots` are the vault roots the
	// live count classifies managers against, resolved by the caller because this
	// package does not own vault lookup.
	CheckManager(ctx context.Context, kind string, roots []string, operatorNamed bool) error
	// ReserveStart paces a start against the arrival rate, waiting or refusing.
	ReserveStart(ctx context.Context) error
}

// NewSpawnCaps creates the cap enforcer.
//
// The ledger supplies identity, the heartbeat store and the session registry
// supply liveness. An empty home resolves to the user's home directory; a nil
// getenv reads the process environment — the same shape as
// NewSpawnModeSourcesReader, so both read the one config file.
func NewSpawnCaps(
	ledger LedgerReader,
	heartbeats HeartbeatReader,
	registry SessionRegistry,
	now libtime.CurrentDateTimeGetter,
	home string,
	getenv func(string) string,
) SpawnCaps {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &spawnCaps{
		ledger:     ledger,
		heartbeats: heartbeats,
		registry:   registry,
		now:        now,
		home:       home,
		getenv:     getenv,
		sleep:      sleepContext,
	}
}

type spawnCaps struct {
	ledger     LedgerReader
	heartbeats HeartbeatReader
	registry   SessionRegistry
	now        libtime.CurrentDateTimeGetter
	home       string
	getenv     func(string) string
	sleep      func(context.Context, time.Duration) error
}

// CheckWorker enforces the worker axes — the mode sub-cap, the worker total and
// the session backstop, in that order.
//
// The mode sub-cap is asked FIRST because it is the narrower population and the
// one a burst grows: telling a headless caller it is over the worker total sends
// it to close tabs, which is not what binds. The session cap is asked LAST
// because it is the backstop — it refuses everyone, so answering it early would
// replace a specific, actionable refusal with a generic one.
func (c *spawnCaps) CheckWorker(ctx context.Context, mode string, operatorNamed bool) error {
	file, path, err := c.config(ctx)
	if err != nil {
		return err
	}
	axes, err := ResolveSpawnAxes(ctx, c.getenv, file, path)
	if err != nil {
		return err
	}
	// Answered before the count, which is the expensive half: with every worker
	// axis switched off there is nothing for the count to decide.
	if axes.Worker.Limit == nil && axes.Session.Limit == nil && !modeHasCap(axes, mode) {
		return nil
	}
	counts, countErr := c.countWorkersByMode(ctx)
	refusal := workerAxesRefusal(axes, mode, counts, countErr, operatorNamed, path)
	if refusal != "" {
		return errors.Errorf(ctx, "%s", refusal)
	}
	return nil
}

// CheckManager enforces the manager axes — the kind sub-cap, the manager total
// and the session backstop.
//
// ⚠️ The live manager count comes from the spawn LEDGER, so a manager started by
// hand in a plain tab writes no row and is invisible here. That gap is unchanged
// by the split and is stated on `countManagersByKind`.
func (c *spawnCaps) CheckManager(
	ctx context.Context,
	kind string,
	roots []string,
	operatorNamed bool,
) error {
	file, path, err := c.config(ctx)
	if err != nil {
		return err
	}
	axes, err := ResolveSpawnAxes(ctx, c.getenv, file, path)
	if err != nil {
		return err
	}
	if axes.Manager.Limit == nil && axes.Session.Limit == nil && !managerKindHasCap(axes, kind) {
		return nil
	}
	managers, countErr := c.countManagersByKind(ctx, roots)
	if countErr != nil {
		return errors.Errorf(
			ctx, "%s",
			AxisLimitRefusal(axes, AxisManager, nil, countErr.Error(), operatorNamed, path),
		)
	}
	// The session backstop counts every session kind, so it needs the worker half
	// too. A second ledger read is the honest cost of one number that spans both
	// populations, and manager spawns are rare enough that it is not a hot path.
	workers, workerErr := c.countWorkersByMode(ctx)
	if workerErr != nil {
		return errors.Errorf(
			ctx, "%s",
			AxisLimitRefusal(axes, AxisSession, nil, workerErr.Error(), operatorNamed, path),
		)
	}
	refusal := managerAxesRefusal(axes, kind, managers, workers.total()+managers.total, operatorNamed, path)
	if refusal != "" {
		return errors.Errorf(ctx, "%s", refusal)
	}
	return nil
}

// workerAxesRefusal decides the worker-side axes from one count pass.
//
// The session total adds the ledger's managers to the workers, because the
// session cap counts every session kind — and the two numbers come from the same
// read, so the refusal cannot describe a different instant than the count.
func workerAxesRefusal(
	axes SpawnAxes,
	mode string,
	counts workerModeCounts,
	countErr error,
	operatorNamed bool,
	path string,
) string {
	if countErr != nil {
		// An uncountable fleet is ONE condition, not one per axis: the historic worker
		// refusal names the worker cap and says the count — not the limit — is the
		// failure, and a per-axis variant of that message would be a second phrasing of
		// one fact. The session backstop answers only when the worker pair is switched
		// off, since then nothing else would refuse.
		if refusal := ConcurrentLimitRefusal(
			axes.Worker, nil, countErr.Error(), operatorNamed, path,
		); refusal != "" {
			return refusal
		}
		return AxisLimitRefusal(axes, AxisSession, nil, countErr.Error(), operatorNamed, path)
	}
	total := counts.total()
	if axis := workerModeAxis(mode); axis != "" {
		if refusal := AxisLimitRefusal(
			axes, axis, intPtr(counts.forMode(mode)), "", operatorNamed, path,
		); refusal != "" {
			return refusal
		}
	}
	if refusal := ConcurrentLimitRefusal(
		axes.Worker, intPtr(total), "", operatorNamed, path,
	); refusal != "" {
		return refusal
	}
	return AxisLimitRefusal(
		axes, AxisSession, intPtr(total+counts.Managers), "", operatorNamed, path,
	)
}

// managerAxesRefusal decides the manager-side axes, then the session backstop.
func managerAxesRefusal(
	axes SpawnAxes,
	kind string,
	managers managerKindCounts,
	sessionCount int,
	operatorNamed bool,
	path string,
) string {
	if axis := managerKindAxis(kind); axis != "" {
		if refusal := AxisLimitRefusal(
			axes, axis, intPtr(managers.byKind[kind]), "", operatorNamed, path,
		); refusal != "" {
			return refusal
		}
	}
	if refusal := ManagerLimitRefusal(
		axes.Manager, intPtr(managers.total), "", operatorNamed, path,
	); refusal != "" {
		return refusal
	}
	return AxisLimitRefusal(axes, AxisSession, intPtr(sessionCount), "", operatorNamed, path)
}

// workerModeAxis maps a resolved spawn mode to its sub-cap axis, or "" for a
// worker that has no local mode.
func workerModeAxis(mode string) string {
	switch mode {
	case SpawnModeHeadless:
		return AxisHeadless
	case SpawnModeInteractive:
		return AxisInteractive
	default:
		return ""
	}
}

// managerKindAxis maps a manager kind to its sub-cap axis, or "" for a kind with
// no sub-cap of its own.
//
// `fleet` and `unresolved` answer "" deliberately: the Fleet Manager is excluded
// from the manager cap by the operator's design, and an unresolved manager's kind
// is exactly what could not be determined — guessing a pair for it would file it
// under a cap it may not belong to.
func managerKindAxis(kind string) string {
	switch kind {
	case ManagerKindTopic:
		return AxisTopicManager
	case ManagerKindGoal:
		return AxisGoalManager
	default:
		return ""
	}
}

// modeHasCap reports whether the mode's sub-cap is switched on.
func modeHasCap(axes SpawnAxes, mode string) bool {
	axis := workerModeAxis(mode)
	if axis == "" {
		return false
	}
	limits, ok := axes.LimitsFor(axis)
	return ok && limits.Limit != nil
}

// managerKindHasCap reports whether the kind's sub-cap is switched on.
func managerKindHasCap(axes SpawnAxes, kind string) bool {
	axis := managerKindAxis(kind)
	if axis == "" {
		return false
	}
	limits, ok := axes.LimitsFor(axis)
	return ok && limits.Limit != nil
}

// ReserveStart paces one start against the arrival rate.
//
// The sequence is read, decide, WRITE, then wait. The write-before-wait half is
// load-bearing: a caller that wrote after the wait would let a simultaneous batch
// read one stale stamp, compute one slot, and all sleep to the same instant.
func (c *spawnCaps) ReserveStart(ctx context.Context) error {
	file, path, err := c.config(ctx)
	if err != nil {
		return err
	}
	rate, err := ResolveMaxStarts(ctx, c.getenv(EnvMaxStartsPerMinute), file, path)
	if err != nil {
		return err
	}
	if rate.Limit == nil {
		return nil
	}
	stateDir, err := SupervisorStateDir(ctx, c.home, c.getenv)
	if err != nil {
		return err
	}
	stateFile := filepath.Join(stateDir, StartRateStateFile)
	decision := StartRateDecision(rate, readReservedAt(stateFile), c.currentTime(), path)
	if decision.Error != "" {
		return errors.Errorf(ctx, "%s", decision.Error)
	}
	if err := writeReservedAt(ctx, stateFile, decision.ReservedAt); err != nil {
		return errors.Wrapf(ctx, err, "record the start-rate reservation failed")
	}
	if decision.Wait <= 0 {
		return nil
	}
	log.Printf("claude-wrapper: %s", decision.Message)
	return c.sleep(ctx, decision.Wait)
}

// currentTime reads the injected clock, falling back to the system clock.
func (c *spawnCaps) currentTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now.Now().Time()
}

// config resolves the config path and reads the file, per call.
func (c *spawnCaps) config(ctx context.Context) (SpawnConfigFile, string, error) {
	path, err := SupervisorConfigPath(ctx, c.home, c.getenv)
	if err != nil {
		return SpawnConfigFile{}, "", err
	}
	return LoadSpawnConfig(ctx, path), path, nil
}

// countWorkers counts live worker sessions — the node's `workerSessions`.
//
// The ledger supplies identity (what the fleet opened) and liveness is the
// heartbeat store unioned with the session registry. So the cap and the
// managers' target cannot count different populations. ONLY an explicit
// `role: manager` excludes: a row with no role predates the field and reads as
// a worker.
//
// ⚠️ A RESUMED worker IS counted. Until 2026-11-10 this loop also skipped rows
// carrying `resumed_from`, mirroring the node, on the reasoning that a resume
// answers to the auto-resume gate's own crash-loop cap. That reasoning inverts
// this cap: a resumed worker is a live process consuming exactly the load the
// cap bounds, so a sweep that revives N dead workers has added N to the fleet
// and must be refused at the ceiling like any other addition. Measured
// 2026-11-10 on the node twin: 22 counted against 78 live worker sessions, and
// every session it missed was a resume. The crash-loop cap on the resume path
// is a separate control and is unchanged by this.
func (c *spawnCaps) countWorkers(ctx context.Context) (int, error) {
	counts, err := c.countWorkersByMode(ctx)
	if err != nil {
		return 0, err
	}
	return counts.total(), nil
}

// workerModeCounts is the live worker population split by the mode the ledger
// recorded, plus the managers the same pass saw.
//
// ⚠️ **Four worker modes, not two.** The ledger carries `interactive`,
// `headless`, `cluster` and `a2a`; a binary headless-vs-else classifier would
// report live cluster and a2a workers as "interactive" — exactly the conflation
// the seven-axis split exists to prevent. The node's `workerSessions` carries the
// same four for the same reason.
type workerModeCounts struct {
	Interactive int
	Headless    int
	Cluster     int
	A2A         int
	// Managers is the ledger's manager population, carried beside the workers
	// because the session backstop counts every session kind in one number.
	Managers int
}

// total is the worker population the worker axis bounds — managers excluded, so
// the count and the axis's own refusal describe the same set.
func (c workerModeCounts) total() int {
	return c.Interactive + c.Headless + c.Cluster + c.A2A
}

// forMode returns the count for one resolved spawn mode, 0 for a mode with no
// local sub-cap.
func (c workerModeCounts) forMode(mode string) int {
	switch mode {
	case SpawnModeHeadless:
		return c.Headless
	case SpawnModeInteractive:
		return c.Interactive
	default:
		return 0
	}
}

// countWorkersByMode counts live workers split by mode, in ONE ledger read.
//
// Identity is the spawn ledger; liveness is the heartbeat store unioned with the
// session registry (see `ledgerAndLive`). Managers and auto-resumes are counted
// apart: a manager is not a worker, and a resumed worker IS a worker (2026-11-10)
// — the resume used to be skipped, which is what made the cap blind to every
// session a sweep revived.
func (c *spawnCaps) countWorkersByMode(ctx context.Context) (workerModeCounts, error) {
	rows, live, err := c.ledgerAndLive(ctx)
	if err != nil {
		return workerModeCounts{}, err
	}
	var counts workerModeCounts
	for _, row := range rows {
		if row.SessionID == "" {
			continue
		}
		if _, ok := live[row.SessionID]; !ok {
			continue
		}
		if row.Role == RoleManager {
			counts.Managers++
			continue
		}
		switch row.Mode {
		case SpawnModeHeadless:
			counts.Headless++
		case "cluster":
			counts.Cluster++
		case "a2a":
			counts.A2A++
		default:
			// An absent mode predates the field and reads as interactive, which is
			// the node's rule (`record.mode || 'interactive'`).
			counts.Interactive++
		}
	}
	return counts, nil
}

// countManagers counts live manager sessions from the spawn ledger.
//
// ⚠️ Known difference from the node: a manager started by hand in a plain tab
// writes no ledger row, so it is not counted here. The node read
// `scripts/fleet-board.py`, which lives in the repository this port is replacing
// and must not become a dependency of the daemon. The worker cap still bounds
// total load.
func (c *spawnCaps) countManagers(ctx context.Context) (int, error) {
	counts, err := c.countManagersByKind(ctx, nil)
	if err != nil {
		return 0, err
	}
	return counts.total, nil
}

// managerKindCounts is the live manager population split by the kind its subject
// page names, plus the total.
type managerKindCounts struct {
	byKind map[string]int
	total  int
}

// countManagersByKind counts live managers from the spawn ledger, classified by
// kind against `roots`.
//
// ⚠️ **One rule, two callers.** The spawn site classifies the session it is about
// to create and this count classifies the sessions already live, both through
// `ManagerKindForLabel`, so a manager cannot be counted under one cap and opened
// under another.
//
// ⚠️ The roots are the caller's, because vault lookup is not this package's: a
// manager's subject page lives in the vault the manager runs in, and the ledger
// row's own `Cwd` is unioned in so a row spawned into a vault the operator has
// since unregistered still classifies against where it actually ran.
func (c *spawnCaps) countManagersByKind(
	ctx context.Context,
	roots []string,
) (managerKindCounts, error) {
	rows, live, err := c.ledgerAndLive(ctx)
	if err != nil {
		return managerKindCounts{}, err
	}
	counts := managerKindCounts{byKind: map[string]int{}}
	for _, row := range rows {
		if row.SessionID == "" || row.Role != RoleManager {
			continue
		}
		if _, ok := live[row.SessionID]; !ok {
			continue
		}
		rowRoots := roots
		if row.Cwd != "" {
			rowRoots = append(append([]string{}, roots...), row.Cwd)
		}
		kind := ManagerKindForLabel(row.Label, rowRoots, FileExists)
		counts.byKind[kind]++
		counts.total++
	}
	return counts, nil
}

// ledgerAndLive reads the ledger and the live set, naming the store that failed.
//
// Liveness is two channels unioned, as the node's count reads it:
//
//   - a heartbeat row with `live: true` AND a non-empty `state`. A `live: true`
//     row with no state is a ghost the store keeps stamping after its session
//     exited; counting it over-counts.
//   - the session registry's pid check, for a local session that holds no
//     heartbeat — a headless worker writes a ledger row and holds a live pid but
//     never stamps one; without this it is invisible and the cap under-counts.
//
// An unreadable ledger or heartbeat store is an error, never zero. An unreadable
// registry adds nothing rather than failing: the heartbeat store already answered,
// and a missing second channel is not an unknown first one.
func (c *spawnCaps) ledgerAndLive(
	ctx context.Context,
) ([]LedgerRow, map[string]struct{}, error) {
	if c.ledger == nil || c.heartbeats == nil {
		return nil, nil, errors.Errorf(
			ctx, "the session-ledger and session-heartbeat readers are not wired",
		)
	}
	rows, err := c.ledger.Rows(ctx)
	if err != nil {
		return nil, nil, errors.Errorf(
			ctx, "the session-ledger endpoint could not be read: %v", err,
		)
	}
	// One row per session, the last one winning — the node's `ledgerBySessionId`
	// collapse. The endpoint serves a LIST, and a session that was spawned,
	// stopped and re-recorded appears more than once; counting per row would
	// charge the same worker twice and refuse a fleet that is not full.
	rows = collapseLedgerRows(rows)
	beats, err := c.heartbeats.Rows(ctx)
	if err != nil {
		return nil, nil, errors.Errorf(
			ctx, "the session-heartbeat endpoint could not be read: %v", err,
		)
	}
	live := make(map[string]struct{}, len(beats))
	for _, beat := range beats {
		if beat.Live && beat.SessionID != "" && beat.State != "" {
			live[beat.SessionID] = struct{}{}
		}
	}
	if c.registry != nil {
		entries, regErr := c.registry.Live(ctx)
		if regErr != nil {
			log.Printf("WARNING: spawn cap: session registry unreadable (%v) — counting heartbeats only", regErr)
		}
		for _, entry := range entries {
			if entry.SessionID != "" {
				live[entry.SessionID] = struct{}{}
			}
		}
	}
	return rows, live, nil
}

// collapseLedgerRows keeps one row per session id, the last occurrence winning,
// in first-seen order.
func collapseLedgerRows(rows []LedgerRow) []LedgerRow {
	index := make(map[string]int, len(rows))
	collapsed := make([]LedgerRow, 0, len(rows))
	for _, row := range rows {
		if row.SessionID == "" {
			collapsed = append(collapsed, row)
			continue
		}
		if at, seen := index[row.SessionID]; seen {
			collapsed[at] = row
			continue
		}
		index[row.SessionID] = len(collapsed)
		collapsed = append(collapsed, row)
	}
	return collapsed
}

// SupervisorConfigPath returns the supervisor config file.
//
// Delegates to the shared resolver the A2A client also uses, so a scratch config
// and an XDG home reach every reader of this file, not only the roster.
func SupervisorConfigPath(
	ctx context.Context,
	home string,
	getenv func(string) string,
) (string, error) {
	return supervisorconfig.Path(ctx, home, getenv)
}

// SupervisorStateDir returns the supervisor state directory.
func SupervisorStateDir(
	ctx context.Context,
	home string,
	getenv func(string) string,
) (string, error) {
	return supervisorconfig.StateDir(ctx, home, getenv)
}

// SpawnConfigFile is the part of the supervisor config the caps read.
//
// Every axis key carries an explicit Soft/Hard suffix, so the nesting invariant
// (`headless + interactive <= worker`, `topic + goal <= manager`,
// `worker + manager <= session`) is readable from the key names alone. The
// pre-rename spellings still parse — `applyLegacySpawnKeys` folds them in and
// warns once per key — because renaming outright would strand every operator's
// existing config.json silently.
//
// `mode` and `maxStartsPerMinute` are not cap axes and keep their names. The
// environment variables keep theirs too: they are not config keys, and
// `SUPERVISOR_MAX_CONCURRENT` still sets `spawn.maxWorkerSoft`.
//
// All fourteen axis keys are read per spawn rather than snapshotted at boot.
type SpawnConfigFile struct {
	Spawn struct {
		MaxWorkerSoft        json.RawMessage `json:"maxWorkerSoft"`
		MaxWorkerHard        json.RawMessage `json:"maxWorkerHard"`
		MaxManagersSoft      json.RawMessage `json:"maxManagersSoft"`
		MaxManagersHard      json.RawMessage `json:"maxManagersHard"`
		MaxStartsPerMinute   json.RawMessage `json:"maxStartsPerMinute"`
		MaxSessionsSoft      json.RawMessage `json:"maxSessionsSoft"`
		MaxSessionsHard      json.RawMessage `json:"maxSessionsHard"`
		MaxHeadlessSoft      json.RawMessage `json:"maxHeadlessSoft"`
		MaxHeadlessHard      json.RawMessage `json:"maxHeadlessHard"`
		MaxInteractiveSoft   json.RawMessage `json:"maxInteractiveSoft"`
		MaxInteractiveHard   json.RawMessage `json:"maxInteractiveHard"`
		MaxTopicManagersSoft json.RawMessage `json:"maxTopicManagersSoft"`
		MaxTopicManagersHard json.RawMessage `json:"maxTopicManagersHard"`
		MaxGoalManagersSoft  json.RawMessage `json:"maxGoalManagersSoft"`
		MaxGoalManagersHard  json.RawMessage `json:"maxGoalManagersHard"`

		// The pre-rename spellings, kept so an existing config.json keeps
		// parsing. `applyLegacySpawnKeys` folds them into the fields above;
		// nothing reads them directly.
		LegacyMaxConcurrent     json.RawMessage `json:"maxConcurrent"`
		LegacyMaxConcurrentHard json.RawMessage `json:"maxConcurrentHard"`
		LegacyMaxManagers       json.RawMessage `json:"maxManagers"`
		LegacyMaxSessions       json.RawMessage `json:"maxSessions"`
		LegacyMaxHeadless       json.RawMessage `json:"maxHeadless"`
		LegacyMaxInteractive    json.RawMessage `json:"maxInteractive"`
		LegacyMaxTopicManagers  json.RawMessage `json:"maxTopicManagers"`
		LegacyMaxGoalManagers   json.RawMessage `json:"maxGoalManagers"`
	} `json:"spawn"`
}

// renamedSpawnKeys pairs each current key with the name it shipped with.
//
// Only the soft keys and maxWorkerHard appear: every other *Hard key already
// carried its suffix, so it was not renamed. Ordered, so the fold's warnings
// arrive in the same order on every run.
var renamedSpawnKeys = []struct{ current, legacy string }{
	{"maxWorkerSoft", "maxConcurrent"},
	{"maxWorkerHard", "maxConcurrentHard"},
	{"maxManagersSoft", "maxManagers"},
	{"maxSessionsSoft", "maxSessions"},
	{"maxHeadlessSoft", "maxHeadless"},
	{"maxInteractiveSoft", "maxInteractive"},
	{"maxTopicManagersSoft", "maxTopicManagers"},
	{"maxGoalManagersSoft", "maxGoalManagers"},
}

// currentSpawnKeys addresses each current axis key's raw value.
func (f *SpawnConfigFile) currentSpawnKeys() map[string]*json.RawMessage {
	return map[string]*json.RawMessage{
		"maxWorkerSoft":        &f.Spawn.MaxWorkerSoft,
		"maxWorkerHard":        &f.Spawn.MaxWorkerHard,
		"maxManagersSoft":      &f.Spawn.MaxManagersSoft,
		"maxManagersHard":      &f.Spawn.MaxManagersHard,
		"maxSessionsSoft":      &f.Spawn.MaxSessionsSoft,
		"maxSessionsHard":      &f.Spawn.MaxSessionsHard,
		"maxHeadlessSoft":      &f.Spawn.MaxHeadlessSoft,
		"maxHeadlessHard":      &f.Spawn.MaxHeadlessHard,
		"maxInteractiveSoft":   &f.Spawn.MaxInteractiveSoft,
		"maxInteractiveHard":   &f.Spawn.MaxInteractiveHard,
		"maxTopicManagersSoft": &f.Spawn.MaxTopicManagersSoft,
		"maxTopicManagersHard": &f.Spawn.MaxTopicManagersHard,
		"maxGoalManagersSoft":  &f.Spawn.MaxGoalManagersSoft,
		"maxGoalManagersHard":  &f.Spawn.MaxGoalManagersHard,
	}
}

// legacySpawnKeys addresses each pre-rename key's raw value.
func (f *SpawnConfigFile) legacySpawnKeys() map[string]*json.RawMessage {
	return map[string]*json.RawMessage{
		"maxConcurrent":     &f.Spawn.LegacyMaxConcurrent,
		"maxConcurrentHard": &f.Spawn.LegacyMaxConcurrentHard,
		"maxManagers":       &f.Spawn.LegacyMaxManagers,
		"maxSessions":       &f.Spawn.LegacyMaxSessions,
		"maxHeadless":       &f.Spawn.LegacyMaxHeadless,
		"maxInteractive":    &f.Spawn.LegacyMaxInteractive,
		"maxTopicManagers":  &f.Spawn.LegacyMaxTopicManagers,
		"maxGoalManagers":   &f.Spawn.LegacyMaxGoalManagers,
	}
}

// applyLegacySpawnKeys folds pre-rename key names into their current fields and
// warns once per key that used one, naming both forms so the operator can
// migrate at their own pace.
//
// The current name always wins. A config carrying both spellings of one axis
// would otherwise resolve by whichever field the reader happened to consult, and
// the operator would have no way to tell which number took effect — so the
// legacy value is reported and ignored rather than silently racing.
func (f *SpawnConfigFile) applyLegacySpawnKeys(path string) {
	current := f.currentSpawnKeys()
	legacy := f.legacySpawnKeys()
	for _, pair := range renamedSpawnKeys {
		old, ok := legacy[pair.legacy]
		if !ok || len(*old) == 0 {
			continue
		}
		cur, ok := current[pair.current]
		if !ok {
			continue
		}
		if len(*cur) == 0 {
			*cur = *old
			log.Printf(
				`WARNING: %s uses the deprecated spawn key "spawn.%s" — renamed to "spawn.%s". `+
					`The old name still works; update the file to silence this.`,
				path, pair.legacy, pair.current,
			)
			continue
		}
		log.Printf(
			`WARNING: %s carries both "spawn.%s" (deprecated) and "spawn.%s" — using `+
				`"spawn.%s" and ignoring "spawn.%s".`,
			path, pair.legacy, pair.current, pair.current, pair.legacy,
		)
	}
}

// LoadSpawnConfig reads and parses the config file.
//
// An absent file is no config — most machines take the built-in defaults. An
// unreadable or unparseable one is logged and read as no config, as the node's
// `readConfigFile` and NewSpawnModeSourcesReader do: the built-in defaults are
// still caps, so the fleet stays bounded while the operator fixes the file. A
// parseable file with a bad VALUE refuses in the resolvers.
func LoadSpawnConfig(ctx context.Context, path string) SpawnConfigFile {
	var file SpawnConfigFile
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("WARNING: read %s failed (%v) — spawn caps fall back to their defaults", path, err)
		}
		return file
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		log.Printf("WARNING: parse %s failed (%v) — spawn caps fall back to their defaults", path, err)
		return SpawnConfigFile{}
	}
	file.applyLegacySpawnKeys(path)
	return file
}

// ConcurrentLimits is the worker pair, resolved and validated together. A nil
// Limit is unlimited, which only the soft key's explicit `0` produces.
type ConcurrentLimits struct {
	Limit      *int
	HardLimit  *int
	Source     string
	HardSource string
}

// ManagerLimits is the manager pair, resolved and validated together.
type ManagerLimits = ConcurrentLimits

// StartRate is the arrival bound, resolved. A nil Limit is no rate limit.
type StartRate struct {
	Limit    *int
	Interval time.Duration
	Source   string
}

// thresholdSpec names one key for resolution and for its refusal text.
type thresholdSpec struct {
	raw      json.RawMessage
	env      string
	envName  string
	key      string
	path     string
	fallback int
	noun     string
}

// limitResolution is one resolved threshold plus the source that decided it.
type limitResolution struct {
	Limit  *int
	Source string
}

// ResolveMaxConcurrent ports the node's `resolveMaxConcurrent`: the worker pair,
// resolved and validated as a pair, because a soft cap above its own ceiling is
// not a limit.
func ResolveMaxConcurrent(
	ctx context.Context,
	envSoft string,
	envHard string,
	file SpawnConfigFile,
	path string,
) (ConcurrentLimits, error) {
	soft, err := resolveThreshold(ctx, thresholdSpec{
		raw: file.Spawn.MaxWorkerSoft, env: envSoft, envName: EnvMaxConcurrent,
		key: "maxWorkerSoft", path: path, fallback: DefaultMaxConcurrent, noun: "concurrent-worker",
	})
	if err != nil {
		return ConcurrentLimits{}, err
	}
	// The off switch disables the PAIR, short-circuited before the hard key is
	// read, so an unparseable hard value cannot refuse a deliberately uncapped
	// fleet.
	if soft.Limit == nil {
		return ConcurrentLimits{Source: soft.Source, HardSource: soft.Source}, nil
	}
	hard, err := resolveThreshold(ctx, thresholdSpec{
		raw: file.Spawn.MaxWorkerHard, env: envHard, envName: EnvMaxConcurrentHard,
		key: "maxWorkerHard", path: path, fallback: DefaultMaxConcurrentHard, noun: "concurrent-worker",
	})
	if err != nil {
		return ConcurrentLimits{}, err
	}
	if hard.Limit == nil {
		return ConcurrentLimits{}, errors.Errorf(
			ctx,
			"%s is 0, which is not a hard concurrent-worker limit — refusing to spawn rather than "+
				"removing the ceiling while the soft cap stays in force, because a fleet that looks capped and is "+
				"not is discovered only by the load it was meant to bound. Set a non-negative integer above the "+
				`soft cap, or use the off switch: "spawn.maxWorkerSoft": 0 disables BOTH thresholds.`,
			thresholdWhere(hard.Source, EnvMaxConcurrentHard, "maxWorkerHard", path),
		)
	}
	if *hard.Limit < *soft.Limit {
		return ConcurrentLimits{}, errors.Errorf(
			ctx,
			`the hard concurrent-worker limit is below the soft one: "spawn.maxWorkerSoft" resolves to `+
				`%d (source: %s) but "spawn.maxWorkerHard" resolves to %d `+
				`(source: %s) — refusing to spawn rather than running under a ceiling beneath its own `+
				`floor, which would empty the operator-named band and refuse every spawn at the soft count. Raise `+
				`spawn.maxWorkerHard in %s to at least %d, or lower spawn.maxWorkerSoft.`,
			*soft.Limit, soft.Source, *hard.Limit, hard.Source, path, *soft.Limit,
		)
	}
	return ConcurrentLimits{
		Limit:      soft.Limit,
		HardLimit:  hard.Limit,
		Source:     soft.Source,
		HardSource: hard.Source,
	}, nil
}

// ResolveMaxManagers ports the node's `resolveMaxManagers`. Config file only —
// the operator's design names no environment variable for it.
func ResolveMaxManagers(
	ctx context.Context,
	file SpawnConfigFile,
	path string,
) (ManagerLimits, error) {
	soft, err := resolveThreshold(ctx, thresholdSpec{
		raw: file.Spawn.MaxManagersSoft, key: "maxManagersSoft", path: path,
		fallback: DefaultMaxManagers, noun: "concurrent-manager",
	})
	if err != nil {
		return ManagerLimits{}, err
	}
	if soft.Limit == nil {
		return ManagerLimits{Source: soft.Source, HardSource: soft.Source}, nil
	}
	hard, err := resolveThreshold(ctx, thresholdSpec{
		raw: file.Spawn.MaxManagersHard, key: "maxManagersHard", path: path,
		fallback: DefaultMaxManagersHard, noun: "concurrent-manager",
	})
	if err != nil {
		return ManagerLimits{}, err
	}
	if hard.Limit == nil {
		return ManagerLimits{}, errors.Errorf(
			ctx,
			`"spawn.maxManagersHard" in %s is 0, which is not a hard concurrent-manager limit — refusing `+
				`to spawn rather than removing the ceiling while the soft cap stays in force. Set a non-negative `+
				`integer above the soft cap, or use the off switch: "spawn.maxManagersSoft": 0 disables BOTH thresholds.`,
			path,
		)
	}
	if *hard.Limit < *soft.Limit {
		return ManagerLimits{}, errors.Errorf(
			ctx,
			`the hard concurrent-manager limit is below the soft one: "spawn.maxManagersSoft" resolves to `+
				`%d (source: %s) but "spawn.maxManagersHard" resolves to %d `+
				`(source: %s) — refusing to spawn rather than running under a ceiling beneath its own `+
				`floor. Raise spawn.maxManagersHard in %s to at least %d, or lower spawn.maxManagersSoft.`,
			*soft.Limit, soft.Source, *hard.Limit, hard.Source, path, *soft.Limit,
		)
	}
	return ManagerLimits{
		Limit:      soft.Limit,
		HardLimit:  hard.Limit,
		Source:     soft.Source,
		HardSource: hard.Source,
	}, nil
}

// ResolveMaxStarts ports the node's `resolveMaxStarts` (`start-rate.mjs`).
func ResolveMaxStarts(
	ctx context.Context,
	env string,
	file SpawnConfigFile,
	path string,
) (StartRate, error) {
	spec := thresholdSpec{
		raw: file.Spawn.MaxStartsPerMinute, env: env, envName: EnvMaxStartsPerMinute,
		key: "maxStartsPerMinute", path: path, fallback: DefaultMaxStartsPerMinute,
	}
	for _, source := range thresholdSources(spec) {
		if source.valid {
			continue
		}
		return StartRate{}, errors.Errorf(
			ctx,
			"%s is %s, which is not a start rate — refusing to spawn rather than "+
				"guessing, since a rate that silently governs nothing is discovered only by the burst it was meant "+
				"to bound. Valid values: a non-negative integer (0 for no rate limit), or omit the key for the "+
				"default of %d.",
			source.where, source.text, DefaultMaxStartsPerMinute,
		)
	}
	resolution := pickThreshold(spec)
	if resolution.Limit == nil {
		return StartRate{Source: resolution.Source}, nil
	}
	return StartRate{
		Limit:    resolution.Limit,
		Interval: time.Minute / time.Duration(*resolution.Limit),
		Source:   resolution.Source,
	}, nil
}

// resolveThreshold ports the node's `resolveThreshold`: the environment, then the
// config file, then the fallback.
//
// Every source is validated, not only the winner: a typo in the file must not
// stay invisible merely because this spawn was capped by the environment instead
// — that is how a file gets accepted, reported as applied, and silently ignored.
func resolveThreshold(ctx context.Context, spec thresholdSpec) (limitResolution, error) {
	for _, source := range thresholdSources(spec) {
		if source.valid {
			continue
		}
		return limitResolution{}, errors.Errorf(
			ctx,
			"%s is %s, which is not a %s limit — refusing to spawn "+
				"rather than guessing, since a limit that silently governs nothing is discovered only by the load it "+
				"was meant to bound. Valid values: a non-negative integer (0 for unlimited), or omit "+
				"the key for the default of %d.",
			source.where, source.text, spec.noun, spec.fallback,
		)
	}
	return pickThreshold(spec), nil
}

// pickThreshold returns the first present source, `0` reading as unlimited.
// Callers validate every source first.
func pickThreshold(spec thresholdSpec) limitResolution {
	sources := thresholdSources(spec)
	if len(sources) == 0 {
		return limitResolution{Limit: intPtr(spec.fallback), Source: LimitSourceDefault}
	}
	first := sources[0]
	if first.value == 0 {
		return limitResolution{Limit: nil, Source: first.source}
	}
	return limitResolution{Limit: intPtr(first.value), Source: first.source}
}

// thresholdSource is one PRESENT place a limit came from.
type thresholdSource struct {
	source string
	where  string
	value  int
	valid  bool
	text   string
}

// thresholdSources collects the present environment and file candidates in
// precedence order, without deciding.
func thresholdSources(spec thresholdSpec) []thresholdSource {
	sources := make([]thresholdSource, 0, 2)
	if spec.env != "" {
		value, valid := parseLimitNumber(spec.env)
		sources = append(sources, thresholdSource{
			source: LimitSourceEnv, where: spec.envName, value: value,
			valid: valid, text: strconv.Quote(spec.env),
		})
	}
	if value, present, valid := parseLimitJSON(spec.raw); present {
		sources = append(sources, thresholdSource{
			source: LimitSourceConfig,
			where:  thresholdWhere(LimitSourceConfig, spec.envName, spec.key, spec.path),
			value:  value, valid: valid, text: string(spec.raw),
		})
	}
	return sources
}

// thresholdWhere names the place a refusal should send the operator.
func thresholdWhere(source, envName, key, path string) string {
	if source == LimitSourceEnv {
		return envName
	}
	return `"spawn.` + key + `" in ` + path
}

// parseLimitJSON parses a JSON number or numeric string.
//
// Only a number or a numeric string is a candidate: `Number(true)` is 1 and
// `Number(null)` is 0 in the node, so coercing blindly would read a stray boolean
// as a limit of one. Absent, `null` and `""` are not present.
func parseLimitJSON(raw json.RawMessage) (value int, present bool, valid bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return 0, false, true
		}
		parsed, ok := parseLimitNumber(text)
		return parsed, true, ok
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		if number != math.Trunc(number) || number < 0 || number > math.MaxInt32 {
			return 0, true, false
		}
		return int(number), true, true
	}
	return 0, true, false
}

// parseLimitNumber parses a non-negative integer string.
func parseLimitNumber(text string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

// ConcurrentLimitRefusal ports the node's `concurrentLimitRefusal`: the worker
// refusal text, or "" to allow the spawn.
//
// The hard cap is asked FIRST because it refuses everyone; answering the soft cap
// first would tell an operator-named caller it was exempt at a count where
// nothing opens. A nil liveCount is "a store could not be read", which is NOT
// "no worker is live", and refuses.
func ConcurrentLimitRefusal(
	limits ConcurrentLimits,
	liveCount *int,
	countError string,
	operatorNamed bool,
	configFile string,
) string {
	if limits.Limit == nil {
		return ""
	}
	if liveCount == nil {
		cause := "The count reads the session-ledger and session-heartbeat endpoints, and both must be " +
			"readable, because a channel that cannot be read may be hiding the very session being " +
			"counted; point ATTENTION_STORE_URL at them if they live elsewhere."
		if countError != "" {
			cause = "The count failed: " + countError + "."
		}
		return fmt.Sprintf(
			"the concurrent-worker limit is set to %d but the live-worker count could not be "+
				"taken, so it is unknown — refusing rather than opening past a limit that cannot be counted. "+
				"%s The limit is not the failure — the count is.",
			*limits.Limit, cause,
		)
	}
	count := *liveCount
	if limits.HardLimit != nil && count >= *limits.HardLimit {
		return fmt.Sprintf(
			"the fleet is FULL: %d live workers against a hard cap of %d "+
				"(source: %s), so nothing opens — not even an operator-named task. The soft "+
				"cap is %d (source: %s). Raise spawn.maxWorkerHard in "+
				"%s to open past %d, or set spawn.maxWorkerSoft to 0 for unlimited.",
			count, *limits.HardLimit, limits.HardSource, *limits.Limit, limits.Source,
			configFile, *limits.HardLimit,
		)
	}
	if count >= *limits.Limit && !operatorNamed {
		return fmt.Sprintf(
			"the fleet-wide concurrent-worker limit is reached: %d live, %d "+
				"allowed (source: %s). Open nothing further and report the remainder as "+
				"held-on-limit; it is picked up next sweep. An OPERATOR-NAMED task may still open here — this one was "+
				"not named, so it is refused. Raise spawn.maxWorkerSoft in %s, or "+
				"set it to 0 for unlimited.",
			count, *limits.Limit, limits.Source, configFile,
		)
	}
	return ""
}

// ManagerLimitRefusal ports the node's `managerLimitRefusal`: the manager refusal
// text, or "" to allow the spawn. Same ordering as ConcurrentLimitRefusal.
func ManagerLimitRefusal(
	limits ManagerLimits,
	liveCount *int,
	countError string,
	operatorNamed bool,
	configFile string,
) string {
	if limits.Limit == nil {
		return ""
	}
	if liveCount == nil {
		cause := "The count reads the session-ledger and session-heartbeat endpoints, which must both be readable."
		if countError != "" {
			cause = "The count failed: " + countError + "."
		}
		return fmt.Sprintf(
			"the live-manager count could not be taken, so it is unknown — refusing rather than opening "+
				"past a limit that cannot be counted. %s The limit now in effect is %d (source: %s) — the count is what "+
				"failed, not the limit.",
			cause, *limits.Limit, limits.Source,
		)
	}
	count := *liveCount
	if limits.HardLimit != nil && count >= *limits.HardLimit {
		return fmt.Sprintf(
			`%s: %d live managers (Fleet Manager excluded) against a hard cap of `+
				`%d — "spawn.maxManagersHard" (source: %s) — so nothing opens, not `+
				`even an operator-named manager. Raise spawn.maxManagersHard in %s, or set `+
				`spawn.maxManagersSoft to 0 for unlimited.`,
			ManagerFleetFull, count, *limits.HardLimit, limits.HardSource, configFile,
		)
	}
	if count >= *limits.Limit && !operatorNamed {
		return fmt.Sprintf(
			`the concurrent-manager limit is reached: %d live managers (Fleet Manager excluded), `+
				`%d allowed — "spawn.maxManagersSoft" (source: %s). An OPERATOR-NAMED manager `+
				`may still open here — this one was not named, so it is refused. Raise spawn.maxManagersSoft in `+
				`%s, or set it to 0 for unlimited.`,
			count, *limits.Limit, limits.Source, configFile,
		)
	}
	return ""
}

// StartRatePlan is the whole pacing decision for one start.
type StartRatePlan struct {
	// Error is a refusal; when set nothing is reserved.
	Error      string
	ReservedAt time.Time
	Wait       time.Duration
	Message    string
}

// StartRateDecision ports the node's `startRateDecision`: proceed now, wait, or
// refuse past the ceiling. Pure, so the rule that must not ship uncovered can be
// tested.
func StartRateDecision(
	rate StartRate,
	lastReservedAt *time.Time,
	now time.Time,
	configFile string,
) StartRatePlan {
	if rate.Limit == nil {
		return StartRatePlan{ReservedAt: now}
	}
	earliest := now
	if lastReservedAt != nil {
		if candidate := lastReservedAt.Add(rate.Interval); candidate.After(earliest) {
			earliest = candidate
		}
	}
	wait := earliest.Sub(now)
	if wait > MaxStartDelay {
		return StartRatePlan{Error: fmt.Sprintf(
			"the session start rate is %d/min (source: %s) and this start is "+
				"%ds behind the queue — past the %ds ceiling, so "+
				"it is refused rather than parked. Wait for the queue to drain and retry, or raise "+
				"spawn.maxStartsPerMinute in %s (0 disables the rate limit).",
			*rate.Limit, rate.Source, int(math.Round(wait.Seconds())),
			int(MaxStartDelay.Seconds()), configFile,
		)}
	}
	if wait <= 0 {
		return StartRatePlan{ReservedAt: earliest}
	}
	return StartRatePlan{
		ReservedAt: earliest,
		Wait:       wait,
		Message: fmt.Sprintf(
			"the session start rate is %d/min (source: %s), so this start is delayed "+
				"%ds to %s. It is paced, not dropped.",
			*rate.Limit, rate.Source, int(math.Round(wait.Seconds())),
			earliest.UTC().Format(isoMillis),
		),
	}
}

// readReservedAt returns the last reserved start, or nil.
//
// An unreadable or malformed file reads as nil rather than refusing, as the
// node's does: the alternative is a fleet that cannot start anything because one
// JSON file was truncated, and the failure this bounds is a burst, not a lost slot.
func readReservedAt(path string) *time.Time {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed struct {
		LastReservedAt string `json:"last_reserved_at"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, parsed.LastReservedAt)
	if err != nil {
		return nil
	}
	return &at
}

// writeReservedAt records a reservation in the node's shape, written to a unique
// temp file and renamed so a reader never sees a partial file.
func writeReservedAt(ctx context.Context, path string, at time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create %s failed", filepath.Dir(path))
	}
	body, err := json.MarshalIndent(
		map[string]string{"last_reserved_at": at.UTC().Format(isoMillis)}, "", "  ",
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "encode the reservation failed")
	}
	tmp := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write %s failed", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errors.Wrapf(ctx, err, "rename %s failed", tmp)
	}
	return nil
}

// sleepContext waits for d or until the context is cancelled.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.Wrapf(ctx, ctx.Err(), "the start-rate wait was cancelled")
	case <-timer.C:
		return nil
	}
}

// intPtr returns a pointer to value, so an unlimited `nil` stays distinct from
// a limit of zero.
func intPtr(value int) *int {
	return &value
}
