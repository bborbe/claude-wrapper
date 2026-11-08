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

// These specs drive spawn_agent through the MCP adapter — raw JSON arguments in,
// SpawnOptions out — because that boundary is where resume-by-id was lost: the
// engine underneath already resumed, but the adapter typed `resume` as a bool and
// never forwarded it, so every headless "resume" opened a fresh conversation.
var _ = ginkgo.Describe("RosterToolServer.spawnAgent resume", func() {
	const resumed = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	var (
		ctx        context.Context
		supervisor *toolSupervisor
		server     *RosterToolServer
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		supervisor = &toolSupervisor{}
		server = &RosterToolServer{Supervisor: supervisor, Spawner: &fakeSpawner{}}
	})

	spawn := func(args string) (string, error) {
		return server.spawnAgent(ctx, json.RawMessage(args), "")
	}

	ginkgo.It("forwards a session id to a headless spawn, so the child resumes it", func() {
		_, err := spawn(`{"resume":"` + resumed + `","interactive":false,"cwd":"/tmp"}`)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		options := supervisor.spawned[0]
		gomega.Expect(options.Resume).To(gomega.Equal(resumed))

		// The argv the supervisor builds from exactly these options must carry the
		// id after --resume, and must not mint a second identity alongside it.
		args := buildArgs("11111111-2222-4333-8444-555555555555", options, "/tmp/generated.json")
		gomega.Expect(indexOf(args, "--resume")).To(gomega.BeNumerically(">=", 0))
		gomega.Expect(indexOf(args, "--resume") + 1).To(gomega.Equal(indexOf(args, resumed)))
		gomega.Expect(args).NotTo(gomega.ContainElement("--session-id"))
	})

	ginkgo.It("spawns a fresh conversation when no resume is given", func() {
		_, err := spawn(`{"interactive":false,"cwd":"/tmp"}`)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		gomega.Expect(supervisor.spawned[0].Resume).To(gomega.BeEmpty())
	})

	// The pre-fix shape could not name a conversation. Decoding it must fail and
	// name the field — a silent fresh spawn on a resume request is the defect.
	ginkgo.It("refuses the old boolean shape with an error naming the field", func() {
		_, err := spawn(`{"resume":true,"interactive":false,"cwd":"/tmp"}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("resume"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("still refuses a resume on the tab path", func() {
		_, err := spawn(`{"resume":"` + resumed + `","cwd":"/tmp","prompt":"x"}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("headless worker only"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})
})
