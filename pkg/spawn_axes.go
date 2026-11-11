// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bborbe/errors"
)

// The seven soft/hard axes the fleet cap is split into.
//
// The worker pair (`spawn_limits.go`) and the manager pair are two of these
// seven. The other five exist so a burst binds on the population it actually
// grows: on 2026-11-10 the flagged-open leg opened 36 rows — 33 of them headless
// resumes — and the fleet went 18 → 58 sessions in 25 minutes, stopping only at
// the shared 50-worker hard cap while interactive tabs and managers had nothing
// left to open into. A headless sub-cap that binds FIRST is the missing control;
// the total stays as the backstop it was, not as the only wall.
//
// The operator's shape (2026-11-10, revised TWICE the same day — the final
// revision: "50 is too low to saturate my laptop"):
//
//	| axis               | soft | hard |
//	| session            |  90  | 120  |
//	| worker             |  60  |  90  |
//	| worker headless    |  45  |  70  |
//	| worker interactive |  12  |  20  |
//	| manager            |  12  |  20  |
//	| topic manager      |   3  |   5  |
//	| goal manager       |   9  |  13  |
//
// ⚠️ The children NEST under their parents, and that is a validated invariant,
// not a comment. headless + interactive ≤ worker; topic + goal ≤ manager;
// worker + manager ≤ session — at both thresholds. A config that breaks it is
// REFUSED with `NESTING_VIOLATION` rather than clamped, because a child cap that
// can outgrow its parent makes the parent's refusal unreachable: the fleet stops
// at the child with the parent's headroom never spent, and the operator's number
// silently does not mean what they wrote.
const (
	// AxisSession bounds every session the fleet runs — the last backstop.
	AxisSession = "session"
	// AxisWorker bounds every worker, headless and interactive alike.
	AxisWorker = "worker"
	// AxisHeadless bounds headless workers, the population a bulk sweep grows.
	AxisHeadless = "headless"
	// AxisInteractive bounds interactive tab workers.
	AxisInteractive = "interactive"
	// AxisManager bounds every manager.
	AxisManager = "manager"
	// AxisTopicManager bounds topic managers.
	AxisTopicManager = "topicManager"
	// AxisGoalManager bounds goal managers.
	AxisGoalManager = "goalManager"
)

// NestingViolation opens every refusal of a config whose child caps do not nest
// under their parent. Exported so a caller can recognise it without restating
// the phrase.
const NestingViolation = "NESTING_VIOLATION"

// The five axes added 2026-11-10. The worker and manager pairs keep their
// existing defaults in `spawn_limits.go` — one number per axis, named once.
const (
	// DefaultMaxSessions is the soft total-session cap an absent key resolves to.
	DefaultMaxSessions = 90
	// DefaultMaxSessionsHard is the hard total-session cap.
	DefaultMaxSessionsHard = 120
	// DefaultMaxHeadless is the soft headless-worker cap.
	DefaultMaxHeadless = 45
	// DefaultMaxHeadlessHard is the hard headless-worker cap.
	DefaultMaxHeadlessHard = 70
	// DefaultMaxInteractive is the soft interactive-worker cap.
	DefaultMaxInteractive = 12
	// DefaultMaxInteractiveHard is the hard interactive-worker cap.
	DefaultMaxInteractiveHard = 20
	// DefaultMaxTopicManagers is the soft topic-manager cap.
	DefaultMaxTopicManagers = 3
	// DefaultMaxTopicManagersHard is the hard topic-manager cap.
	DefaultMaxTopicManagersHard = 5
	// DefaultMaxGoalManagers is the soft goal-manager cap.
	DefaultMaxGoalManagers = 9
	// DefaultMaxGoalManagersHard is the hard goal-manager cap.
	DefaultMaxGoalManagersHard = 13
)

// Environment overrides for the five new axes, named as the node names them.
const (
	EnvMaxSessions          = "SUPERVISOR_MAX_SESSIONS"
	EnvMaxSessionsHard      = "SUPERVISOR_MAX_SESSIONS_HARD"
	EnvMaxHeadless          = "SUPERVISOR_MAX_HEADLESS"
	EnvMaxHeadlessHard      = "SUPERVISOR_MAX_HEADLESS_HARD"
	EnvMaxInteractive       = "SUPERVISOR_MAX_INTERACTIVE"
	EnvMaxInteractiveHard   = "SUPERVISOR_MAX_INTERACTIVE_HARD"
	EnvMaxTopicManagers     = "SUPERVISOR_MAX_TOPIC_MANAGERS"
	EnvMaxTopicManagersHard = "SUPERVISOR_MAX_TOPIC_MANAGERS_HARD"
	EnvMaxGoalManagers      = "SUPERVISOR_MAX_GOAL_MANAGERS"
	EnvMaxGoalManagersHard  = "SUPERVISOR_MAX_GOAL_MANAGERS_HARD"
)

// SpawnAxes is all seven resolved pairs.
type SpawnAxes struct {
	Session      ConcurrentLimits
	Worker       ConcurrentLimits
	Headless     ConcurrentLimits
	Interactive  ConcurrentLimits
	Manager      ConcurrentLimits
	TopicManager ConcurrentLimits
	GoalManager  ConcurrentLimits
}

// LimitsFor returns one axis's pair, and whether the axis is known.
func (a SpawnAxes) LimitsFor(axis string) (ConcurrentLimits, bool) {
	switch axis {
	case AxisSession:
		return a.Session, true
	case AxisWorker:
		return a.Worker, true
	case AxisHeadless:
		return a.Headless, true
	case AxisInteractive:
		return a.Interactive, true
	case AxisManager:
		return a.Manager, true
	case AxisTopicManager:
		return a.TopicManager, true
	case AxisGoalManager:
		return a.GoalManager, true
	default:
		return ConcurrentLimits{}, false
	}
}

// axisLabels is how an axis is called in a refusal. The two historical axes keep
// the phrases their existing readers match on (`the fleet is FULL`,
// `the manager fleet is FULL`); the rest are named plainly.
var axisLabels = map[string]string{
	AxisSession:      "session",
	AxisWorker:       "worker",
	AxisHeadless:     "worker headless",
	AxisInteractive:  "worker interactive",
	AxisManager:      "manager",
	AxisTopicManager: "topic manager",
	AxisGoalManager:  "goal manager",
}

// axisKeys is the config key per axis, soft first. Every key carries an explicit
// Soft/Hard suffix so the nesting invariant is readable from the names alone;
// the pre-rename spellings still parse, folded in by
// `applyLegacySpawnKeys` with a warning naming both forms.
var axisKeys = map[string][2]string{
	AxisSession:      {"maxSessionsSoft", "maxSessionsHard"},
	AxisWorker:       {"maxWorkerSoft", "maxWorkerHard"},
	AxisHeadless:     {"maxHeadlessSoft", "maxHeadlessHard"},
	AxisInteractive:  {"maxInteractiveSoft", "maxInteractiveHard"},
	AxisManager:      {"maxManagersSoft", "maxManagersHard"},
	AxisTopicManager: {"maxTopicManagersSoft", "maxTopicManagersHard"},
	AxisGoalManager:  {"maxGoalManagersSoft", "maxGoalManagersHard"},
}

// axisPlurals is the population word a refusal counts, so a reader sees
// "6 live sessions" rather than "6 live session sessions".
var axisPlurals = map[string]string{
	AxisSession:      "sessions",
	AxisWorker:       "workers",
	AxisHeadless:     "headless workers",
	AxisInteractive:  "interactive workers",
	AxisManager:      "managers",
	AxisTopicManager: "topic managers",
	AxisGoalManager:  "goal managers",
}

// axisNouns is the word `resolveThreshold` prints when it refuses a value, kept
// beside the labels so a refusal about a topic-manager limit cannot read as one
// about workers.
var axisNouns = map[string]string{
	AxisSession:      "session",
	AxisWorker:       "concurrent-worker",
	AxisHeadless:     "headless-worker",
	AxisInteractive:  "interactive-worker",
	AxisManager:      "concurrent-manager",
	AxisTopicManager: "concurrent-topic-manager",
	AxisGoalManager:  "concurrent-goal-manager",
}

// nestingRule is one parent and the children whose caps sum under it.
type nestingRule struct {
	parent   string
	children []string
}

// nesting is the invariant, as data.
var nesting = []nestingRule{
	{parent: AxisWorker, children: []string{AxisHeadless, AxisInteractive}},
	{parent: AxisManager, children: []string{AxisTopicManager, AxisGoalManager}},
	{parent: AxisSession, children: []string{AxisWorker, AxisManager}},
}

// axisSpec names one axis for resolution and for its refusal text.
type axisSpec struct {
	name        string
	rawSoft     json.RawMessage
	rawHard     json.RawMessage
	softEnv     string
	hardEnv     string
	defaultSoft int
	defaultHard int
}

// ResolveSpawnAxes resolves and validates all seven axes, then checks that the
// children nest under their parents.
//
// ⚠️ One resolution, not seven call sites. A spawn that consulted only the axis
// it thought it belonged to would miss the parent that binds, and the nesting
// check is only meaningful over the whole set.
func ResolveSpawnAxes(
	ctx context.Context,
	getenv func(string) string,
	file SpawnConfigFile,
	path string,
) (SpawnAxes, error) {
	worker, err := ResolveMaxConcurrent(
		ctx, getenv(EnvMaxConcurrent), getenv(EnvMaxConcurrentHard), file, path,
	)
	if err != nil {
		return SpawnAxes{}, err
	}
	manager, err := ResolveMaxManagers(ctx, file, path)
	if err != nil {
		return SpawnAxes{}, err
	}

	axes := SpawnAxes{Worker: worker, Manager: manager}
	for _, spec := range []axisSpec{
		{
			name: AxisSession, rawSoft: file.Spawn.MaxSessionsSoft, rawHard: file.Spawn.MaxSessionsHard,
			softEnv: getenv(EnvMaxSessions), hardEnv: getenv(EnvMaxSessionsHard),
			defaultSoft: DefaultMaxSessions, defaultHard: DefaultMaxSessionsHard,
		},
		{
			name: AxisHeadless, rawSoft: file.Spawn.MaxHeadlessSoft, rawHard: file.Spawn.MaxHeadlessHard,
			softEnv: getenv(EnvMaxHeadless), hardEnv: getenv(EnvMaxHeadlessHard),
			defaultSoft: DefaultMaxHeadless, defaultHard: DefaultMaxHeadlessHard,
		},
		{
			name: AxisInteractive, rawSoft: file.Spawn.MaxInteractiveSoft, rawHard: file.Spawn.MaxInteractiveHard,
			softEnv: getenv(EnvMaxInteractive), hardEnv: getenv(EnvMaxInteractiveHard),
			defaultSoft: DefaultMaxInteractive, defaultHard: DefaultMaxInteractiveHard,
		},
		{
			name: AxisTopicManager, rawSoft: file.Spawn.MaxTopicManagersSoft, rawHard: file.Spawn.MaxTopicManagersHard,
			softEnv: getenv(EnvMaxTopicManagers), hardEnv: getenv(EnvMaxTopicManagersHard),
			defaultSoft: DefaultMaxTopicManagers, defaultHard: DefaultMaxTopicManagersHard,
		},
		{
			name: AxisGoalManager, rawSoft: file.Spawn.MaxGoalManagersSoft, rawHard: file.Spawn.MaxGoalManagersHard,
			softEnv: getenv(EnvMaxGoalManagers), hardEnv: getenv(EnvMaxGoalManagersHard),
			defaultSoft: DefaultMaxGoalManagers, defaultHard: DefaultMaxGoalManagersHard,
		},
	} {
		resolved, err := resolveAxis(ctx, spec, path)
		if err != nil {
			return SpawnAxes{}, err
		}
		axes = axes.with(spec.name, resolved)
	}

	if refusal := NestingRefusal(axes, path); refusal != "" {
		return SpawnAxes{}, errors.Errorf(ctx, "%s", refusal)
	}
	return axes, nil
}

// with returns a copy of the axes carrying one resolved pair.
func (a SpawnAxes) with(axis string, limits ConcurrentLimits) SpawnAxes {
	switch axis {
	case AxisSession:
		a.Session = limits
	case AxisHeadless:
		a.Headless = limits
	case AxisInteractive:
		a.Interactive = limits
	case AxisTopicManager:
		a.TopicManager = limits
	case AxisGoalManager:
		a.GoalManager = limits
	}
	return a
}

// resolveAxis resolves one pair with the same rules the two historical pairs use:
// every source validated, `0` on the soft key switching BOTH off, `0` on the hard
// key refused, and a ceiling beneath its floor refused.
func resolveAxis(ctx context.Context, spec axisSpec, path string) (ConcurrentLimits, error) {
	keys := axisKeys[spec.name]
	noun := axisNouns[spec.name]

	soft, err := resolveThreshold(ctx, thresholdSpec{
		raw: spec.rawSoft, env: spec.softEnv, envName: spec.softEnv,
		key: keys[0], path: path, fallback: spec.defaultSoft, noun: noun,
	})
	if err != nil {
		return ConcurrentLimits{}, err
	}
	// The off switch disables the pair, short-circuited before the hard key is
	// read, so an unparseable hard value cannot refuse a deliberately uncapped
	// axis.
	if soft.Limit == nil {
		return ConcurrentLimits{Source: soft.Source, HardSource: soft.Source}, nil
	}

	hard, err := resolveThreshold(ctx, thresholdSpec{
		raw: spec.rawHard, env: spec.hardEnv, envName: spec.hardEnv,
		key: keys[1], path: path, fallback: spec.defaultHard, noun: noun,
	})
	if err != nil {
		return ConcurrentLimits{}, err
	}
	if hard.Limit == nil {
		return ConcurrentLimits{}, errors.Errorf(
			ctx,
			`%s is 0, which is not a hard %s limit — refusing to spawn rather than removing the `+
				`ceiling while the soft cap stays in force. Set a non-negative integer above the soft `+
				`cap, or use the off switch: "spawn.%s": 0 disables BOTH thresholds.`,
			thresholdWhere(hard.Source, spec.hardEnv, keys[1], path), noun, keys[0],
		)
	}
	if *hard.Limit < *soft.Limit {
		return ConcurrentLimits{}, errors.Errorf(
			ctx,
			`the hard %s limit is below the soft one: "spawn.%s" resolves to %d (source: %s) but `+
				`"spawn.%s" resolves to %d (source: %s) — refusing to spawn rather than running under a `+
				`ceiling beneath its own floor. Raise spawn.%s in %s to at least %d, or lower spawn.%s.`,
			noun, keys[0], *soft.Limit, soft.Source, keys[1], *hard.Limit, hard.Source,
			keys[1], path, *soft.Limit, keys[0],
		)
	}
	return ConcurrentLimits{
		Limit:      soft.Limit,
		HardLimit:  hard.Limit,
		Source:     soft.Source,
		HardSource: hard.Source,
	}, nil
}

// NestingRefusal checks the nesting invariant on the resolved axes and returns a
// `NESTING_VIOLATION` string, or "" when every child set fits under its parent at
// both thresholds.
//
// Refused rather than clamped: which of the two numbers the operator meant is not
// ours to guess, and the same rule `ResolveMaxConcurrent` applies to a ceiling
// beneath its floor — a limit that silently governs nothing is discovered only by
// the load it was meant to bound.
func NestingRefusal(axes SpawnAxes, path string) string {
	for _, rule := range nesting {
		for _, tier := range []string{"soft", "hard"} {
			if refusal := nestingTierRefusal(axes, rule, tier, path); refusal != "" {
				return refusal
			}
		}
	}
	return ""
}

// nestingTierRefusal checks one rule at one threshold.
func nestingTierRefusal(axes SpawnAxes, rule nestingRule, tier, path string) string {
	parentLimit := axisLimitForTier(axes, rule.parent, tier)
	// A nil parent limit is the off switch — an unlimited parent cannot be outgrown.
	if parentLimit == nil {
		return ""
	}

	sum := 0
	parts := ""
	for _, child := range rule.children {
		value := axisLimitForTier(axes, child, tier)
		// A nil child is an axis switched off (`0`), which places no constraint of its
		// own — the parent still bounds the population, so its refusal stays reachable
		// and the nesting invariant is not broken.
		if value == nil {
			continue
		}
		if parts != "" {
			parts += " + "
		}
		parts += fmt.Sprintf(`"spawn.%s" %d`, axisKeys[child][keyIndex(tier)], *value)
		sum += *value
	}
	if sum <= *parentLimit {
		return ""
	}

	parentKey := axisKeys[rule.parent][keyIndex(tier)]
	return fmt.Sprintf(
		`%s: the %s caps nest wrongly — %s = %d exceeds the parent "%s" cap of %d `+
			`("spawn.%s" in %s). Every child cap must nest under its parent, or the parent's `+
			`own refusal is unreachable and its number means nothing. Lower a child key, or `+
			`raise "spawn.%s".`,
		NestingViolation, tier, parts, sum, axisLabels[rule.parent], *parentLimit,
		parentKey, path, parentKey,
	)
}

// axisLimitForTier returns one axis's limit at the named threshold.
func axisLimitForTier(axes SpawnAxes, axis, tier string) *int {
	limits, _ := axes.LimitsFor(axis)
	if tier == "hard" {
		return limits.HardLimit
	}
	return limits.Limit
}

// keyIndex maps a threshold tier to its position in the axis key pair.
func keyIndex(tier string) int {
	if tier == "hard" {
		return 1
	}
	return 0
}

// AxisLimitRefusal decides one axis — a refusal string, or "" to allow the spawn.
//
// Same ordering as `ConcurrentLimitRefusal`: the hard cap first (it refuses
// everyone), a nil liveCount refuses (a limit that cannot count must not open),
// then the soft cap with `operatorNamed` as the caller's assertion.
//
// ⚠️ The refusal NAMES THE AXIS and its numbers, which is the whole point of the
// split. A reader who sees "the worker headless cap is FULL: 45 live against 45"
// knows to wait out the headless burst; "the fleet is full" told them nothing.
func AxisLimitRefusal(
	axes SpawnAxes,
	axis string,
	liveCount *int,
	countError string,
	operatorNamed bool,
	configFile string,
) string {
	limits, known := axes.LimitsFor(axis)
	if !known || limits.Limit == nil {
		return ""
	}
	label := axisLabels[axis]
	keys := axisKeys[axis]

	if liveCount == nil {
		cause := ""
		if countError != "" {
			cause = "The count failed: " + countError + "."
		}
		return fmt.Sprintf(
			"the live %s count could not be taken, so it is unknown — refusing rather than opening "+
				"past the %s cap that cannot be counted. %s The %s cap now in effect is %d "+
				"(source: %s) — the count is what failed, not the limit.",
			label, label, cause, label, *limits.Limit, limits.Source,
		)
	}
	count := *liveCount
	if limits.HardLimit != nil && count >= *limits.HardLimit {
		// The manager hard refusal keeps its historical opener: `commands/open.md`
		// § Step 1.5 triggers its fleet-full card on that phrase, and a reworded
		// refusal would silently stop minting it.
		opener := "the " + label + " cap is FULL"
		if axis == AxisManager {
			opener = ManagerFleetFull
		}
		return fmt.Sprintf(
			`%s: %d live %s against a hard cap of %d (axis "%s", "spawn.%s", source: %s), `+
				`so nothing opens here — not even an operator-named task. The soft cap is %d `+
				`(source: %s). Raise "spawn.%s" in %s, or set "spawn.%s" to 0 for unlimited.`,
			opener, count, axisPlurals[axis], *limits.HardLimit, label, keys[1], limits.HardSource,
			*limits.Limit, limits.Source, keys[1], configFile, keys[0],
		)
	}
	if count >= *limits.Limit && !operatorNamed {
		return fmt.Sprintf(
			`the %s cap is reached: %d live, %d allowed (axis "%s", "spawn.%s", source: %s). `+
				`An OPERATOR-NAMED task may still open here — this one was not named, so it is `+
				`refused. Raise "spawn.%s" in %s, or set it to 0 for unlimited.`,
			label, count, *limits.Limit, label, keys[0], limits.Source, keys[0], configFile,
		)
	}
	return ""
}
