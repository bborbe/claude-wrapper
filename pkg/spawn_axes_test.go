// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// axesFrom resolves the seven axes from a config body, with no environment.
func axesFrom(body string) (SpawnAxes, error) {
	return ResolveSpawnAxes(
		context.Background(),
		func(string) string { return "" },
		loadTestConfig(body),
		"/cfg.json",
	)
}

// mustAxes resolves the axes or fails the spec.
func mustAxes(body string) SpawnAxes {
	axes, err := axesFrom(body)
	gomega.Expect(err).To(gomega.BeNil())
	return axes
}

// pageVault builds a vault root carrying the named pages, and returns it.
func pageVault(pages ...string) string {
	root := ginkgo.GinkgoT().TempDir()
	for _, page := range pages {
		full := filepath.Join(root, page)
		gomega.Expect(os.MkdirAll(filepath.Dir(full), 0o755)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(full, []byte("---\n---\n"), 0o600)).To(gomega.Succeed())
	}
	return root
}

var _ = ginkgo.Describe("spawn axes — SC1 and SC6: seven axes, shipped defaults", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("ships the operator's table as the defaults, with no config at all", func() {
		axes, err := axesFrom(`{}`)
		gomega.Expect(err).To(gomega.BeNil())

		expected := map[string][2]int{
			AxisSession:      {60, 85},
			AxisWorker:       {40, 60},
			AxisHeadless:     {30, 45},
			AxisInteractive:  {8, 12},
			AxisManager:      {10, 15},
			AxisTopicManager: {2, 4},
			AxisGoalManager:  {7, 11},
		}
		for axis, pair := range expected {
			limits, ok := axes.LimitsFor(axis)
			gomega.Expect(ok).To(gomega.BeTrue(), axis)
			// ⚠️ A nil Limit is "unlimited". An absent key must resolve to the number
			// above, never to nil — that is the whole of SC6.
			gomega.Expect(limits.Limit).NotTo(gomega.BeNil(), axis)
			gomega.Expect(limits.HardLimit).NotTo(gomega.BeNil(), axis)
			gomega.Expect(*limits.Limit).To(gomega.Equal(pair[0]), axis)
			gomega.Expect(*limits.HardLimit).To(gomega.Equal(pair[1]), axis)
			gomega.Expect(limits.Source).To(gomega.Equal(LimitSourceDefault), axis)
		}
	})

	ginkgo.It("reads every one of the fourteen keys from the config file", func() {
		// One key at a time, with a body that keeps the nesting invariant satisfied:
		// the child keys are only raised alongside the parent they must fit under.
		cases := []struct {
			axis string
			body string
			soft int
			hard int
		}{
			{AxisSession, `{"spawn":{"maxSessions":61,"maxSessionsHard":86}}`, 61, 86},
			{AxisWorker, `{"spawn":{"maxConcurrent":41,"maxConcurrentHard":61}}`, 41, 61},
			{AxisHeadless, `{"spawn":{"maxHeadless":31,"maxHeadlessHard":46}}`, 31, 46},
			{AxisInteractive, `{"spawn":{"maxInteractive":9,"maxInteractiveHard":13}}`, 9, 13},
			{AxisManager, `{"spawn":{"maxManagers":11,"maxManagersHard":16}}`, 11, 16},
			{AxisTopicManager, `{"spawn":{"maxTopicManagers":3,"maxTopicManagersHard":5,"maxManagersHard":16}}`, 3, 5},
			{AxisGoalManager, `{"spawn":{"maxGoalManagers":8,"maxGoalManagersHard":12,"maxManagersHard":16}}`, 8, 12},
		}
		for _, c := range cases {
			axes := mustAxes(c.body)
			limits, _ := axes.LimitsFor(c.axis)
			gomega.Expect(*limits.Limit).To(gomega.Equal(c.soft), c.axis)
			gomega.Expect(*limits.HardLimit).To(gomega.Equal(c.hard), c.axis)
			gomega.Expect(limits.Source).To(gomega.Equal(LimitSourceConfig), c.axis)
		}
	})

	ginkgo.It("resolves every axis, so each has a live enforcement site", func() {
		// The axes are read by `CheckWorker` / `CheckManager` through this one
		// resolver; a single axis missing from it would be silently unenforced.
		axes := mustAxes(`{}`)
		for _, axis := range []string{
			AxisSession, AxisWorker, AxisHeadless, AxisInteractive,
			AxisManager, AxisTopicManager, AxisGoalManager,
		} {
			_, ok := axes.LimitsFor(axis)
			gomega.Expect(ok).To(gomega.BeTrue(), axis)
		}
		_ = ctx
	})
})

var _ = ginkgo.Describe("spawn axes — SC3: the nesting invariant", func() {
	ginkgo.It("accepts the shipped defaults", func() {
		gomega.Expect(NestingRefusal(mustAxes(`{}`), "/cfg.json")).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses mode children that outgrow the worker total", func() {
		_, err := axesFrom(`{"spawn":{"maxConcurrent":30,"maxHeadless":25,"maxInteractive":8}}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.HavePrefix(NestingViolation))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`"spawn.maxHeadless" 25 + "spawn.maxInteractive" 8 = 33`))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`"spawn.maxConcurrent"`))
	})

	ginkgo.It("refuses manager children that outgrow the manager total", func() {
		_, err := axesFrom(`{"spawn":{"maxManagers":8,"maxTopicManagers":2,"maxGoalManagers":7}}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.HavePrefix(NestingViolation))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`"spawn.maxGoalManagers" 7`))
	})

	ginkgo.It("refuses worker + manager above the session backstop", func() {
		_, err := axesFrom(`{"spawn":{"maxSessions":60,"maxSessionsHard":70}}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.HavePrefix(NestingViolation))
		// worker 60 + manager 15 = 75 > 70, at the HARD tier.
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("the hard caps nest wrongly"))
	})

	ginkgo.It("refuses at the soft tier too, naming it", func() {
		_, err := axesFrom(`{"spawn":{"maxConcurrent":20,"maxHeadless":15,"maxInteractive":8}}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("the soft caps nest wrongly"))
	})

	ginkgo.It("refuses rather than clamping — the resolved value is never silently reduced", func() {
		// The refusal is the contract: a clamped config would report a limit the
		// operator never wrote and could not find.
		axes, err := axesFrom(`{"spawn":{"maxConcurrent":30,"maxHeadless":25,"maxInteractive":8}}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(axes).To(gomega.Equal(SpawnAxes{}))
	})
})

var _ = ginkgo.Describe("spawn axes — SC2: a headless storm binds on its own cap", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("refuses headless at its hard cap while the interactive and manager caps have room", func() {
		rows := make([]LedgerRow, 0, DefaultMaxHeadlessHard)
		beats := make([]HeartbeatRow, 0, DefaultMaxHeadlessHard)
		for i := 0; i < DefaultMaxHeadlessHard; i++ {
			id := "h" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			rows = append(rows, LedgerRow{SessionID: id, Mode: SpawnModeHeadless})
			beats = append(beats, liveBeat(id))
		}
		caps := testCaps(&rosterLedger{rows: rows}, &stubHeartbeats{rows: beats},
			writeSpawnConfig(`{}`), ginkgo.GinkgoT().TempDir())

		err := caps.CheckWorker(ctx, SpawnModeHeadless, false)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("the worker headless cap is FULL"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`axis "worker headless"`))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("45 live worker headless sessions against a hard cap of 45"))

		// The interactive population is untouched by the headless cap. An
		// operator-named open rides the band above the worker soft cap, which the
		// 45-session headless floor sits above — the same band a named task always had.
		gomega.Expect(caps.CheckWorker(ctx, SpawnModeInteractive, true)).To(gomega.Succeed())
		gomega.Expect(caps.CheckManager(ctx, ManagerKindTopic, nil, true)).To(gomega.Succeed())
	})

	ginkgo.It("counts a resumed worker against its mode cap", func() {
		// The 2026-11-10 fix: a resume is live load like any other. A sweep that
		// revives N dead workers has added N to the headless population.
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{
				{SessionID: "r1", Mode: SpawnModeHeadless, ResumedFrom: "older"},
				{SessionID: "r2", Mode: SpawnModeHeadless, ResumedFrom: "older"},
			}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("r1"), liveBeat("r2")}},
			writeSpawnConfig(`{"spawn":{"maxConcurrent":0,"maxManagers":0,"maxHeadless":2,"maxHeadlessHard":3}}`),
			ginkgo.GinkgoT().TempDir(),
		)

		err := caps.CheckWorker(ctx, SpawnModeHeadless, false)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`axis "worker headless"`))
	})
})

var _ = ginkgo.Describe("spawn axes — SC4: the refusal names the axis", func() {
	ginkgo.It("names the headless axis and its numbers", func() {
		axes := mustAxes(`{"spawn":{"maxConcurrent":0,"maxHeadless":3,"maxHeadlessHard":4}}`)
		refusal := AxisLimitRefusal(axes, AxisHeadless, intPtr(4), "", false, "/cfg.json")

		gomega.Expect(refusal).To(gomega.ContainSubstring("the worker headless cap is FULL"))
		gomega.Expect(refusal).To(gomega.ContainSubstring(`axis "worker headless"`))
		gomega.Expect(refusal).To(gomega.ContainSubstring(`"spawn.maxHeadlessHard"`))
		gomega.Expect(refusal).To(gomega.ContainSubstring("4 live worker headless sessions against a hard cap of 4"))
	})

	ginkgo.It("names the manager axis and keeps the phrase the open card matches on", func() {
		axes := mustAxes(`{"spawn":{"maxManagers":1,"maxManagersHard":2,"maxTopicManagers":0,"maxGoalManagers":0}}`)
		refusal := AxisLimitRefusal(axes, AxisManager, intPtr(2), "", true, "/cfg.json")

		gomega.Expect(refusal).To(gomega.HavePrefix(ManagerFleetFull))
		gomega.Expect(refusal).To(gomega.ContainSubstring(`axis "manager"`))
		gomega.Expect(refusal).To(gomega.ContainSubstring(`"spawn.maxManagersHard"`))
	})

	ginkgo.It("names the topic-manager axis", func() {
		axes := mustAxes(`{"spawn":{"maxTopicManagers":1,"maxTopicManagersHard":2}}`)
		refusal := AxisLimitRefusal(axes, AxisTopicManager, intPtr(2), "", false, "/cfg.json")

		gomega.Expect(refusal).To(gomega.ContainSubstring("the topic manager cap is FULL"))
		gomega.Expect(refusal).To(gomega.ContainSubstring(`axis "topic manager"`))
	})

	ginkgo.It("lets an operator-named spawn through at the soft cap, on every axis", func() {
		axes := mustAxes(`{"spawn":{"maxHeadless":2,"maxHeadlessHard":4}}`)

		gomega.Expect(AxisLimitRefusal(axes, AxisHeadless, intPtr(2), "", true, "/cfg.json")).To(gomega.BeEmpty())
		gomega.Expect(AxisLimitRefusal(axes, AxisHeadless, intPtr(2), "", false, "/cfg.json")).NotTo(gomega.BeEmpty())
		gomega.Expect(AxisLimitRefusal(axes, AxisHeadless, intPtr(4), "", true, "/cfg.json")).NotTo(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("spawn axes — SC5: the session backstop", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("counts every session kind — headless, interactive, cluster, a2a and managers", func() {
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{
				{SessionID: "w1", Mode: SpawnModeHeadless},
				{SessionID: "w2", Mode: SpawnModeInteractive},
				{SessionID: "w3", Mode: "cluster"},
				{SessionID: "w4", Mode: "a2a"},
				{SessionID: "m1", Role: RoleManager, Label: "Some Topic"},
			}},
			&stubHeartbeats{rows: []HeartbeatRow{
				liveBeat("w1"), liveBeat("w2"), liveBeat("w3"), liveBeat("w4"), liveBeat("m1"),
			}},
			writeSpawnConfig(`{"spawn":{"maxConcurrent":0,"maxManagers":0,"maxSessions":5,"maxSessionsHard":6}}`),
			ginkgo.GinkgoT().TempDir(),
		)

		// 4 workers + 1 manager = 5, which is the soft cap: an ordinary spawn is
		// refused and a named one still opens.
		err := caps.CheckWorker(ctx, SpawnModeHeadless, false)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`axis "session"`))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("5 live, 5 allowed"))

		gomega.Expect(caps.CheckWorker(ctx, SpawnModeHeadless, true)).To(gomega.Succeed())
	})

	ginkgo.It("is the last backstop — at the session hard cap even an operator-named spawn is refused", func() {
		rows := []LedgerRow{
			{SessionID: "w1", Mode: SpawnModeHeadless},
			{SessionID: "w2", Mode: SpawnModeInteractive},
			{SessionID: "w3", Mode: "cluster"},
			{SessionID: "w4", Mode: "a2a"},
			{SessionID: "m1", Role: RoleManager, Label: "Some Topic"},
			{SessionID: "m2", Role: RoleManager, Label: "Some Goal"},
		}
		beats := []HeartbeatRow{
			liveBeat("w1"), liveBeat("w2"), liveBeat("w3"),
			liveBeat("w4"), liveBeat("m1"), liveBeat("m2"),
		}
		caps := testCaps(&rosterLedger{rows: rows}, &stubHeartbeats{rows: beats},
			writeSpawnConfig(`{"spawn":{"maxConcurrent":0,"maxManagers":0,"maxSessions":5,"maxSessionsHard":6}}`),
			ginkgo.GinkgoT().TempDir())

		err := caps.CheckWorker(ctx, SpawnModeHeadless, true)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`axis "session"`))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("6 live session sessions against a hard cap of 6"))

		managerErr := caps.CheckManager(ctx, ManagerKindTopic, nil, true)
		gomega.Expect(managerErr).NotTo(gomega.BeNil())
		gomega.Expect(managerErr.Error()).To(gomega.ContainSubstring(`axis "session"`))
	})
})

var _ = ginkgo.Describe("manager kind — the topic/goal split", func() {
	ginkgo.It("classifies a label whose subject is a topic page", func() {
		root := pageVault("23 Topics/Some Topic.md")
		gomega.Expect(ManagerKindForLabel("Some Topic", []string{root}, FileExists)).To(gomega.Equal(ManagerKindTopic))
	})

	ginkgo.It("classifies a label whose subject is a goal page, under either goal directory", func() {
		root := pageVault("24 Goals/Some Goal.md")
		gomega.Expect(ManagerKindForLabel("Some Goal", []string{root}, FileExists)).To(gomega.Equal(ManagerKindGoal))

		legacy := pageVault("23 Goals/Legacy Goal.md")
		gomega.Expect(ManagerKindForLabel("Legacy Goal", []string{legacy}, FileExists)).To(gomega.Equal(ManagerKindGoal))
	})

	ginkgo.It("accepts the `<Title> Manager` spelling a launcher produces", func() {
		root := pageVault("23 Topics/Some Topic.md")
		gomega.Expect(ManagerKindForLabel("Some Topic Manager", []string{root}, FileExists)).
			To(gomega.Equal(ManagerKindTopic))
	})

	ginkgo.It("matches case-insensitively, because a registry name is not normalised", func() {
		root := pageVault("24 Goals/Some Goal.md")
		gomega.Expect(ManagerKindForLabel("some goal", []string{root}, FileExists)).To(gomega.Equal(ManagerKindGoal))
	})

	ginkgo.It("answers fleet for the board's root, which no manager cap counts", func() {
		gomega.Expect(ManagerKindForLabel(FleetManagerLabel, nil, FileExists)).To(gomega.Equal(ManagerKindFleet))
	})

	ginkgo.It("answers unresolved rather than guessing when no page resolves", func() {
		root := pageVault("23 Topics/Other Topic.md")
		gomega.Expect(ManagerKindForLabel("Missing Subject", []string{root}, FileExists)).
			To(gomega.Equal(ManagerKindUnresolved))
		gomega.Expect(ManagerKindForLabel("", []string{root}, FileExists)).To(gomega.Equal(ManagerKindUnresolved))
	})

	ginkgo.It("maps topic and goal kinds to their axes, and fleet/unresolved to none", func() {
		gomega.Expect(managerKindAxis(ManagerKindTopic)).To(gomega.Equal(AxisTopicManager))
		gomega.Expect(managerKindAxis(ManagerKindGoal)).To(gomega.Equal(AxisGoalManager))
		gomega.Expect(managerKindAxis(ManagerKindFleet)).To(gomega.BeEmpty())
		gomega.Expect(managerKindAxis(ManagerKindUnresolved)).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a topic manager at its own cap while the goal pair has room", func() {
		ctx := context.Background()
		root := pageVault("23 Topics/Some Topic.md", "24 Goals/Some Goal.md")
		caps := testCaps(
			&rosterLedger{rows: []LedgerRow{
				{SessionID: "t1", Role: RoleManager, Label: "Some Topic", Cwd: root},
				{SessionID: "t2", Role: RoleManager, Label: "Some Topic", Cwd: root},
			}},
			&stubHeartbeats{rows: []HeartbeatRow{liveBeat("t1"), liveBeat("t2")}},
			writeSpawnConfig(`{"spawn":{"maxManagers":10,"maxManagersHard":15,`+
				`"maxTopicManagers":2,"maxTopicManagersHard":4,"maxGoalManagers":7,"maxGoalManagersHard":11}}`),
			ginkgo.GinkgoT().TempDir(),
		)

		err := caps.CheckManager(ctx, ManagerKindTopic, []string{root}, false)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("the topic manager cap is reached"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(`axis "topic manager"`))

		// The goal pair is a different population and still has room.
		gomega.Expect(caps.CheckManager(ctx, ManagerKindGoal, []string{root}, false)).To(gomega.Succeed())
	})
})
