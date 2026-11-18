// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"

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
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("Rule 2"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("interactive=false"))
		// The refusal fires before the spawn's first side effect: no pane opened.
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an absent role, which defaults to agent, when it asks for a tab", func() {
		interactive := true
		_, err := server.spawnLocalTarget(
			ctx,
			spawnAgentArgs{Prompt: "x", Interactive: &interactive},
			"spawner",
		)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("Rule 2"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("interactive=false"))
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
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

	ginkgo.It("surfaces the agent refusal as a tool error, not a transport error", func() {
		for _, arguments := range []string{
			`{"interactive":true,"role":"agent","prompt":"x","cwd":"/tmp"}`,
			`{"interactive":true,"prompt":"x","cwd":"/tmp"}`,
		} {
			result, rpcErr := server.callTool(
				ctx,
				json.RawMessage(`{"name":"spawn_agent","arguments":`+arguments+`}`),
				"caller",
			)

			gomega.Expect(rpcErr).To(gomega.BeNil())
			body, ok := result.(map[string]any)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(body["isError"]).To(gomega.Equal(true))
			content, ok := body["content"].([]any)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(content).To(gomega.HaveLen(1))
			first, ok := content[0].(map[string]any)
			gomega.Expect(ok).To(gomega.BeTrue())
			text, ok := first["text"].(string)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(text).To(gomega.ContainSubstring("Rule 2"))
			gomega.Expect(text).To(gomega.ContainSubstring("interactive=false"))
			gomega.Expect(spawner.requests).To(gomega.BeEmpty())
		}
	})
})
