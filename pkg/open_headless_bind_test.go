// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// These specs pin the OPEN ENDPOINT's task bind — the half that was missing.
//
// ⚠️ They drive SpawnOpenHeadless itself, never bindSpawnedTask: the defect was
// that the open path never reached the bind at all, and a spec on the helper
// would have passed while the bug shipped. bindSpawnedTask is exercised through
// spawnAgent in spawn_bind_test.go; what was missing here is the CALL SITE.
//
// Measured 2026-11-22: five workers opened through this path on v0.63.1 got no
// claude_session_id. The bind was wired to one of the two call sites of
// startHeadless (pkg/roster_mcp.go:432, the MCP spawn_agent reply); the open
// endpoint's headless path is the other and had no bind.
//
// The ownership branch is the load-bearing case, as in spawn_bind_test.go: an
// unconditional overwrite would pass a stamp-only spec while stealing a task
// another live session owns.
var _ = ginkgo.Describe("RosterToolServer.SpawnOpenHeadless binds the opened session to its task", func() {
	const foreignOwner = "11111111-2222-4333-8444-555555555555"

	var (
		ctx    context.Context
		dir    string
		server *RosterToolServer
		runner *bindRunner
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "open-bind-*")
		gomega.Expect(err).To(gomega.BeNil())
		launcher := filepath.Join(dir, "cc-test")
		gomega.Expect(os.WriteFile(launcher, []byte("claude \"$@\"\n"), 0o600)).To(gomega.Succeed())
		runner = &bindRunner{}
		// Live channel required: startHeadless sends the opening turn and the fake
		// supervisor emits on `live` — a nil one blocks forever.
		server = withAnswerPath(&RosterToolServer{
			Supervisor: &toolSupervisor{live: make(chan Event, 64)},
			Spawner:    &fakeSpawner{},
			Vaults: &fakeVaults{vaults: []Vault{{
				Name: "test-vault", Path: dir, WorkerLauncher: launcher,
			}}},
			Runner: runner,
		})
	})

	ginkgo.AfterEach(func() { _ = os.RemoveAll(dir) })

	open := func(task string) *OpenBind {
		_, bind, err := server.SpawnOpenHeadless(ctx, OpenHeadlessRequest{
			Vault: "test-vault",
			Task:  task,
		})
		gomega.Expect(err).To(gomega.BeNil())
		return bind
	}

	ginkgo.It("stamps an unowned task and appends the metrics session", func() {
		runner.owner = ""
		bind := open("Some Task")

		// The bind is the endpoint's OWN record — the evidence a task file cannot
		// supply, because a spawned child stamps itself through work-on-task.
		gomega.Expect(bind).NotTo(gomega.BeNil(), "an open that names a task must report its bind")
		gomega.Expect(bind.Action).To(gomega.Equal("stamped"))
		gomega.Expect(bind.Task).To(gomega.Equal("Some Task"))
		gomega.Expect(bind.SessionID).To(gomega.Equal("s1"))
		gomega.Expect(bind.Error).To(gomega.BeEmpty())

		// Exact ORDERED calls: read, then stamp, then append — the same contract
		// spawn_agent honours. Pre-fix this list is EMPTY, which is the bug.
		gomega.Expect(runner.calls).To(gomega.Equal([][]string{
			{"vault-cli", "--vault", "test-vault", "task", "get", "Some Task", "claude_session_id"},
			{"vault-cli", "--vault", "test-vault", "task", "set", "Some Task", "claude_session_id", "s1"},
			{"vault-cli", "--vault", "test-vault", "task", "append-metrics-session", "Some Task", "s1"},
		}))
	})

	ginkgo.It("leaves another session's owner intact and appends instead", func() {
		runner.owner = foreignOwner
		bind := open("Some Task")

		gomega.Expect(bind).NotTo(gomega.BeNil())
		gomega.Expect(bind.Action).To(gomega.Equal("appended"))

		// No `set` at all: the owner is never overwritten, and the new session
		// lands in metrics_sessions only.
		gomega.Expect(runner.calls).To(gomega.Equal([][]string{
			{"vault-cli", "--vault", "test-vault", "task", "get", "Some Task", "claude_session_id"},
			{"vault-cli", "--vault", "test-vault", "task", "append-metrics-session", "Some Task", "s1"},
		}))
		for _, call := range runner.calls {
			gomega.Expect(call).NotTo(gomega.ContainElement(foreignOwner))
		}
	})

	ginkgo.It("reports a failed bind without failing the open", func() {
		runner.owner = ""
		runner.errOn = map[string]error{"get": errors.New("vault-cli exploded")}

		_, bind, err := server.SpawnOpenHeadless(ctx, OpenHeadlessRequest{
			Vault: "test-vault",
			Task:  "Some Task",
		})
		// The child is already live, so a bind failure must not read as a failed open.
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(bind).NotTo(gomega.BeNil())
		gomega.Expect(bind.Action).To(gomega.Equal("failed"))
		gomega.Expect(bind.Error).To(gomega.ContainSubstring("vault-cli exploded"))
		gomega.Expect(runner.countSub("append-metrics-session")).To(gomega.Equal(0))
	})
})
