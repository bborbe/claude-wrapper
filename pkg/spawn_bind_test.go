// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// These specs pin the headless spawn's TASK BIND — the half that writes the
// spawned session id onto the task the caller named.
//
// ⚠️ The load-bearing case is the ownership branch (SC2): spawning onto a task
// another session already owns must leave that owner intact and record the new
// session in metrics_sessions instead. A test that only covered the unowned row
// would pass on an unconditional overwrite, which is exactly the defect this
// branch exists to prevent.
var _ = ginkgo.Describe("RosterToolServer.spawnAgent binds the spawned session to its task", func() {
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
		dir, err = os.MkdirTemp("", "spawn-bind-*")
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

	spawn := func(args string) map[string]any {
		reply, err := server.spawnAgent(ctx, json.RawMessage(args), "test-session")
		gomega.Expect(err).To(gomega.BeNil())
		var decoded map[string]any
		gomega.Expect(json.Unmarshal([]byte(reply), &decoded)).To(gomega.Succeed())
		return decoded
	}

	bindOf := func(reply map[string]any) map[string]any {
		raw, ok := reply["bind"]
		gomega.Expect(ok).To(gomega.BeTrue(), "reply carries no bind key")
		bind, ok := raw.(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue(), "bind is not an object")
		return bind
	}

	ginkgo.It("stamps an unowned task and appends the metrics session", func() {
		runner.owner = ""
		reply := spawn(`{"interactive":false,"vault":"test-vault","task":"Some Task"}`)

		bind := bindOf(reply)
		gomega.Expect(bind["action"]).To(gomega.Equal("stamped"))
		gomega.Expect(bind["task"]).To(gomega.Equal("Some Task"))
		gomega.Expect(bind["session_id"]).To(gomega.Equal("s1"))

		// The argv IS the contract: the stamp and the append both go through
		// `vault-cli --vault <vault> task …`, never a direct frontmatter write.
		gomega.Expect(runner.calls).To(gomega.ContainElement([]string{
			"vault-cli", "--vault", "test-vault", "task", "set",
			"Some Task", "claude_session_id", "s1",
		}))
		gomega.Expect(runner.calls).To(gomega.ContainElement([]string{
			"vault-cli", "--vault", "test-vault", "task", "append-metrics-session",
			"Some Task", "s1",
		}))
	})

	ginkgo.It("leaves another session's owner intact and appends instead", func() {
		runner.owner = foreignOwner
		reply := spawn(`{"interactive":false,"vault":"test-vault","task":"Some Task"}`)

		bind := bindOf(reply)
		gomega.Expect(bind["action"]).To(gomega.Equal("appended"))
		gomega.Expect(bind["session_id"]).To(gomega.Equal("s1"))

		// The owner must NOT be overwritten — no `set` call at all — and the new
		// session must land in metrics_sessions via the append.
		gomega.Expect(runner.countSub("set")).To(gomega.Equal(0))
		gomega.Expect(runner.countSub("append-metrics-session")).To(gomega.Equal(1))
	})

	ginkgo.It("writes nothing and emits no bind key when no task is named", func() {
		reply := spawn(`{"interactive":false,"vault":"test-vault"}`)

		_, ok := reply["bind"]
		gomega.Expect(ok).To(gomega.BeFalse(), "a task-less spawn must carry no bind key")
		gomega.Expect(runner.calls).To(gomega.BeEmpty(), "a task-less spawn must write nothing")
	})
})

// bindRunner is a CommandRunner over a fixed current owner. It answers the
// `task get … claude_session_id` read with `owner` and records every argv so a
// spec can assert exactly which vault-cli calls were made.
type bindRunner struct {
	owner string
	calls [][]string
}

func (r *bindRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	// argv layout: [vault-cli, --vault, <vault>, task, <sub>, ...]
	if len(call) >= 5 && call[3] == "task" && call[4] == "get" {
		return []byte(r.owner + "\n"), nil
	}
	return nil, nil
}

// countSub counts calls whose task subcommand is `sub`.
func (r *bindRunner) countSub(sub string) int {
	n := 0
	for _, call := range r.calls {
		if len(call) >= 5 && call[3] == "task" && call[4] == sub {
			n++
		}
	}
	return n
}
