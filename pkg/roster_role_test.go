// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("RosterToolServer.spawnLocalTarget role refusal", func() {
	var ctx context.Context
	var server *RosterToolServer
	var spawner *fakeSpawner

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		spawner = &fakeSpawner{}
		server = &RosterToolServer{Spawner: spawner}
	})

	ginkgo.It("refuses an explicit role: agent spawn that asks for a tab", func() {
		interactive := true
		_, err := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Prompt: "x", Role: RoleAgent, Interactive: &interactive},
			"spawner",
		)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("role: agent"))
		// The refusal fires before the spawn's first side effect: no pane opened.
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
	})

	// The discriminating case for the argument-vs-resolved-role keying: an absent
	// `role` resolves to `agent` (DefaultRole), so a guard keyed on the resolved
	// value would refuse this call. Keying on the caller's argument lets it open.
	ginkgo.It("still opens when the role is absent", func() {
		interactive := true
		_, err := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Prompt: "x", Interactive: &interactive},
			"spawner",
		)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("still opens for an explicit role: human tab spawn", func() {
		interactive := true
		_, err := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Prompt: "x", Role: RoleHuman, Interactive: &interactive},
			"spawner",
		)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
	})
})
