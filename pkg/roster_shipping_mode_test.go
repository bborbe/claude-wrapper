// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// These specs pin the `shipping` → permission-mode mapping on the HEADLESS spawn path.
//
// Before it existed `spawn_agent` decoded `shipping` and dropped it: every child started
// in `auto` and the reply never mentioned the mode, so a caller could not tell. Measured
// 2026-11-11 — 119 of 120 live children in `auto`, none in `acceptEdits`.
var _ = ginkgo.Describe("RosterToolServer.spawnAgent headless spawn permission mode", func() {
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

	// spawn calls the tool and returns the decoded reply, so a case can read the keys
	// the caller actually sees rather than a value the test recomputed.
	spawn := func(args string) map[string]string {
		reply, err := server.spawnAgent(ctx, json.RawMessage(args), "")
		gomega.Expect(err).To(gomega.BeNil())
		var decoded map[string]string
		gomega.Expect(json.Unmarshal([]byte(reply), &decoded)).To(gomega.Succeed())
		return decoded
	}

	ginkgo.DescribeTable("hands the supervisor the mode the shipping flag decides",
		func(name, args, wantMode, wantSource string, wantResume bool) {
			reply := spawn(args)
			gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
			options := supervisor.spawned[0]

			// Read from what the supervisor was HANDED, never from the reply alone: a
			// reply-only assertion would pass on a hardcoded echo.
			gomega.Expect(options.PermissionMode).To(gomega.Equal(wantMode))
			gomega.Expect(reply["permission_mode"]).To(gomega.Equal(wantMode))
			gomega.Expect(reply["permission_mode_source"]).To(gomega.Equal(wantSource))

			args2 := buildArgs("11111111-2222-4333-8444-555555555555", options, "/tmp/generated.json")
			gomega.Expect(indexOf(args2, "--permission-mode") + 1).
				To(gomega.Equal(indexOf(args2, wantMode)))
			if wantResume {
				gomega.Expect(indexOf(args2, "--resume") + 1).
					To(gomega.Equal(indexOf(args2, resumed)))
			}

			// The verdict line the spec's evidence reads.
			fmt.Printf("case=%s requested=%s source=%s\n", name, wantMode, reply["permission_mode_source"])
		},
		ginkgo.Entry("shipping", "shipping", `{"shipping":true,"interactive":false,"cwd":"/tmp"}`,
			ShippingPermissionMode, PermissionModeSourceShipping, false),
		ginkgo.Entry("no-shipping", "no-shipping", `{"interactive":false,"cwd":"/tmp"}`,
			DefaultPermissionMode, PermissionModeSourceDefault, false),
		ginkgo.Entry("shipping-with-stray-permission-mode", "shipping-with-stray-permission-mode",
			`{"shipping":true,"permission_mode":"bypassPermissions","interactive":false,"cwd":"/tmp"}`,
			ShippingPermissionMode, PermissionModeSourceShipping, false),
		ginkgo.Entry("resume-shipping", "resume-shipping",
			`{"resume":"`+resumed+`","shipping":true,"interactive":false,"cwd":"/tmp"}`,
			ShippingPermissionMode, PermissionModeSourceShipping, true),
	)

	// ⚠️ The mode has exactly one source. A stray `permission_mode` argument must not
	// decode at all, or `bypassPermissions` becomes reachable through this tool.
	ginkgo.It("decodes no permission_mode argument", func() {
		reply := spawn(`{"shipping":false,"permission_mode":"bypassPermissions","interactive":false,"cwd":"/tmp"}`)
		gomega.Expect(reply["permission_mode"]).To(gomega.Equal(DefaultPermissionMode))
	})

	ginkgo.It("keeps the tab refusal for shipping", func() {
		_, err := server.spawnAgent(ctx,
			json.RawMessage(`{"shipping":true,"interactive":true,"cwd":"/tmp","prompt":"hi"}`), "")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("needs the server to set the worker"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})
})
