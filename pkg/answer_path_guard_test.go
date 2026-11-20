// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	stderrors "errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// answerPathTestScript is the probe path the fixtures wire. It matches no real
// file: the runner is a fake, and a real path would invite a spec that silently
// depends on the operator's marketplace clone.
const answerPathTestScript = "/scripts/answer-path-check.py"

// withAnswerPath wires the answer-path guard to a probe that PASSES, so a spec
// about some other headless guard is not refused by this one.
func withAnswerPath(server *RosterToolServer) *RosterToolServer {
	server.Scripts = &fakeScriptRunner{results: map[string]ScriptResult{
		answerPathTestScript: {ExitCode: 0},
	}}
	server.AnswerPathScript = answerPathTestScript
	return server
}

// lapsedProbe is the probe's own refusal line, verbatim from a live reading on
// 2026-11-20 — the state word is what the refusal must name.
const lapsedProbe = "REFUSED\tthis session's manager loop on goal 'Managers Unblock " +
	"Their Own Workers' reads LAPSED, not OK — nothing is armed to answer a " +
	"headless worker's parked prompt\n"

var _ = ginkgo.Describe("RosterToolServer answer-path guard on the live spawn path", func() {
	var (
		ctx        context.Context
		supervisor *toolSupervisor
		scripts    *fakeScriptRunner
		server     *RosterToolServer
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		supervisor = &toolSupervisor{live: make(chan Event, 64)}
		scripts = &fakeScriptRunner{results: map[string]ScriptResult{
			answerPathTestScript: {ExitCode: 0},
		}}
		server = &RosterToolServer{
			Supervisor:       supervisor,
			Spawner:          &fakeSpawner{},
			Scripts:          scripts,
			AnswerPathScript: answerPathTestScript,
		}
	})

	// SC1 — a session whose probe reads non-OK is refused, and the refusal names
	// the condition. Before the guard existed this same call returned a session id.
	ginkgo.It("refuses a headless spawn when the probe reads LAPSED", func() {
		scripts.results[answerPathTestScript] = ScriptResult{ExitCode: 1, Stdout: lapsedProbe}

		_, err := server.spawnAgent(ctx,
			json.RawMessage(`{"interactive":false,"cwd":"/tmp","prompt":"go"}`),
			"lapsed-session")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("LAPSED"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("no answer path"))
		// Nothing was spawned: the refusal precedes every side effect.
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	// SC2 — the guard is not a blanket refusal. The same session, probe OK, spawns.
	ginkgo.It("spawns the same call once the probe reads OK", func() {
		reply, err := server.spawnAgent(ctx,
			json.RawMessage(`{"interactive":false,"cwd":"/tmp","prompt":"go"}`),
			"armed-session")

		gomega.Expect(err).To(gomega.BeNil())
		var decoded map[string]string
		gomega.Expect(json.Unmarshal([]byte(reply), &decoded)).To(gomega.Succeed())
		gomega.Expect(decoded["session_id"]).To(gomega.Equal("s1"))
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
	})

	// SC3 — it calls the probe, with the caller's session id, never a
	// re-implementation of the verdict.
	ginkgo.It("invokes the configured probe with the caller's session id", func() {
		_, err := server.spawnAgent(ctx,
			json.RawMessage(`{"interactive":false,"cwd":"/tmp","prompt":"go"}`),
			"armed-session")
		gomega.Expect(err).To(gomega.BeNil())

		gomega.Expect(scripts.calls).To(gomega.ContainElement(
			[]string{openPython, answerPathTestScript, "--session-id", "armed-session"},
		))
	})

	// Every branch refuses: an unwired probe must not open an unguarded worker.
	ginkgo.DescribeTable("refuses rather than opening an unguarded worker",
		func(sessionID string, mutate func(*RosterToolServer)) {
			mutate(server)
			_, err := server.spawnAgent(ctx,
				json.RawMessage(`{"interactive":false,"cwd":"/tmp","prompt":"go"}`),
				sessionID)
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
		},
		ginkgo.Entry("probe not wired", "armed-session",
			func(s *RosterToolServer) { s.Scripts = nil }),
		ginkgo.Entry("probe path empty", "armed-session",
			func(s *RosterToolServer) { s.AnswerPathScript = "" }),
		ginkgo.Entry("no caller session id", "",
			func(s *RosterToolServer) {}),
		ginkgo.Entry("probe cannot run", "armed-session",
			func(s *RosterToolServer) {
				s.Scripts = &fakeScriptRunner{
					results: map[string]ScriptResult{answerPathTestScript: {}},
					errs:    map[string]error{answerPathTestScript: stderrors.New("boom")},
				}
			}),
	)
})
