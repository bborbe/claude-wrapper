// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("OpenExecutor replay", func() {
	ginkgo.It("AC17: the 2026-11-18 batch replay", func() {
		ctx := context.Background()
		dir := ginkgo.GinkgoT().TempDir()

		claudeScript := filepath.Join(dir, "cc-v1")
		gomega.Expect(os.WriteFile(claudeScript, []byte("#!/bin/sh\n"), 0o755)).
			To(gomega.Succeed())
		gomega.Expect(os.WriteFile(claudeScript+"-claude", []byte("#!/bin/sh\n"), 0o755)).
			To(gomega.Succeed())
		vault := Vault{
			Name:            "v1",
			Path:            dir,
			ClaudeScript:    claudeScript,
			ManagerLauncher: "/scripts/mgr",
		}

		// 13 agent rows: 5 stored interactive, 8 stored headless, all recorded.
		var rows []OpenRow
		for i := 1; i <= 13; i++ {
			name := fmt.Sprintf("A%02d", i)
			mode := SpawnModeHeadless
			if i <= 5 {
				mode = SpawnModeInteractive
			}
			writeOpenPage(dir, openTaskTestFolder, name,
				"role: agent", "mode: "+mode, "claude_session_id: "+openExecTestUUID(100+i))
			rows = append(rows, OpenRow{Kind: OpenKindTask, Vault: "v1", Name: name})
		}
		writeOpenPage(dir, openTaskTestFolder, "H", "role: human")
		rows = append(rows, OpenRow{Kind: OpenKindTask, Vault: "v1", Name: "H"})
		writeOpenPage(dir, openGoalTestFolder, "G")
		rows = append(rows, OpenRow{Kind: OpenKindGoal, Vault: "v1", Name: "G"})

		registry := &fakeRegistry{names: map[string][]string{}}
		tabs := &fakeTabClient{}
		tabs.onSpawn = func() {
			last := tabs.spawned[len(tabs.spawned)-1]
			registry.names[last.TabName] = []string{"sess-" + last.TabName}
		}
		scripts := &fakeScriptRunner{results: map[string]ScriptResult{
			openAnswerPathScript:           {ExitCode: 0},
			"/scripts/manager-launcher.py": {Stdout: "/scripts/cc-mgr\tmanager-default\n"},
		}}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, registry, nil, &fakeLedger{}, scripts,
		)
		opener.vaults = &fakeVaults{vaults: []Vault{vault}}

		roleMap := &fakeRoleMap{entries: map[string]RoleMapEntry{
			"Direct":   {Chip: new("cyan"), WindowID: new(12)},
			"Managers": {Chip: new("orange"), WindowID: new(14)},
			"Hold":     {WindowID: new(2)},
		}}
		headless := &fakeHeadlessOpener{}
		executor := NewOpenExecutor(OpenExecutorDeps{
			Vaults:           &fakeVaults{vaults: []Vault{vault}},
			Registry:         registry,
			Panes:            &fakeJoiner{panes: map[int]string{}},
			RoleMap:          roleMap,
			Tabs:             tabs,
			TabOpener:        opener,
			Headless:         headless,
			Runner:           &recordingCommandRunner{},
			Scripts:          scripts,
			AnswerPathScript: openAnswerPathScript,
			Logf:             func(string, ...any) {},
		})

		verdicts := executor.OpenRequest(ctx, rows, "caller-1")

		gomega.Expect(verdicts).To(gomega.HaveLen(15))
		gomega.Expect(headless.requests).To(gomega.HaveLen(13))
		for _, request := range headless.requests {
			gomega.Expect(request.Resume).NotTo(gomega.BeEmpty())
		}
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(2))
		gomega.Expect([]string{tabs.spawned[0].TabName, tabs.spawned[1].TabName}).
			To(gomega.ConsistOf("H", "G Manager"))
		for _, spawn := range tabs.spawned {
			gomega.Expect(spawn.WindowID).NotTo(gomega.BeNil())
			gomega.Expect(*spawn.WindowID).NotTo(gomega.Equal(2))
		}
		gomega.Expect(windowOf(tabs.spawned, "H")).To(gomega.Equal(12))
		gomega.Expect(windowOf(tabs.spawned, "G Manager")).To(gomega.Equal(14))
		// The role map is read once per tab row and never for an agent row.
		gomega.Expect(roleMap.reads).To(gomega.Equal(2))
	})
})

// windowOf returns the window a tab spawn with the given name used.
func windowOf(spawns []TabSpawnOptions, tabName string) int {
	for _, spawn := range spawns {
		if spawn.TabName == tabName && spawn.WindowID != nil {
			return *spawn.WindowID
		}
	}
	return -1
}
