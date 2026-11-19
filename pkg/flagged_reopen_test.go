// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
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

	ginkgo.Context("a goal row", func() {
		ginkgo.It("opens through the goal loop", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "goal", Name: "Some Goal"})

			gomega.Expect(prompt).To(gomega.Equal(`/supervisor:goal-loop "Some Goal"`))
		})
	})

	ginkgo.Context("a topic row", func() {
		ginkgo.It("opens through the topic loop", func() {
			prompt := reopenPrompt(FlaggedRow{Kind: "topic", Name: "Some Topic"})

			gomega.Expect(prompt).To(gomega.Equal(`/supervisor:topic-loop "Some Topic"`))
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

var _ = ginkgo.Describe("resume-no-transcript", func() {
	cwd := "/Users/bborbe/Documents/Obsidian/private-personal"
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)

	ginkgo.It("resumes when the stamped session has a transcript", func() {
		dir := ginkgo.GinkgoT().TempDir()
		gomega.Expect(os.MkdirAll(filepath.Join(dir, slug), 0o755)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(
			filepath.Join(dir, slug, "sid-1.jsonl"), []byte("{}\n"), 0o600,
		)).To(gomega.Succeed())

		r := &flaggedReopener{ProjectsDir: dir}

		gomega.Expect(r.resumableSessionID(
			FlaggedRow{Kind: "task", Name: "T", SessionID: "sid-1"}, cwd,
		)).To(gomega.Equal("sid-1"))
	})

	ginkgo.It("re-creates when the stamped session has no transcript", func() {
		// No file written under the projects dir: the stamp points at a
		// conversation that does not exist, so the row must be created fresh.
		r := &flaggedReopener{ProjectsDir: ginkgo.GinkgoT().TempDir()}

		gomega.Expect(r.resumableSessionID(
			FlaggedRow{Kind: "task", Name: "T", SessionID: "sid-missing"}, cwd,
		)).To(gomega.BeEmpty())
	})

	ginkgo.It("keeps a row with no session id on the create path", func() {
		r := &flaggedReopener{ProjectsDir: ginkgo.GinkgoT().TempDir()}

		gomega.Expect(r.resumableSessionID(
			FlaggedRow{Kind: "task", Name: "T"}, cwd,
		)).To(gomega.BeEmpty())
	})
})
