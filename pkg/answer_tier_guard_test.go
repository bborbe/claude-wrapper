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

// permissionTierTestScript is the probe path the fixtures wire. It matches no
// real file: the runner is a fake, and a real path would invite a spec that
// silently depends on the operator's marketplace clone.
const permissionTierTestScript = "/scripts/permission-tier.py"

func operatorTierProbe() ScriptResult {
	return ScriptResult{ExitCode: 0, Stdout: `{"tier":"operator","form":"cluster-unknown-env","reason":"operator-only form: cluster-unknown-env"}`}
}

func managerTierProbe() ScriptResult {
	return ScriptResult{ExitCode: 0, Stdout: `{"tier":"manager","form":null,"reason":"manager"}`}
}

var _ = ginkgo.Describe("RosterToolServer permission-tier guard on the answer path", func() {
	var (
		ctx        context.Context
		scripts    *fakeScriptRunner
		supervisor *toolSupervisor
		server     *RosterToolServer
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		scripts = &fakeScriptRunner{results: map[string]ScriptResult{
			permissionTierTestScript: managerTierProbe(),
		}}
		supervisor = &toolSupervisor{live: make(chan Event, 64)}
		server = &RosterToolServer{
			Supervisor:           supervisor,
			Scripts:              scripts,
			PermissionTierScript: permissionTierTestScript,
		}
	})

	ginkgo.Describe("permissionTierRefusal", func() {
		// SC3 — an operator-tier park is refused, and the refusal names the reason.
		ginkgo.It("refuses an operator-tier park and names the reason", func() {
			scripts.results[permissionTierTestScript] = operatorTierProbe()
			refusal := server.permissionTierRefusal(ctx, &Question{
				ToolName: "Bash",
				Input:    map[string]any{"command": "kubectl apply -f x.yaml"},
			})
			gomega.Expect(refusal).To(gomega.ContainSubstring("operator-tier"))
			gomega.Expect(refusal).To(gomega.ContainSubstring("cluster-unknown-env"))
		})

		// The guard is not a blanket refusal: a manager-tier park is answerable.
		ginkgo.It("allows a manager-tier park", func() {
			refusal := server.permissionTierRefusal(ctx, &Question{
				ToolName: "Bash",
				Input:    map[string]any{"command": "ls -la"},
			})
			gomega.Expect(refusal).To(gomega.BeEmpty())
		})

		ginkgo.It("allows an allowlist-tier park", func() {
			scripts.results[permissionTierTestScript] = ScriptResult{ExitCode: 0, Stdout: `{"tier":"allowlist"}`}
			refusal := server.permissionTierRefusal(ctx, &Question{
				ToolName: "Read",
				Input:    map[string]any{"file_path": "/x"},
			})
			gomega.Expect(refusal).To(gomega.BeEmpty())
		})

		ginkgo.It("has no refusal when there is no park", func() {
			gomega.Expect(server.permissionTierRefusal(ctx, nil)).To(gomega.BeEmpty())
		})

		// It calls the probe with the park's own input on stdin, never argv.
		ginkgo.It("passes a Bash command on stdin, never argv", func() {
			_ = server.permissionTierRefusal(ctx, &Question{
				ToolName: "Bash",
				Input:    map[string]any{"command": "ls -la"},
			})
			gomega.Expect(scripts.calls).To(gomega.ContainElement(
				[]string{openPython, permissionTierTestScript, "--tool", "Bash", "--input", "-"},
			))
			gomega.Expect(scripts.stdin).To(gomega.ContainElement("ls -la"))
		})

		ginkgo.It("passes a non-Bash input as JSON on stdin, never argv", func() {
			_ = server.permissionTierRefusal(ctx, &Question{
				ToolName: "Write",
				Input:    map[string]any{"file_path": "/x"},
			})
			gomega.Expect(scripts.calls).To(gomega.ContainElement(
				[]string{openPython, permissionTierTestScript, "--tool", "Write", "--input", "-"},
			))
			gomega.Expect(scripts.stdin).To(gomega.ContainElement(`{"file_path":"/x"}`))
		})

		// Every branch refuses: a guard that failed open would answer the very gate
		// it exists to protect.
		ginkgo.DescribeTable("refuses rather than allowing an unclassifiable park",
			func(mutate func(*RosterToolServer, *fakeScriptRunner), question *Question) {
				mutate(server, scripts)
				gomega.Expect(server.permissionTierRefusal(ctx, question)).NotTo(gomega.BeEmpty())
			},
			ginkgo.Entry("probe not wired", func(s *RosterToolServer, _ *fakeScriptRunner) { s.Scripts = nil },
				&Question{ToolName: "Bash", Input: map[string]any{"command": "ls"}}),
			ginkgo.Entry("probe path empty", func(s *RosterToolServer, _ *fakeScriptRunner) { s.PermissionTierScript = "" },
				&Question{ToolName: "Bash", Input: map[string]any{"command": "ls"}}),
			ginkgo.Entry("prompt names no tool", func(_ *RosterToolServer, _ *fakeScriptRunner) {},
				&Question{ToolName: "", Input: map[string]any{}}),
			ginkgo.Entry("Bash prompt has no command", func(_ *RosterToolServer, _ *fakeScriptRunner) {},
				&Question{ToolName: "Bash", Input: map[string]any{}}),
			ginkgo.Entry("probe cannot run", func(_ *RosterToolServer, f *fakeScriptRunner) {
				f.errs = map[string]error{permissionTierTestScript: stderrors.New("boom")}
			}, &Question{ToolName: "Bash", Input: map[string]any{"command": "ls"}}),
			ginkgo.Entry("probe exits non-zero", func(_ *RosterToolServer, f *fakeScriptRunner) {
				f.results[permissionTierTestScript] = ScriptResult{ExitCode: 1, Stdout: "classifier error"}
			}, &Question{ToolName: "Bash", Input: map[string]any{"command": "ls"}}),
			ginkgo.Entry("probe returns garbage", func(_ *RosterToolServer, f *fakeScriptRunner) {
				f.results[permissionTierTestScript] = ScriptResult{ExitCode: 0, Stdout: "not json"}
			}, &Question{ToolName: "Bash", Input: map[string]any{"command": "ls"}}),
		)
	})

	// The call site: answerPermission refuses BEFORE Supervisor.Answer settles the
	// park, so an operator-tier gate stays open on the board.
	ginkgo.Describe("answerPermission", func() {
		ginkgo.It("refuses an operator-tier park before the answer lands", func() {
			scripts.results[permissionTierTestScript] = operatorTierProbe()
			supervisor.question = &Question{
				RequestID: "r1",
				ToolName:  "Bash",
				Input:     map[string]any{"command": "kubectl apply -f x.yaml"},
			}
			_, err := server.answerPermission(ctx,
				json.RawMessage(`{"agent_id":"a1","request_id":"r1","behavior":"allow"}`), "")
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("operator-tier"))
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(0))
		})

		ginkgo.It("answers a manager-tier park", func() {
			supervisor.question = &Question{RequestID: "r1", ToolName: "Bash", Input: map[string]any{"command": "ls -la"}}
			_, err := server.answerPermission(ctx,
				json.RawMessage(`{"agent_id":"a1","request_id":"r1","behavior":"allow"}`), "")
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(1))
		})

		// The bypass the reviewer named: an agent_id whose Question is manager-tier
		// paired with an operator-tier park's request_id must refuse, or the classified
		// park is not the answered one.
		ginkgo.It("refuses when the request_id does not match the parked prompt", func() {
			supervisor.question = &Question{
				RequestID: "other",
				ToolName:  "Bash",
				Input:     map[string]any{"command": "ls -la"},
			}
			_, err := server.answerPermission(ctx,
				json.RawMessage(`{"agent_id":"a1","request_id":"r1","behavior":"allow"}`), "")
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("does not match the parked prompt"))
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(0))
		})
	})

	// SC1 — `answer_pending` was the second unguarded surface: a manager that
	// reached the park through it settled an operator-tier gate with no tier check.
	// The same park, answered on the same route, is refused.
	ginkgo.Describe("answerPending", func() {
		var mcp *MCPToolServer

		ginkgo.BeforeEach(func() {
			mcp = &MCPToolServer{
				Supervisor: supervisor,
				TierGuard: PermissionTierGuard{
					Scripts:              scripts,
					PermissionTierScript: permissionTierTestScript,
				},
			}
		})

		ginkgo.It("refuses an operator-tier park before the answer lands", func() {
			scripts.results[permissionTierTestScript] = operatorTierProbe()
			supervisor.question = &Question{
				RequestID: "r1",
				Kind:      AnswerKindPermission,
				ToolName:  "Bash",
				Input:     map[string]any{"command": "kubectl apply -f x.yaml"},
			}

			_, err := mcp.answerPending(ctx,
				json.RawMessage(`{"session_id":"s1","request_id":"r1","behavior":"allow"}`))

			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("answer_pending refused"))
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("operator-tier"))
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(0))
		})

		// The positive half: a guard that refused every answer would satisfy the
		// refusal assertion above and break the product.
		ginkgo.It("answers a manager-tier park", func() {
			supervisor.question = &Question{
				RequestID: "r1",
				Kind:      AnswerKindPermission,
				ToolName:  "Bash",
				Input:     map[string]any{"command": "ls -la"},
			}

			_, err := mcp.answerPending(ctx,
				json.RawMessage(`{"session_id":"s1","request_id":"r1","behavior":"allow"}`))

			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(1))
		})

		ginkgo.It("refuses when the request_id does not match the parked prompt", func() {
			supervisor.question = &Question{
				RequestID: "other",
				Kind:      AnswerKindPermission,
				ToolName:  "Bash",
				Input:     map[string]any{"command": "ls -la"},
			}

			_, err := mcp.answerPending(ctx,
				json.RawMessage(`{"session_id":"s1","request_id":"r1","behavior":"allow"}`))

			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("does not match the parked prompt"))
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(0))
		})

		// An unwired guard refuses: the daemon cannot prove an operator-tier gate is
		// safe to answer, so it must not answer one.
		ginkgo.It("refuses an operator-tier park when the guard is unwired", func() {
			scripts.results[permissionTierTestScript] = operatorTierProbe()
			supervisor.question = &Question{
				RequestID: "r1",
				Kind:      AnswerKindPermission,
				ToolName:  "Bash",
				Input:     map[string]any{"command": "kubectl apply -f x.yaml"},
			}
			mcp.TierGuard = PermissionTierGuard{}

			_, err := mcp.answerPending(ctx,
				json.RawMessage(`{"session_id":"s1","request_id":"r1","behavior":"allow"}`))

			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(0))
		})

		// A question park is not a permission gate: gating it here would refuse a
		// selection no arm has ever been tier-checked for.
		ginkgo.It("answers an AskUserQuestion park", func() {
			supervisor.question = &Question{
				RequestID: "r1",
				Kind:      AnswerKindQuestion,
				ToolName:  "AskUserQuestion",
			}

			_, err := mcp.answerPending(ctx,
				json.RawMessage(`{"session_id":"s1","request_id":"r1","answers":{"Pick one":"a"}}`))

			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(supervisor.answerCalls).To(gomega.Equal(1))
		})
	})
})
