// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("flagged reopen prompt", func() {
	ginkgo.Context("a task row", func() {
		ginkgo.It("puts the readiness gate before the work command", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "task", Name: "Some Task"})

			gomega.Expect(prompt).To(gomega.HavePrefix("FIRST STEP — readiness gate"))
			gomega.Expect(prompt).To(gomega.ContainSubstring(`/vault-cli:work-on-task "Some Task"`))
			gomega.Expect(prompt).To(gomega.ContainSubstring("READINESS:"))
			// The gate must come FIRST: measured 2026-11-18, a trailing gate let the
			// chained work-on-task consume the turn, so the worker stopped at planning
			// and printed the gaps instead of posting the card the gate requires.
			gomega.Expect(strings.Index(prompt, "FIRST STEP")).
				To(gomega.BeNumerically("<", strings.Index(prompt, "/vault-cli:work-on-task")))
			gomega.Expect(prompt).To(gomega.ContainSubstring("Do NOT run the command below"))
		})
	})

	ginkgo.Context("a non-task row", func() {
		ginkgo.It("stays the bare open command, because Gate 2 runs there", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "goal", Name: "Some Goal"})

			gomega.Expect(prompt).To(gomega.Equal(`/supervisor:open "Some Goal"`))
		})
	})
})

var _ = ginkgo.Describe("flagged reopen of an interactive row", func() {
	ginkgo.It("refuses an agent row and opens a human row", func() {
		ctx := context.Background()
		tabs := &fakeTabClient{}
		vaults := &fakeVaults{vaults: []Vault{{
			Name:           "v1",
			Path:           ginkgo.GinkgoT().TempDir(),
			ClaudeScript:   "/scripts/cc-v1",
			WorkerLauncher: "/scripts/cc-v1",
		}}}
		spawner := &tabSpawner{
			tabs:     tabs,
			registry: &fakeRegistry{names: map[string][]string{}},
			roleMap: &fakeRoleMap{entries: map[string]RoleMapEntry{
				"Agents": {Chip: new("pink"), WindowID: new(16)},
			}},
			vaults: vaults,
			ledger: &fakeLedger{},
			now:    steppingClock(),
			sleep:  func(time.Duration) {},
		}
		supervisor := &recordingSupervisor{}
		reopener := NewFlaggedReopener(supervisor, spawner, vaults, &fakeLedger{}, steppingClock())

		err := reopener.Reopen(ctx, FlaggedRow{
			Kind: "task", Vault: "v1", Name: "T", Role: RoleAgent, Mode: SpawnModeInteractive,
		})
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("Rule 2"))
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
		gomega.Expect(supervisor.spawns).To(gomega.BeEmpty())

		err = reopener.Reopen(ctx, FlaggedRow{
			Kind: "task", Vault: "v1", Name: "T", Role: RoleHuman, Mode: SpawnModeInteractive,
		})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(1))
	})
})
