// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// A manager is interactive always (role-mode matrix Rule 2). A headless manager
// has no tab, no pane and no jump link, and the pane wake cannot reach it, so its
// loop is never armed. Measured 2026-11-19: `census-manager-mode.py` found live
// headless managers carrying goal session ids.
var _ = ginkgo.Describe("RosterToolServer manager headless refusal", func() {
	const resumeID = "1c05c414-269b-4a65-887e-bb8dc25f5b1e"

	var ctx context.Context
	var server *RosterToolServer
	var supervisor *headlessSupervisor

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		supervisor = &headlessSupervisor{}
		server = &RosterToolServer{Supervisor: supervisor}
	})

	ginkgo.It("test_spawn_and_resume_refuse_a_manager_headless", func() {
		headless := false

		// A fresh spawn of a manager row that asks for a headless worker.
		_, spawnErr := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Prompt: "x", Cwd: "/tmp", Role: RoleManager, Interactive: &headless},
			"spawner",
		)
		gomega.Expect(spawnErr).NotTo(gomega.BeNil())
		gomega.Expect(errors.Is(spawnErr, ErrManagerHeadless)).To(gomega.BeTrue())
		gomega.Expect(spawnErr.Error()).To(gomega.ContainSubstring("manager headless refused"))

		// A resume of a manager row that asks for a headless worker. The guard
		// fires before the resume's own guards, so no prompt is needed to reach it.
		_, resumeErr := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Resume: resumeID, Cwd: "/tmp", Role: RoleManager, Interactive: &headless},
			"spawner",
		)
		gomega.Expect(resumeErr).NotTo(gomega.BeNil())
		gomega.Expect(errors.Is(resumeErr, ErrManagerHeadless)).To(gomega.BeTrue())

		// Negative evidence: the refusals created no session.
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())

		// Positive control: a role: agent headless spawn is unaffected.
		_, agentErr := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Prompt: "x", Cwd: "/tmp", Role: RoleAgent, Interactive: &headless},
			"spawner",
		)
		gomega.Expect(agentErr).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
	})

	ginkgo.It("refuses a manager headless spawn chosen by the config, not the argument", func() {
		// No `interactive` argument: the mode comes from the resolved source. A
		// guard keyed on the argument alone would let this one through.
		server.SpawnMode = fixedModeSources{sources: SpawnModeSources{
			Config:     json.RawMessage(`"headless"`),
			ConfigPath: "/tmp/config.json",
		}}
		_, err := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Prompt: "x", Cwd: "/tmp", Role: RoleManager},
			"spawner",
		)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(errors.Is(err, ErrManagerHeadless)).To(gomega.BeTrue())
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})
})
