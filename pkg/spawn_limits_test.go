// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/a2a"
)

// writeSpawnConfig writes a config file and returns its path.
func writeSpawnConfig(body string) string {
	path := filepath.Join(ginkgo.GinkgoT().TempDir(), "config.json")
	gomega.Expect(os.WriteFile(path, []byte(body), 0o600)).To(gomega.Succeed())
	return path
}

// workerAxisConfig builds a COHERENT worker-side config: the two mode children sum
// to the worker total at both thresholds, which the nesting invariant requires. A
// test that lowers `maxConcurrent` alone is refused as a NESTING_VIOLATION, so
// every worker-cap test builds its config through here.
func workerAxisConfig(soft, hard int) string {
	headlessSoft, headlessHard := soft-1, hard-1
	if headlessSoft < 0 {
		headlessSoft = 0
	}
	if headlessHard < 0 {
		headlessHard = 0
	}
	return fmt.Sprintf(
		`{"spawn":{"maxConcurrent":%d,"maxConcurrentHard":%d,"maxHeadless":%d,`+
			`"maxHeadlessHard":%d,"maxInteractive":1,"maxInteractiveHard":1}}`,
		soft, hard, headlessSoft, headlessHard,
	)
}

// loadTestConfig parses a config body into the typed shape.
func loadTestConfig(body string) SpawnConfigFile {
	return LoadSpawnConfig(context.Background(), writeSpawnConfig(body))
}

// envOf is a getenv over a fixed map.
func envOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// fixedClock is a clock pinned at at.
func fixedClock(at time.Time) libtime.CurrentDateTimeGetter {
	clock := libtime.NewCurrentDateTime()
	clock.SetNow(libtime.DateTime(at))
	return clock
}

// capsRegistry is a SessionRegistry whose Live answer is fixed.
type capsRegistry struct {
	fakeRegistry
	live []RegistryEntry
	err  error
}

func (r *capsRegistry) Live(context.Context) ([]RegistryEntry, error) {
	return r.live, r.err
}

// testCaps builds the enforcer over a scratch config and state dir, with an
// injectable clock and a sleeper that never waits.
func testCaps(
	ledger LedgerReader,
	heartbeats HeartbeatReader,
	configPath string,
	stateDir string,
) *spawnCaps {
	return &spawnCaps{
		ledger:     ledger,
		heartbeats: heartbeats,
		now:        fixedClock(time.Unix(0, 0)),
		home:       ginkgo.GinkgoT().TempDir(),
		getenv: envOf(map[string]string{
			"SUPERVISOR_CONFIG": configPath,
			"XDG_STATE_HOME":    stateDir,
		}),
		sleep: func(context.Context, time.Duration) error { return nil },
	}
}

// liveBeat is a heartbeat row the count reads as live.
func liveBeat(sessionID string) HeartbeatRow {
	return HeartbeatRow{SessionID: sessionID, Live: true, State: "idle"}
}

var _ = ginkgo.Describe("supervisor config path", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("prefers SUPERVISOR_CONFIG", func() {
		path, err := SupervisorConfigPath(ctx, "/home/x", envOf(map[string]string{
			"SUPERVISOR_CONFIG": "/scratch/config.json",
			"XDG_CONFIG_HOME":   "/xdg",
		}))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(path).To(gomega.Equal("/scratch/config.json"))
	})

	ginkgo.It("falls back to XDG_CONFIG_HOME", func() {
		path, err := SupervisorConfigPath(ctx, "/home/x", envOf(map[string]string{
			"XDG_CONFIG_HOME": "/xdg",
		}))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(path).To(gomega.Equal("/xdg/claude-supervisor/config.json"))
	})

	ginkgo.It("defaults under the home directory", func() {
		path, err := SupervisorConfigPath(ctx, "/home/x", envOf(nil))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(path).To(gomega.Equal("/home/x/.config/claude-supervisor/config.json"))
	})

	ginkgo.It("resolves the state dir from XDG_STATE_HOME, then the home directory", func() {
		dir, err := SupervisorStateDir(ctx, "/home/x", envOf(map[string]string{"XDG_STATE_HOME": "/st"}))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(dir).To(gomega.Equal("/st/claude-supervisor"))

		dir, err = SupervisorStateDir(ctx, "/home/x", envOf(nil))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(dir).To(gomega.Equal("/home/x/.local/state/claude-supervisor"))
	})

	ginkgo.It("is the file the spawn mode reads too", func() {
		path := writeSpawnConfig(`{"spawn":{"mode":"headless"}}`)
		sources, err := NewSpawnModeSourcesReader(
			"/nonexistent-home", envOf(map[string]string{"SUPERVISOR_CONFIG": path}),
		).Read(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(sources.ConfigPath).To(gomega.Equal(path))
		gomega.Expect(string(sources.Config)).To(gomega.Equal(`"headless"`))
	})

	ginkgo.It("reads an unparseable file as no config, so the defaults still cap", func() {
		file := LoadSpawnConfig(ctx, writeSpawnConfig(`{not json`))
		limits, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*limits.Limit).To(gomega.Equal(DefaultMaxConcurrent))
	})
})

var _ = ginkgo.Describe("spawn limit resolvers", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("defaults the worker pair when the keys are absent", func() {
		limits, err := ResolveMaxConcurrent(ctx, "", "", SpawnConfigFile{}, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*limits.Limit).To(gomega.Equal(DefaultMaxConcurrent))
		gomega.Expect(*limits.HardLimit).To(gomega.Equal(DefaultMaxConcurrentHard))
		gomega.Expect(limits.Source).To(gomega.Equal(LimitSourceDefault))
	})

	ginkgo.It("reads the worker pair from the config file", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":2,"maxConcurrentHard":4}}`)

		limits, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*limits.Limit).To(gomega.Equal(2))
		gomega.Expect(*limits.HardLimit).To(gomega.Equal(4))
		gomega.Expect(limits.Source).To(gomega.Equal(LimitSourceConfig))
	})

	ginkgo.It("lets the environment win over the config file", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":2,"maxConcurrentHard":4}}`)

		limits, err := ResolveMaxConcurrent(ctx, "7", "9", file, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*limits.Limit).To(gomega.Equal(7))
		gomega.Expect(*limits.HardLimit).To(gomega.Equal(9))
		gomega.Expect(limits.Source).To(gomega.Equal(LimitSourceEnv))
		gomega.Expect(limits.HardSource).To(gomega.Equal(LimitSourceEnv))
	})

	ginkgo.It("still refuses a bad file value when the environment wins", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":"lots"}}`)

		_, err := ResolveMaxConcurrent(ctx, "7", "9", file, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`"spawn.maxConcurrent" in cfg.json is "lots"`))
	})

	ginkgo.It("refuses a bad environment value by its variable name", func() {
		_, err := ResolveMaxConcurrent(ctx, "-1", "", SpawnConfigFile{}, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`SUPERVISOR_MAX_CONCURRENT is "-1"`))
	})

	ginkgo.It("accepts a numeric string as a limit", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":"7","maxConcurrentHard":"9"}}`)

		limits, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*limits.Limit).To(gomega.Equal(7))
		gomega.Expect(*limits.HardLimit).To(gomega.Equal(9))
	})

	ginkgo.It("disables BOTH thresholds when the soft key is 0, ignoring a bad hard key", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":0,"maxConcurrentHard":"junk"}}`)

		limits, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(limits.Limit).To(gomega.BeNil())
		gomega.Expect(limits.HardLimit).To(gomega.BeNil())
	})

	ginkgo.It("refuses a hard key of 0 rather than reading it as unlimited", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":2,"maxConcurrentHard":0}}`)

		_, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`"spawn.maxConcurrentHard" in cfg.json is 0`))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("disables BOTH thresholds."))
	})

	ginkgo.It("names the environment variable when the hard 0 came from there", func() {
		_, err := ResolveMaxConcurrent(ctx, "", "0", SpawnConfigFile{}, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.HavePrefix("SUPERVISOR_MAX_CONCURRENT_HARD is 0"))
	})

	ginkgo.It("refuses a hard cap below its own floor", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":10,"maxConcurrentHard":5}}`)

		_, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("below the soft one"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("to at least 10"))
	})

	ginkgo.It("refuses a non-integer value", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":2.5}}`)

		_, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("not a concurrent-worker limit"))
	})

	ginkgo.It("refuses a stray boolean rather than reading it as one", func() {
		file := loadTestConfig(`{"spawn":{"maxConcurrent":true}}`)

		_, err := ResolveMaxConcurrent(ctx, "", "", file, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is true, which is not a concurrent-worker limit"))
	})

	ginkgo.It("defaults the manager pair", func() {
		limits, err := ResolveMaxManagers(ctx, SpawnConfigFile{}, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*limits.Limit).To(gomega.Equal(DefaultMaxManagers))
		gomega.Expect(*limits.HardLimit).To(gomega.Equal(DefaultMaxManagersHard))
	})

	ginkgo.It("reads the manager pair from the config file", func() {
		file := loadTestConfig(`{"spawn":{"maxManagers":10,"maxManagersHard":15}}`)

		limits, err := ResolveMaxManagers(ctx, file, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*limits.Limit).To(gomega.Equal(10))
		gomega.Expect(*limits.HardLimit).To(gomega.Equal(15))
	})

	ginkgo.It("refuses a manager hard key of 0", func() {
		file := loadTestConfig(`{"spawn":{"maxManagers":2,"maxManagersHard":0}}`)

		_, err := ResolveMaxManagers(ctx, file, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("not a hard concurrent-manager limit"))
	})

	ginkgo.It("refuses a manager hard cap below its own floor", func() {
		file := loadTestConfig(`{"spawn":{"maxManagers":10,"maxManagersHard":5}}`)

		_, err := ResolveMaxManagers(ctx, file, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("below the soft one"))
	})

	ginkgo.It("defaults the start rate and derives its interval", func() {
		rate, err := ResolveMaxStarts(ctx, "", SpawnConfigFile{}, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(*rate.Limit).To(gomega.Equal(DefaultMaxStartsPerMinute))
		gomega.Expect(rate.Interval).To(gomega.Equal(time.Minute / DefaultMaxStartsPerMinute))
	})

	ginkgo.It("treats 0 on the start rate as the off switch", func() {
		file := loadTestConfig(`{"spawn":{"maxStartsPerMinute":0}}`)

		rate, err := ResolveMaxStarts(ctx, "", file, "cfg.json")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rate.Limit).To(gomega.BeNil())
	})

	ginkgo.It("refuses a bad start rate with the start-rate wording", func() {
		_, err := ResolveMaxStarts(ctx, "fast", SpawnConfigFile{}, "cfg.json")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`SUPERVISOR_MAX_STARTS_PER_MINUTE is "fast"`))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("which is not a start rate"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("(0 for no rate limit)"))
	})
})

var _ = ginkgo.Describe("spawn limit refusals", func() {
	ginkgo.It("refuses an ordinary spawn at the soft cap, naming count, limit, source and file", func() {
		limits := ConcurrentLimits{Limit: intPtr(2), HardLimit: intPtr(50), Source: "config", HardSource: "default"}
		count := 2

		refusal := ConcurrentLimitRefusal(limits, &count, "", false, "/cfg.json")

		gomega.Expect(refusal).To(gomega.Equal(
			"the fleet-wide concurrent-worker limit is reached: 2 live, 2 allowed (source: config). " +
				"Open nothing further and report the remainder as held-on-limit; it is picked up next sweep. " +
				"An OPERATOR-NAMED task may still open here — this one was not named, so it is refused. " +
				"Raise spawn.maxConcurrent in /cfg.json, or set it to 0 for unlimited.",
		))
	})

	ginkgo.It("lets an operator-named spawn through inside the soft-to-hard band", func() {
		limits := ConcurrentLimits{Limit: intPtr(2), HardLimit: intPtr(5), Source: "config", HardSource: "config"}
		count := 3

		gomega.Expect(ConcurrentLimitRefusal(limits, &count, "", true, "/cfg.json")).To(gomega.BeEmpty())
	})

	ginkgo.It("allows an ordinary spawn below the soft cap", func() {
		limits := ConcurrentLimits{Limit: intPtr(2), HardLimit: intPtr(5), Source: "config", HardSource: "config"}
		count := 1

		gomega.Expect(ConcurrentLimitRefusal(limits, &count, "", false, "/cfg.json")).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses even an operator-named spawn at the hard cap", func() {
		limits := ConcurrentLimits{Limit: intPtr(2), HardLimit: intPtr(5), Source: "config", HardSource: "config"}
		count := 5

		refusal := ConcurrentLimitRefusal(limits, &count, "", true, "/cfg.json")

		gomega.Expect(refusal).To(gomega.HavePrefix("the fleet is FULL: 5 live workers against a hard cap of 5"))
		gomega.Expect(refusal).To(gomega.ContainSubstring("not even an operator-named task"))
		gomega.Expect(refusal).To(gomega.ContainSubstring("spawn.maxConcurrentHard in /cfg.json"))
	})

	ginkgo.It("allows everything when the pair is off", func() {
		count := 1000

		gomega.Expect(ConcurrentLimitRefusal(ConcurrentLimits{}, &count, "", false, "/cfg.json")).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses when the count cannot be taken, naming the cause", func() {
		limits := ConcurrentLimits{Limit: intPtr(2), HardLimit: intPtr(50), Source: "config", HardSource: "default"}

		refusal := ConcurrentLimitRefusal(limits, nil, "the session-ledger endpoint could not be read", false, "/cfg.json")

		gomega.Expect(refusal).To(gomega.ContainSubstring("limit is set to 2 but the live-worker count could not be taken"))
		gomega.Expect(refusal).To(gomega.ContainSubstring("The count failed: the session-ledger endpoint could not be read."))
		gomega.Expect(refusal).To(gomega.HaveSuffix("The limit is not the failure — the count is."))
	})

	ginkgo.It("refuses an ordinary manager spawn at the manager soft cap", func() {
		limits := ManagerLimits{Limit: intPtr(1), HardLimit: intPtr(5), Source: "config", HardSource: "config"}
		count := 1

		refusal := ManagerLimitRefusal(limits, &count, "", false, "/cfg.json")

		gomega.Expect(refusal).To(gomega.Equal(
			`the concurrent-manager limit is reached: 1 live managers (Fleet Manager excluded), 1 allowed — ` +
				`"spawn.maxManagers" (source: config). An OPERATOR-NAMED manager may still open here — this one ` +
				`was not named, so it is refused. Raise spawn.maxManagers in /cfg.json, or set it to 0 for unlimited.`,
		))
	})

	ginkgo.It("lets an operator-named manager through in the manager band", func() {
		limits := ManagerLimits{Limit: intPtr(1), HardLimit: intPtr(5), Source: "config", HardSource: "config"}
		count := 1

		gomega.Expect(ManagerLimitRefusal(limits, &count, "", true, "/cfg.json")).To(gomega.BeEmpty())
	})

	ginkgo.It("opens a hard-cap manager refusal with the named phrase", func() {
		limits := ManagerLimits{Limit: intPtr(1), HardLimit: intPtr(1), Source: "config", HardSource: "config"}
		count := 1

		refusal := ManagerLimitRefusal(limits, &count, "", true, "/cfg.json")

		gomega.Expect(refusal).To(gomega.HavePrefix(ManagerFleetFull + ": 1 live managers"))
		gomega.Expect(refusal).To(gomega.ContainSubstring(`hard cap of 1 — "spawn.maxManagersHard"`))
	})

	ginkgo.It("refuses an uncountable manager fleet, naming the cause and the limit", func() {
		limits := ManagerLimits{Limit: intPtr(3), HardLimit: intPtr(5), Source: "default", HardSource: "default"}

		refusal := ManagerLimitRefusal(limits, nil, "boom", false, "/cfg.json")

		gomega.Expect(refusal).To(gomega.ContainSubstring("The count failed: boom."))
		gomega.Expect(refusal).To(gomega.ContainSubstring("The limit now in effect is 3 (source: default)"))
	})
})

var _ = ginkgo.Describe("spawn cap counting", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("counts live workers, excluding managers, ghosts and the dead", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{
				{SessionID: "worker-live"},
				{SessionID: "worker-dead"},
				{SessionID: "worker-ghost"},
				{SessionID: "manager-live", Role: RoleManager},
				// A resumed worker is live load like any other — it used to be skipped here,
				// which is what made the cap blind to every session a sweep revived.
				{SessionID: "resumed-live", ResumedFrom: "older"},
			}},
			&stubHeartbeats{rows: []HeartbeatRow{
				liveBeat("worker-live"),
				// `live: true` with no state is a ghost the store keeps stamping.
				{SessionID: "worker-ghost", Live: true},
				{SessionID: "worker-dead", Live: false, State: "idle"},
				liveBeat("manager-live"),
				liveBeat("resumed-live"),
				// Live but not in the ledger: one of the operator's own sessions.
				liveBeat("unledgered"),
			}},
			writeSpawnConfig(`{}`),
			ginkgo.GinkgoT().TempDir(),
		)

		count, err := caps.countWorkers(ctx)

		gomega.Expect(err).To(gomega.BeNil())
		// worker-live + resumed-live. The manager, the ghost, the dead row and the unledgered
		// session are all still excluded, which is the control this number carries.
		gomega.Expect(count).To(gomega.Equal(2))
	})

	ginkgo.It("unions the session registry for a worker that stamps no heartbeat", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "tab"}, {SessionID: "headless"}}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("tab")}},
			writeSpawnConfig(`{}`),
			ginkgo.GinkgoT().TempDir(),
		)
		caps.registry = &capsRegistry{live: []RegistryEntry{{SessionID: "headless", PID: 1}}}

		count, err := caps.countWorkers(ctx)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(count).To(gomega.Equal(2))
	})

	ginkgo.It("counts on the heartbeat store alone when the registry is unreadable", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "tab"}}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("tab")}},
			writeSpawnConfig(`{}`),
			ginkgo.GinkgoT().TempDir(),
		)
		caps.registry = &capsRegistry{err: errors.New("no dir")}

		count, err := caps.countWorkers(ctx)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(count).To(gomega.Equal(1))
	})

	ginkgo.It("counts live managers only", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{
				{SessionID: "manager-live", Role: RoleManager},
				{SessionID: "manager-dead", Role: RoleManager},
				{SessionID: "worker-live"},
			}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("manager-live"), liveBeat("worker-live")}},
			writeSpawnConfig(`{}`),
			ginkgo.GinkgoT().TempDir(),
		)

		count, err := caps.countManagers(ctx)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(count).To(gomega.Equal(1))
	})

	ginkgo.It("refuses at the configured cap, naming the value and the config path", func() {
		path := writeSpawnConfig(workerAxisConfig(2, 50))
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "a"}, {SessionID: "b"}}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("a"), liveBeat("b")}},
			path,
			ginkgo.GinkgoT().TempDir(),
		)

		err := caps.CheckWorker(ctx, SpawnModeHeadless, false)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("2 live, 2 allowed (source: config)"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("spawn.maxConcurrent in " + path))
	})

	ginkgo.It("re-reads the config per call, so a lowered cap applies with no restart", func() {
		path := writeSpawnConfig(workerAxisConfig(5, 60))
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "a"}, {SessionID: "b"}}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("a"), liveBeat("b")}},
			path,
			ginkgo.GinkgoT().TempDir(),
		)
		gomega.Expect(caps.CheckWorker(ctx, SpawnModeHeadless, false)).To(gomega.Succeed())

		gomega.Expect(os.WriteFile(path, []byte(workerAxisConfig(2, 60)), 0o600)).To(gomega.Succeed())

		gomega.Expect(caps.CheckWorker(ctx, SpawnModeHeadless, false)).NotTo(gomega.Succeed())
	})

	ginkgo.It("lets an operator-named spawn through in the band", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "a"}, {SessionID: "b"}}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("a"), liveBeat("b")}},
			writeSpawnConfig(workerAxisConfig(2, 5)),
			ginkgo.GinkgoT().TempDir(),
		)

		gomega.Expect(caps.CheckWorker(ctx, SpawnModeHeadless, true)).To(gomega.Succeed())
	})

	ginkgo.It("does not read the stores when every worker axis is off", func() {
		caps := testCaps(
			&rosterLedger{err: errors.New("must not be read")},
			&stubHeartbeats{err: errors.New("must not be read")},
			writeSpawnConfig(`{"spawn":{"maxConcurrent":0,"maxSessions":0,"maxHeadless":0,`+
				`"maxInteractive":0,"maxManagers":0,"maxTopicManagers":0,"maxGoalManagers":0}}`),
			ginkgo.GinkgoT().TempDir(),
		)

		gomega.Expect(caps.CheckWorker(ctx, SpawnModeHeadless, false)).To(gomega.Succeed())
	})

	ginkgo.It("still reads the count when only the worker pair is off — the session cap is a separate key", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "a"}}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("a")}},
			writeSpawnConfig(`{"spawn":{"maxConcurrent":0,"maxSessions":1,"maxSessionsHard":2,"maxManagers":0}}`),
			ginkgo.GinkgoT().TempDir(),
		)

		err := caps.CheckWorker(ctx, SpawnModeHeadless, false)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`axis "session"`))
	})

	ginkgo.It("refuses when the ledger cannot be read, rather than reading zero", func() {
		caps := testCaps(
			&rosterLedger{err: errors.New("boom")},
			&stubHeartbeats{},
			writeSpawnConfig(workerAxisConfig(2, 50)),
			ginkgo.GinkgoT().TempDir(),
		)

		err := caps.CheckWorker(ctx, SpawnModeHeadless, false)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("the session-ledger endpoint could not be read: boom"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("The limit is not the failure — the count is."))
	})

	ginkgo.It("refuses when the heartbeat store cannot be read, rather than reading zero", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "a"}}},
			&stubHeartbeats{err: errors.New("boom")},
			writeSpawnConfig(workerAxisConfig(2, 50)),
			ginkgo.GinkgoT().TempDir(),
		)

		err := caps.CheckWorker(ctx, SpawnModeHeadless, false)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("the session-heartbeat endpoint could not be read"))
	})

	ginkgo.It("refuses a manager past spawn.maxManagers, quoting the count and the limit", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{{SessionID: "m1", Role: RoleManager}}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("m1")}},
			writeSpawnConfig(`{"spawn":{"maxManagers":1,"maxManagersHard":3,"maxTopicManagers":0,"maxGoalManagers":0}}`),
			ginkgo.GinkgoT().TempDir(),
		)

		err := caps.CheckManager(ctx, "", nil, false)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("1 live managers (Fleet Manager excluded), 1 allowed"))
	})
})

var _ = ginkgo.Describe("spawn start rate", func() {
	now := time.Unix(1_700_000_000, 0)
	rate := StartRate{Limit: intPtr(4), Interval: 15 * time.Second, Source: "default"}

	ginkgo.It("allows the first start with no reservation on record", func() {
		decision := StartRateDecision(rate, nil, now, "/cfg.json")

		gomega.Expect(decision.Error).To(gomega.BeEmpty())
		gomega.Expect(decision.Wait).To(gomega.Equal(time.Duration(0)))
		gomega.Expect(decision.ReservedAt.Equal(now)).To(gomega.BeTrue())
	})

	ginkgo.It("allows a start once the interval has passed", func() {
		last := now.Add(-time.Minute)

		decision := StartRateDecision(rate, &last, now, "/cfg.json")

		gomega.Expect(decision.Wait).To(gomega.Equal(time.Duration(0)))
	})

	ginkgo.It("delays a start that arrives inside the interval", func() {
		last := now.Add(-1 * time.Second)

		decision := StartRateDecision(rate, &last, now, "/cfg.json")

		gomega.Expect(decision.Error).To(gomega.BeEmpty())
		gomega.Expect(decision.Wait).To(gomega.Equal(14 * time.Second))
		gomega.Expect(decision.Message).To(gomega.ContainSubstring("delayed 14s to"))
		gomega.Expect(decision.Message).To(gomega.HaveSuffix("It is paced, not dropped."))
	})

	ginkgo.It("refuses a start past the ceiling rather than parking it", func() {
		last := now.Add(10 * time.Minute)

		decision := StartRateDecision(rate, &last, now, "/cfg.json")

		gomega.Expect(decision.Error).To(gomega.ContainSubstring("refused rather than parked"))
		gomega.Expect(decision.Error).To(gomega.ContainSubstring("past the 300s ceiling"))
		gomega.Expect(decision.Error).To(gomega.ContainSubstring("spawn.maxStartsPerMinute in /cfg.json"))
	})

	ginkgo.It("writes the reservation before it sleeps", func() {
		stateDir := ginkgo.GinkgoT().TempDir()
		stateFile := filepath.Join(stateDir, "claude-supervisor", StartRateStateFile)
		caps := testCaps(nil, nil, writeSpawnConfig(`{"spawn":{"maxStartsPerMinute":4}}`), stateDir)
		caps.now = fixedClock(now)
		var reservedAtSleep *time.Time
		caps.sleep = func(context.Context, time.Duration) error {
			reservedAtSleep = readReservedAt(stateFile)
			return nil
		}
		// Seed a recent reservation so this start is delayed and the sleeper runs.
		gomega.Expect(writeReservedAt(context.Background(), stateFile, now.Add(-1*time.Second))).To(gomega.Succeed())

		gomega.Expect(caps.ReserveStart(context.Background())).To(gomega.Succeed())

		gomega.Expect(reservedAtSleep).NotTo(gomega.BeNil())
		gomega.Expect(reservedAtSleep.Equal(now.Add(14 * time.Second))).To(gomega.BeTrue())
	})

	ginkgo.It("writes the node's state-file shape", func() {
		stateFile := filepath.Join(ginkgo.GinkgoT().TempDir(), StartRateStateFile)
		gomega.Expect(writeReservedAt(context.Background(), stateFile, now)).To(gomega.Succeed())

		raw, err := os.ReadFile(stateFile)
		gomega.Expect(err).To(gomega.BeNil())
		var parsed map[string]string
		gomega.Expect(json.Unmarshal(raw, &parsed)).To(gomega.Succeed())
		gomega.Expect(parsed).To(gomega.Equal(map[string]string{"last_reserved_at": "2023-11-14T22:13:20.000Z"}))
	})

	ginkgo.It("does not touch the state file when the rate is off", func() {
		stateDir := ginkgo.GinkgoT().TempDir()
		caps := testCaps(nil, nil, writeSpawnConfig(`{"spawn":{"maxStartsPerMinute":0}}`), stateDir)

		gomega.Expect(caps.ReserveStart(context.Background())).To(gomega.Succeed())

		_, err := os.Stat(filepath.Join(stateDir, "claude-supervisor", StartRateStateFile))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
})

// fakeSpawnCaps records which caps were consulted and returns a fixed error.
type fakeSpawnCaps struct {
	workerErr    error
	managerErr   error
	startErr     error
	workerCalls  []bool
	managerCalls []bool
	startCalls   int
	// startDelay makes ReserveStart block, standing in for a paced start.
	startDelay time.Duration
}

func (f *fakeSpawnCaps) CheckWorker(_ context.Context, _ string, operatorNamed bool) error {
	f.workerCalls = append(f.workerCalls, operatorNamed)
	return f.workerErr
}

func (f *fakeSpawnCaps) CheckManager(_ context.Context, _ string, _ []string, operatorNamed bool) error {
	f.managerCalls = append(f.managerCalls, operatorNamed)
	return f.managerErr
}

func (f *fakeSpawnCaps) ReserveStart(ctx context.Context) error {
	f.startCalls++
	if f.startDelay > 0 {
		select {
		case <-time.After(f.startDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.startErr
}

var _ = ginkgo.Describe("RosterToolServer spawn caps", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("refuses a tab spawn at the cap without opening a pane", func() {
		spawner := &fakeSpawner{}
		caps := &fakeSpawnCaps{workerErr: errors.New("the fleet-wide concurrent-worker limit is reached")}
		server := &RosterToolServer{Spawner: spawner, Caps: caps}

		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x"}, "spawner")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("concurrent-worker limit is reached"))
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
		gomega.Expect(caps.startCalls).To(gomega.Equal(0))
	})

	ginkgo.It("refuses a headless spawn at the cap before the child is created", func() {
		supervisor := &toolSupervisor{}
		caps := &fakeSpawnCaps{workerErr: errors.New("the fleet-wide concurrent-worker limit is reached")}
		server := &RosterToolServer{Supervisor: supervisor, Caps: caps}
		interactive := false

		_, err := server.spawnLocalTarget(
			ctx, spawnAgentArgs{Interactive: &interactive, Cwd: "/tmp", Prompt: "x"}, "spawner",
		)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("concurrent-worker limit is reached"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a bad argument before consulting the caps", func() {
		caps := &fakeSpawnCaps{}
		server := &RosterToolServer{Spawner: &fakeSpawner{}, Caps: caps}

		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{}, "spawner")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(caps.workerCalls).To(gomega.BeEmpty())
		gomega.Expect(caps.startCalls).To(gomega.Equal(0))
	})

	ginkgo.It("passes operator_named through from the wire to the worker cap", func() {
		spawner := &fakeSpawner{}
		caps := &fakeSpawnCaps{}
		server := &RosterToolServer{Spawner: spawner, Caps: caps}

		_, err := server.spawnAgent(ctx, json.RawMessage(`{"prompt":"x","operator_named":true}`), "spawner")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(caps.workerCalls).To(gomega.Equal([]bool{true}))
		gomega.Expect(caps.startCalls).To(gomega.Equal(1))
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("consults the manager cap only for a manager spawn", func() {
		caps := &fakeSpawnCaps{}
		server := &RosterToolServer{Spawner: &fakeSpawner{}, Caps: caps}

		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x", Role: RoleManager}, "spawner")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(caps.managerCalls).To(gomega.HaveLen(1))

		_, err = server.spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x", Role: RoleAgent}, "spawner")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(caps.managerCalls).To(gomega.HaveLen(1))
		gomega.Expect(caps.workerCalls).To(gomega.HaveLen(2))
	})

	ginkgo.It("refuses a manager spawn at the manager cap without opening a pane", func() {
		spawner := &fakeSpawner{}
		caps := &fakeSpawnCaps{managerErr: errors.New(ManagerFleetFull + ": 1 live managers")}
		server := &RosterToolServer{Spawner: spawner, Caps: caps}

		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x", Role: RoleManager}, "spawner")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(ManagerFleetFull))
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
		gomega.Expect(caps.startCalls).To(gomega.Equal(0))
	})

	ginkgo.It("does not open a pane when the start rate refuses", func() {
		spawner := &fakeSpawner{}
		caps := &fakeSpawnCaps{startErr: errors.New("refused rather than parked")}
		server := &RosterToolServer{Spawner: spawner, Caps: caps}

		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x"}, "spawner")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
	})

	// End to end through the real enforcer and the JSON wire: a scratch config with
	// the interactive sub-cap at 2 and two live interactive workers refuses an
	// ordinary tab spawn NAMING THE AXIS, and lets the same call through when
	// operator-named — the soft-cap band, on the tab path, through spawn_agent.
	//
	// ⚠️ The sub-cap is what binds here, and under the nesting invariant it always
	// will for a local spawn: the mode children sum to no more than the worker
	// total, so a count that reaches the worker cap has already reached one of its
	// children. The worker axis stays load-bearing for a worker with NO local mode
	// (cluster, a2a) — `spawn axes` covers that case.
	ginkgo.It("enforces a scratch config's cap on the tab path through spawn_agent", func() {
		path := writeSpawnConfig(`{"spawn":{"maxConcurrent":2,"maxConcurrentHard":6,` +
			`"maxInteractive":2,"maxInteractiveHard":4,"maxHeadless":0,"maxStartsPerMinute":0}}`)
		spawner := &fakeSpawner{}
		server := &RosterToolServer{
			Spawner: spawner,
			Caps: testCaps(
				&rosterLedger{rows: []LedgerRow{{SessionID: "a"}, {SessionID: "b"}}},
				&stubHeartbeats{rows: []HeartbeatRow{liveBeat("a"), liveBeat("b")}},
				path,
				ginkgo.GinkgoT().TempDir(),
			),
		}

		_, err := server.spawnAgent(ctx, json.RawMessage(`{"prompt":"x"}`), "spawner")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`the worker interactive cap is reached: 2 live, 2 allowed`))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(path))
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())

		result, err := server.spawnAgent(ctx, json.RawMessage(`{"prompt":"x","operator_named":true}`), "spawner")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
		var parsed map[string]any
		gomega.Expect(json.Unmarshal([]byte(result), &parsed)).To(gomega.Succeed())
		gomega.Expect(parsed["agent_id"]).To(gomega.Equal("s1"))
	})
})

var _ = ginkgo.Describe("spawn caps after review", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("counts a session recorded twice once, the last row winning", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{
				{SessionID: "a"},
				{SessionID: "a", Role: RoleManager},
			}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("a")}},
			writeSpawnConfig(`{}`),
			ginkgo.GinkgoT().TempDir(),
		)

		workers, err := caps.countWorkers(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(workers).To(gomega.Equal(0))

		managers, err := caps.countManagers(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(managers).To(gomega.Equal(1))
	})

	ginkgo.It("records role and resumed_from on the headless ledger row", func() {
		ledger := &rosterLedger{}
		server := &RosterToolServer{Supervisor: &toolSupervisor{}, Ledger: ledger}

		_, err := server.spawnAgent(ctx, json.RawMessage(
			`{"interactive":false,"cwd":"/tmp","role":"manager",`+
				`"resume":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}`,
		), "caller")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(ledger.posted).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posted[0].Role).To(gomega.Equal(RoleManager))
		gomega.Expect(ledger.posted[0].ResumedFrom).To(gomega.Equal("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"))
	})

	ginkgo.It("charges a headless manager spawn to the manager cap, not the worker cap", func() {
		ledger := &rosterLedger{}
		server := &RosterToolServer{Supervisor: &toolSupervisor{}, Ledger: ledger}

		_, err := server.spawnAgent(ctx, json.RawMessage(
			`{"interactive":false,"cwd":"/tmp","role":"manager"}`,
		), "caller")
		gomega.Expect(err).To(gomega.BeNil())

		// The recorded row IS the ledger's content, and every spawned session is live.
		ledger.rows = ledger.posted
		beats := make([]HeartbeatRow, 0, len(ledger.posted))
		for _, row := range ledger.posted {
			beats = append(beats, liveBeat(row.SessionID))
		}
		caps := testCaps(ledger, &stubHeartbeats{rows: beats}, writeSpawnConfig(`{}`), ginkgo.GinkgoT().TempDir())
		workers, err := caps.countWorkers(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(workers).To(gomega.Equal(0))
		managers, err := caps.countManagers(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(managers).To(gomega.Equal(1))
	})

	ginkgo.It("refuses an A2A spawn at the worker cap before the controller is called", func() {
		caps := &fakeSpawnCaps{workerErr: errors.New("the fleet-wide concurrent-worker limit is reached")}
		server := &RosterToolServer{
			A2A:  a2a.NewManager(a2a.NewClient(a2a.NewID), a2a.NewPushRegistry("host.docker.internal"), a2a.Config{}),
			Caps: caps,
		}

		_, err := server.spawnAgent(ctx, json.RawMessage(`{"target":"docker","task":"t","prompt":"x"}`), "caller")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("concurrent-worker limit is reached"))
		gomega.Expect(caps.startCalls).To(gomega.Equal(0))
	})

	ginkgo.It("reserves a start on the A2A path too", func() {
		caps := &fakeSpawnCaps{startErr: errors.New("refused rather than parked")}
		server := &RosterToolServer{
			A2A:  a2a.NewManager(a2a.NewClient(a2a.NewID), a2a.NewPushRegistry("host.docker.internal"), a2a.Config{}),
			Caps: caps,
		}

		_, err := server.spawnAgent(ctx, json.RawMessage(`{"target":"docker","task":"t","prompt":"x"}`), "caller")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("refused rather than parked"))
		gomega.Expect(caps.startCalls).To(gomega.Equal(1))
	})

	// The HIGH finding: a paced start may block far past the server's
	// WriteTimeout, and the caller must still receive its response. A 1 s server
	// timeout against a 2 s paced start fails without the deadline clear.
	ginkgo.It("delivers the response of a start paced past the server's WriteTimeout", func() {
		caps := &fakeSpawnCaps{startDelay: 2 * time.Second}
		server := &RosterToolServer{
			Roster:  NewRoster(&rosterSupervisor{sessions: []Session{}}, &rosterLedger{}, &rosterLiveness{}, nil, nil),
			Spawner: &fakeSpawner{},
			Caps:    caps,
		}
		httpServer := httptest.NewUnstartedServer(server)
		httpServer.Config.WriteTimeout = time.Second
		httpServer.Start()
		defer httpServer.Close()

		request, err := http.NewRequest(
			http.MethodPost,
			httpServer.URL+"/supervisor/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
				`"params":{"name":"spawn_agent","arguments":{"prompt":"x"}}}`),
		)
		gomega.Expect(err).To(gomega.BeNil())
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(SessionHeader, "caller")

		client := &http.Client{Timeout: 15 * time.Second}
		response, err := client.Do(request)
		gomega.Expect(err).To(gomega.BeNil())
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusOK))
		gomega.Expect(string(body)).To(gomega.ContainSubstring(`"result"`))
		gomega.Expect(string(body)).To(gomega.ContainSubstring("s1"))
	})
})
