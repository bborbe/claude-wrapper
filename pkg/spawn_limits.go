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
//   - `spawn.maxConcurrent` / `spawn.maxConcurrentHard` bound live WORKER
//     sessions. The soft cap refuses an ordinary spawn; the hard cap refuses
//     every spawn, including one the operator named by hand. The band between
//     them is where an operator-named task may still open.
//   - `spawn.maxManagers` / `spawn.maxManagersHard` bound live MANAGER sessions,
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
	DefaultMaxConcurrent = 20
	// DefaultMaxConcurrentHard is the hard worker cap an absent key resolves to.
	DefaultMaxConcurrentHard = 50
	// DefaultMaxManagers is the soft manager cap an absent key resolves to.
	DefaultMaxManagers = 3
	// DefaultMaxManagersHard is the hard manager cap an absent key resolves to.
	DefaultMaxManagersHard = 5
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
	// CheckWorker refuses an ordinary spawn at the soft worker cap, and every
	// spawn at the hard cap. operatorNamed is the CALLER's assertion and is never
	// inferred here.
	CheckWorker(ctx context.Context, operatorNamed bool) error
	// CheckManager refuses a manager spawn at the manager pair.
	CheckManager(ctx context.Context, operatorNamed bool) error
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

// CheckWorker enforces the worker pair.
func (c *spawnCaps) CheckWorker(ctx context.Context, operatorNamed bool) error {
	file, path, err := c.config(ctx)
	if err != nil {
		return err
	}
	limits, err := ResolveMaxConcurrent(
		ctx, c.getenv(EnvMaxConcurrent), c.getenv(EnvMaxConcurrentHard), file, path,
	)
	if err != nil {
		return err
	}
	// `nil` is the soft key's off switch, which disables the pair. Answered before
	// the count, which is the expensive half.
	if limits.Limit == nil {
		return nil
	}
	var refusal string
	if count, countErr := c.countWorkers(ctx); countErr != nil {
		refusal = ConcurrentLimitRefusal(limits, nil, countErr.Error(), operatorNamed, path)
	} else {
		refusal = ConcurrentLimitRefusal(limits, &count, "", operatorNamed, path)
	}
	if refusal != "" {
		return errors.Errorf(ctx, "%s", refusal)
	}
	return nil
}

// CheckManager enforces the manager pair.
func (c *spawnCaps) CheckManager(ctx context.Context, operatorNamed bool) error {
	file, path, err := c.config(ctx)
	if err != nil {
		return err
	}
	limits, err := ResolveMaxManagers(ctx, file, path)
	if err != nil {
		return err
	}
	if limits.Limit == nil {
		return nil
	}
	var refusal string
	if count, countErr := c.countManagers(ctx); countErr != nil {
		refusal = ManagerLimitRefusal(limits, nil, countErr.Error(), operatorNamed, path)
	} else {
		refusal = ManagerLimitRefusal(limits, &count, "", operatorNamed, path)
	}
	if refusal != "" {
		return errors.Errorf(ctx, "%s", refusal)
	}
	return nil
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
	rows, live, err := c.ledgerAndLive(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		if row.SessionID == "" || row.Role == RoleManager {
			continue
		}
		if _, ok := live[row.SessionID]; ok {
			count++
		}
	}
	return count, nil
}

// countManagers counts live manager sessions from the spawn ledger.
//
// ⚠️ Known difference from the node: a manager started by hand in a plain tab
// writes no ledger row, so it is not counted here. The node read
// `scripts/fleet-board.py`, which lives in the repository this port is replacing
// and must not become a dependency of the daemon. The worker cap still bounds
// total load.
func (c *spawnCaps) countManagers(ctx context.Context) (int, error) {
	rows, live, err := c.ledgerAndLive(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		if row.SessionID == "" || row.Role != RoleManager {
			continue
		}
		if _, ok := live[row.SessionID]; ok {
			count++
		}
	}
	return count, nil
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
type SpawnConfigFile struct {
	Spawn struct {
		MaxConcurrent      json.RawMessage `json:"maxConcurrent"`
		MaxConcurrentHard  json.RawMessage `json:"maxConcurrentHard"`
		MaxManagers        json.RawMessage `json:"maxManagers"`
		MaxManagersHard    json.RawMessage `json:"maxManagersHard"`
		MaxStartsPerMinute json.RawMessage `json:"maxStartsPerMinute"`
	} `json:"spawn"`
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
		raw: file.Spawn.MaxConcurrent, env: envSoft, envName: EnvMaxConcurrent,
		key: "maxConcurrent", path: path, fallback: DefaultMaxConcurrent, noun: "concurrent-worker",
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
		raw: file.Spawn.MaxConcurrentHard, env: envHard, envName: EnvMaxConcurrentHard,
		key: "maxConcurrentHard", path: path, fallback: DefaultMaxConcurrentHard, noun: "concurrent-worker",
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
				`soft cap, or use the off switch: "spawn.maxConcurrent": 0 disables BOTH thresholds.`,
			thresholdWhere(hard.Source, EnvMaxConcurrentHard, "maxConcurrentHard", path),
		)
	}
	if *hard.Limit < *soft.Limit {
		return ConcurrentLimits{}, errors.Errorf(
			ctx,
			`the hard concurrent-worker limit is below the soft one: "spawn.maxConcurrent" resolves to `+
				`%d (source: %s) but "spawn.maxConcurrentHard" resolves to %d `+
				`(source: %s) — refusing to spawn rather than running under a ceiling beneath its own `+
				`floor, which would empty the operator-named band and refuse every spawn at the soft count. Raise `+
				`spawn.maxConcurrentHard in %s to at least %d, or lower spawn.maxConcurrent.`,
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
		raw: file.Spawn.MaxManagers, key: "maxManagers", path: path,
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
				`integer above the soft cap, or use the off switch: "spawn.maxManagers": 0 disables BOTH thresholds.`,
			path,
		)
	}
	if *hard.Limit < *soft.Limit {
		return ManagerLimits{}, errors.Errorf(
			ctx,
			`the hard concurrent-manager limit is below the soft one: "spawn.maxManagers" resolves to `+
				`%d (source: %s) but "spawn.maxManagersHard" resolves to %d `+
				`(source: %s) — refusing to spawn rather than running under a ceiling beneath its own `+
				`floor. Raise spawn.maxManagersHard in %s to at least %d, or lower spawn.maxManagers.`,
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
				"cap is %d (source: %s). Raise spawn.maxConcurrentHard in "+
				"%s to open past %d, or set spawn.maxConcurrent to 0 for unlimited.",
			count, *limits.HardLimit, limits.HardSource, *limits.Limit, limits.Source,
			configFile, *limits.HardLimit,
		)
	}
	if count >= *limits.Limit && !operatorNamed {
		return fmt.Sprintf(
			"the fleet-wide concurrent-worker limit is reached: %d live, %d "+
				"allowed (source: %s). Open nothing further and report the remainder as "+
				"held-on-limit; it is picked up next sweep. An OPERATOR-NAMED task may still open here — this one was "+
				"not named, so it is refused. Raise spawn.maxConcurrent in %s, or "+
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
				`spawn.maxManagers to 0 for unlimited.`,
			ManagerFleetFull, count, *limits.HardLimit, limits.HardSource, configFile,
		)
	}
	if count >= *limits.Limit && !operatorNamed {
		return fmt.Sprintf(
			`the concurrent-manager limit is reached: %d live managers (Fleet Manager excluded), `+
				`%d allowed — "spawn.maxManagers" (source: %s). An OPERATOR-NAMED manager `+
				`may still open here — this one was not named, so it is refused. Raise spawn.maxManagers in `+
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
