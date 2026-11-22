// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// openTestUUID is a full 36-character session id, so a case that wants a
// well-formed recorded id does not have to spell one out.
const openTestUUID = "11111111-1111-1111-1111-111111111111"

// taskFacts builds a found task page with the given frontmatter scalars.
func taskFacts(fields map[string]string) OpenRowFacts {
	return factsFor(OpenKindTask, "T", fields)
}

// factsFor builds a found page of any kind.
func factsFor(kind, name string, fields map[string]string) OpenRowFacts {
	if fields == nil {
		fields = map[string]string{}
	}
	return OpenRowFacts{
		Row: OpenRow{Kind: kind, Vault: "v", Name: name},
		Page: OpenPage{
			Found:  true,
			Path:   "/v/" + name + ".md",
			Fields: fields,
		},
	}
}

var _ = ginkgo.Describe("DecideOpen", func() {
	ginkgo.Context("role arms", func() {
		ginkgo.It("defaults an absent role to agent and writes it back", func() {
			decision := DecideOpen(taskFacts(map[string]string{}))
			gomega.Expect(decision.Verdict.Role).To(gomega.Equal(RoleAgent))
			gomega.Expect(decision.Verdict.RoleSource).To(gomega.Equal(OpenRoleSourceDefaulted))
			gomega.Expect(decision.WriteRole).To(gomega.Equal(RoleAgent))
		})

		ginkgo.It("defaults a blank role to agent and writes it back", func() {
			decision := DecideOpen(taskFacts(map[string]string{"role": "  "}))
			gomega.Expect(decision.Verdict.Role).To(gomega.Equal(RoleAgent))
			gomega.Expect(decision.Verdict.RoleSource).To(gomega.Equal(OpenRoleSourceDefaulted))
			gomega.Expect(decision.WriteRole).To(gomega.Equal(RoleAgent))
		})

		ginkgo.It("refuses an unrecognised role and names the value", func() {
			decision := DecideOpen(taskFacts(map[string]string{"role": "robot"}))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchRefused))
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonUndecidableRole))
			gomega.Expect(decision.Verdict.Message).To(gomega.ContainSubstring("robot"))
		})

		ginkgo.It("reports a task manager as the operator's own and opens nothing", func() {
			decision := DecideOpen(taskFacts(map[string]string{"role": "manager"}))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchYours))
			gomega.Expect(decision.Verdict.Path).To(gomega.Equal(OpenPathNone))
			gomega.Expect(decision.WriteRole).To(gomega.BeEmpty())
			gomega.Expect(decision.WriteMode).To(gomega.BeEmpty())
		})

		ginkgo.It("makes a goal a kind-sourced manager", func() {
			decision := DecideOpen(factsFor(OpenKindGoal, "G", map[string]string{}))
			gomega.Expect(decision.Verdict.Role).To(gomega.Equal(RoleManager))
			gomega.Expect(decision.Verdict.RoleSource).To(gomega.Equal(OpenRoleSourceKind))
		})

		ginkgo.It("makes a topic a kind-sourced manager", func() {
			decision := DecideOpen(factsFor(OpenKindTopic, "G", map[string]string{}))
			gomega.Expect(decision.Verdict.Role).To(gomega.Equal(RoleManager))
			gomega.Expect(decision.Verdict.RoleSource).To(gomega.Equal(OpenRoleSourceKind))
		})

		ginkgo.It("refuses a goal declaring mode headless", func() {
			decision := DecideOpen(factsFor(OpenKindGoal, "G", map[string]string{"mode": "headless"}))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchRefused))
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonManagerModeHeadless))
			gomega.Expect(decision.Verdict.Message).To(gomega.ContainSubstring("Rule 2"))
		})
	})

	ginkgo.Context("mode arms", func() {
		ginkgo.It("normalises an agent's stored interactive to headless and writes it", func() {
			decision := DecideOpen(taskFacts(map[string]string{"mode": "interactive"}))
			gomega.Expect(decision.Verdict.Mode).To(gomega.Equal(SpawnModeHeadless))
			gomega.Expect(decision.Verdict.ModeNote).To(gomega.Equal(OpenModeNoteNormalised))
			gomega.Expect(decision.WriteMode).To(gomega.Equal(SpawnModeHeadless))
		})

		ginkgo.It("does not write an agent already stored headless", func() {
			decision := DecideOpen(taskFacts(map[string]string{"mode": "headless"}))
			gomega.Expect(decision.Verdict.Mode).To(gomega.Equal(SpawnModeHeadless))
			gomega.Expect(decision.Verdict.ModeNote).To(gomega.BeEmpty())
			gomega.Expect(decision.WriteMode).To(gomega.BeEmpty())
		})

		ginkgo.It("writes headless for an agent with no stored mode", func() {
			decision := DecideOpen(taskFacts(map[string]string{}))
			gomega.Expect(decision.Verdict.Mode).To(gomega.Equal(SpawnModeHeadless))
			gomega.Expect(decision.Verdict.ModeNote).To(gomega.BeEmpty())
			gomega.Expect(decision.WriteMode).To(gomega.Equal(SpawnModeHeadless))
		})

		ginkgo.It("leaves a human interactive and writes it when absent", func() {
			decision := DecideOpen(taskFacts(map[string]string{"role": "human"}))
			gomega.Expect(decision.Verdict.Mode).To(gomega.Equal(SpawnModeInteractive))
			gomega.Expect(decision.WriteMode).To(gomega.Equal(SpawnModeInteractive))
		})

		ginkgo.It("leaves a human interactive and does not write when headless is stored", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"role": "human", "mode": "headless",
			}))
			gomega.Expect(decision.Verdict.Mode).To(gomega.Equal(SpawnModeInteractive))
			gomega.Expect(decision.WriteMode).To(gomega.BeEmpty())
		})
	})

	ginkgo.Context("row, session and unported arms", func() {
		ginkgo.It("refuses a row with no page", func() {
			facts := taskFacts(map[string]string{})
			facts.Page.Found = false
			decision := DecideOpen(facts)
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchRefused))
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonMissingRow))
		})

		ginkgo.It("refuses an unreadable page and carries the error text", func() {
			facts := taskFacts(map[string]string{})
			facts.Page.ReadErr = stderrors.New("boom")
			decision := DecideOpen(facts)
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonRowUnreadable))
			gomega.Expect(decision.Verdict.Message).To(gomega.ContainSubstring("boom"))
		})

		ginkgo.It("refuses a truncated recorded id", func() {
			decision := DecideOpen(taskFacts(map[string]string{"claude_session_id": "1234abcd"}))
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonTruncatedID))
		})

		ginkgo.It("refuses an unreadable session registry", func() {
			facts := taskFacts(map[string]string{})
			facts.LiveErr = stderrors.New("registry unavailable")
			decision := DecideOpen(facts)
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonLivenessUnreadable))
		})

		ginkgo.It("refuses a goal_loop task", func() {
			decision := DecideOpen(taskFacts(map[string]string{"goal_loop": "true"}))
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonGoalLoopUnsupported))
		})

		ginkgo.It("refuses a non-local target", func() {
			decision := DecideOpen(taskFacts(map[string]string{"target": "docker"}))
			gomega.Expect(decision.Verdict.Reason).To(gomega.Equal(OpenReasonTargetUnsupported))
			gomega.Expect(decision.Verdict.Message).To(gomega.ContainSubstring("docker"))
		})

		ginkgo.It("does not refuse a local target", func() {
			decision := DecideOpen(taskFacts(map[string]string{"target": "local"}))
			gomega.Expect(decision.Verdict.Branch).NotTo(gomega.Equal(OpenBranchRefused))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchCreate))
		})

		ginkgo.It("clears the write hints on a refusal after a defaulted role", func() {
			decision := DecideOpen(taskFacts(map[string]string{"goal_loop": "true"}))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchRefused))
			gomega.Expect(decision.WriteRole).To(gomega.BeEmpty())
			gomega.Expect(decision.WriteMode).To(gomega.BeEmpty())
		})

		ginkgo.It("reports shipping true", func() {
			decision := DecideOpen(taskFacts(map[string]string{"shipping": "true"}))
			gomega.Expect(decision.Shipping).To(gomega.BeTrue())
		})
	})

	ginkgo.Context("branch selection", func() {
		ginkgo.It("jumps a live human row with a pane", func() {
			facts := taskFacts(map[string]string{
				"role": "human", "claude_session_id": openTestUUID,
			})
			facts.Live = []OpenLiveEntry{{PID: 7, SessionID: openTestUUID}}
			facts.PaneFor = func(int) string { return "p7" }
			decision := DecideOpen(facts)
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchJump))
			gomega.Expect(decision.Verdict.PaneID).To(gomega.Equal("p7"))
			gomega.Expect(decision.Verdict.SessionID).To(gomega.Equal(openTestUUID))
		})

		ginkgo.It("marks a live human row with no pane live", func() {
			facts := taskFacts(map[string]string{
				"role": "human", "claude_session_id": openTestUUID,
			})
			facts.Live = []OpenLiveEntry{{PID: 7, SessionID: openTestUUID}}
			facts.PaneFor = func(int) string { return "" }
			decision := DecideOpen(facts)
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchLive))
		})

		ginkgo.It("marks a live agent row live without consulting the pane lookup", func() {
			calls := 0
			facts := taskFacts(map[string]string{"claude_session_id": openTestUUID})
			facts.Live = []OpenLiveEntry{{PID: 7, SessionID: openTestUUID}}
			facts.PaneFor = func(int) string {
				calls++
				return "p7"
			}
			decision := DecideOpen(facts)
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchLive))
			gomega.Expect(calls).To(gomega.Equal(0))
		})

		ginkgo.It("resumes a recorded agent id that is not live", func() {
			decision := DecideOpen(taskFacts(map[string]string{"claude_session_id": openTestUUID}))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchResume))
			gomega.Expect(decision.Resume).To(gomega.Equal(openTestUUID))
			gomega.Expect(decision.Verdict.Path).To(gomega.Equal(OpenPathHeadless))
		})

		ginkgo.It("resumes a recorded human id as a tab", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"role": "human", "claude_session_id": openTestUUID,
			}))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchResume))
			gomega.Expect(decision.Verdict.Path).To(gomega.Equal(OpenPathTab))
		})

		ginkgo.It("creates when nothing is recorded", func() {
			decision := DecideOpen(taskFacts(map[string]string{}))
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchCreate))
		})

		ginkgo.It("jumps a goal to a live manager named case-insensitively", func() {
			facts := factsFor(OpenKindGoal, "G", map[string]string{})
			facts.Live = []OpenLiveEntry{{PID: 9, SessionID: openTestUUID, Name: "g manager"}}
			facts.PaneFor = func(int) string { return "p9" }
			decision := DecideOpen(facts)
			gomega.Expect(decision.Verdict.Branch).To(gomega.Equal(OpenBranchJump))
			gomega.Expect(decision.Verdict.SessionID).To(gomega.Equal(openTestUUID))
			gomega.Expect(decision.Verdict.PaneID).To(gomega.Equal("p9"))
		})
	})

	ginkgo.Context("operator flag", func() {
		ginkgo.It("accepts flag true set by operator", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"flag": "true", "flag_set_by": "operator",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeTrue())
		})

		ginkgo.It("accepts flag true set by legacy", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"flag": "true", "flag_set_by": "legacy",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeTrue())
		})

		ginkgo.It("accepts flag yes set by operator", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"flag": "yes", "flag_set_by": "operator",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeTrue())
		})

		ginkgo.It("accepts flag On set by operator", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"flag": "On", "flag_set_by": "operator",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeTrue())
		})

		ginkgo.It("accepts flag Y set by operator", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"flag": "Y", "flag_set_by": "operator",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeTrue())
		})

		ginkgo.It("rejects flag 1, which is not in the strict set", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"flag": "1", "flag_set_by": "operator",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeFalse())
		})

		ginkgo.It("rejects a flag set by an agent", func() {
			decision := DecideOpen(taskFacts(map[string]string{
				"flag": "true", "flag_set_by": "agent",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeFalse())
		})

		ginkgo.It("rejects a flag with no set_by", func() {
			decision := DecideOpen(taskFacts(map[string]string{"flag": "true"}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeFalse())
		})

		ginkgo.It("reads the flag on a goal too", func() {
			decision := DecideOpen(factsFor(OpenKindGoal, "G", map[string]string{
				"flag": "true", "flag_set_by": "operator",
			}))
			gomega.Expect(decision.OperatorNamed).To(gomega.BeTrue())
		})
	})
})

var _ = ginkgo.Describe("DecideOpen openPathFor", func() {
	ginkgo.DescribeTable("routes by role and branch",
		func(role, branch, want string) {
			gomega.Expect(openPathFor(role, branch)).To(gomega.Equal(want))
		},
		ginkgo.Entry("agent resume is headless", RoleAgent, OpenBranchResume, OpenPathHeadless),
		ginkgo.Entry("agent create is headless", RoleAgent, OpenBranchCreate, OpenPathHeadless),
		ginkgo.Entry("human resume is a tab", RoleHuman, OpenBranchResume, OpenPathTab),
		ginkgo.Entry("human create is a tab", RoleHuman, OpenBranchCreate, OpenPathTab),
		ginkgo.Entry("manager resume is a tab", RoleManager, OpenBranchResume, OpenPathTab),
		ginkgo.Entry("manager create is a tab", RoleManager, OpenBranchCreate, OpenPathTab),
		ginkgo.Entry("agent jump is none", RoleAgent, OpenBranchJump, OpenPathNone),
		ginkgo.Entry("agent live is none", RoleAgent, OpenBranchLive, OpenPathNone),
		ginkgo.Entry("agent yours is none", RoleAgent, OpenBranchYours, OpenPathNone),
		ginkgo.Entry("agent refused is none", RoleAgent, OpenBranchRefused, OpenPathNone),
		ginkgo.Entry("human jump is none", RoleHuman, OpenBranchJump, OpenPathNone),
		ginkgo.Entry("human live is none", RoleHuman, OpenBranchLive, OpenPathNone),
		ginkgo.Entry("human yours is none", RoleHuman, OpenBranchYours, OpenPathNone),
		ginkgo.Entry("human refused is none", RoleHuman, OpenBranchRefused, OpenPathNone),
		ginkgo.Entry("manager jump is none", RoleManager, OpenBranchJump, OpenPathNone),
		ginkgo.Entry("manager live is none", RoleManager, OpenBranchLive, OpenPathNone),
		ginkgo.Entry("manager yours is none", RoleManager, OpenBranchYours, OpenPathNone),
		ginkgo.Entry("manager refused is none", RoleManager, OpenBranchRefused, OpenPathNone),
	)
})

var _ = ginkgo.Describe("ResolveOpenWindow", func() {
	ginkgo.Context("role-map-key-missing", func() {
		ginkgo.It("refuses an absent role map, naming the key", func() {
			ctx := context.Background()
			path := filepath.Join(ginkgo.GinkgoT().TempDir(), "absent.json")
			entries, err := NewRoleMapReader(path).Read(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, refusal := ResolveOpenWindow(RoleHuman, "private-personal", entries, err)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonRoleMapKeyMissing))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Direct"))
		})

		ginkgo.It("refuses a map without the Direct key, naming it", func() {
			entries := map[string]RoleMapEntry{
				"Managers": {WindowID: new(14)},
				"Hold":     {WindowID: new(2)},
			}
			_, refusal := ResolveOpenWindow(RoleHuman, "private-personal", entries, nil)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonRoleMapKeyMissing))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Direct"))
		})

		ginkgo.It("refuses a Direct key with a null window_id, naming it", func() {
			entries := map[string]RoleMapEntry{
				"Direct": {Chip: new("cyan")},
			}
			_, refusal := ResolveOpenWindow(RoleHuman, "private-personal", entries, nil)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonRoleMapKeyMissing))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Direct"))
		})

		ginkgo.It("refuses an unparseable role map, naming the key and the error", func() {
			ctx := context.Background()
			path := filepath.Join(ginkgo.GinkgoT().TempDir(), "bad.json")
			gomega.Expect(os.WriteFile(path, []byte("{not json"), 0o600)).To(gomega.Succeed())
			entries, err := NewRoleMapReader(path).Read(ctx)
			gomega.Expect(err).To(gomega.HaveOccurred())
			_, refusal := ResolveOpenWindow(RoleHuman, "private-personal", entries, err)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonRoleMapKeyMissing))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Direct"))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring(err.Error()))
		})

		ginkgo.It("refuses a map without the Managers key, naming it", func() {
			entries := map[string]RoleMapEntry{
				"Direct": {WindowID: new(12)},
				"Hold":   {WindowID: new(2)},
			}
			_, refusal := ResolveOpenWindow(RoleManager, "private-personal", entries, nil)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonRoleMapKeyMissing))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Managers"))
		})
	})

	ginkgo.Context("hold-window", func() {
		ginkgo.It("refuses a Direct window equal to Hold, naming both keys", func() {
			entries := map[string]RoleMapEntry{
				"Direct": {WindowID: new(2)},
				"Hold":   {WindowID: new(2)},
			}
			_, refusal := ResolveOpenWindow(RoleHuman, "private-personal", entries, nil)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonHoldWindow))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Direct"))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Hold"))
		})

		ginkgo.It("refuses a Managers window equal to Hold, naming both keys", func() {
			entries := map[string]RoleMapEntry{
				"Managers": {WindowID: new(2)},
				"Hold":     {WindowID: new(2)},
			}
			_, refusal := ResolveOpenWindow(RoleManager, "private-personal", entries, nil)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonHoldWindow))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Managers"))
			gomega.Expect(refusal.Message).To(gomega.ContainSubstring("Hold"))
		})

		ginkgo.It("resolves a window distinct from Hold", func() {
			entries := map[string]RoleMapEntry{
				"Direct": {WindowID: new(12)},
				"Hold":   {WindowID: new(2)},
			}
			window, refusal := ResolveOpenWindow(RoleHuman, "private-personal", entries, nil)
			gomega.Expect(refusal).To(gomega.BeNil())
			gomega.Expect(window.Key).To(gomega.Equal("Direct"))
			gomega.Expect(window.ID).To(gomega.Equal(12))
		})
	})

	ginkgo.Context("chip", func() {
		ginkgo.It("gives a human the vault family chip even when Direct names another", func() {
			entries := map[string]RoleMapEntry{
				"Direct": {Chip: new("pink"), WindowID: new(12)},
				"Hold":   {WindowID: new(2)},
			}
			window, refusal := ResolveOpenWindow(RoleHuman, "private-personal", entries, nil)
			gomega.Expect(refusal).To(gomega.BeNil())
			gomega.Expect(window.Chip).To(gomega.Equal("cyan"))
		})

		ginkgo.It("gives a manager orange from the Managers chip", func() {
			entries := map[string]RoleMapEntry{
				"Managers": {Chip: new("orange"), WindowID: new(14)},
			}
			window, refusal := ResolveOpenWindow(RoleManager, "private-personal", entries, nil)
			gomega.Expect(refusal).To(gomega.BeNil())
			gomega.Expect(window.Chip).To(gomega.Equal("orange"))
		})

		ginkgo.It("gives a manager orange even when its entry chip is pink", func() {
			entries := map[string]RoleMapEntry{
				"Managers": {Chip: new("pink"), WindowID: new(14)},
			}
			window, refusal := ResolveOpenWindow(RoleManager, "private-personal", entries, nil)
			gomega.Expect(refusal).To(gomega.BeNil())
			gomega.Expect(window.Chip).To(gomega.Equal("orange"))
		})

		ginkgo.It("gives a manager orange when its entry has no chip", func() {
			entries := map[string]RoleMapEntry{
				"Managers": {WindowID: new(14)},
			}
			window, refusal := ResolveOpenWindow(RoleManager, "private-personal", entries, nil)
			gomega.Expect(refusal).To(gomega.BeNil())
			gomega.Expect(window.Chip).To(gomega.Equal("orange"))
		})

		ginkgo.It("refuses an agent tab", func() {
			_, refusal := ResolveOpenWindow(RoleAgent, "private-personal", nil, nil)
			gomega.Expect(refusal).NotTo(gomega.BeNil())
			gomega.Expect(refusal.Reason).To(gomega.Equal(OpenReasonSpawnRefused))
			gomega.Expect(refusal.Message).To(gomega.Equal(agentTabRefusal))
		})
	})
})

var _ = ginkgo.Describe("open verdict shape", func() {
	ginkgo.It("carries exactly the contract fields and only contract tokens", func() {
		yours := DecideOpen(taskFacts(map[string]string{"role": "manager"})).Verdict

		liveFacts := taskFacts(map[string]string{
			"role": "human", "claude_session_id": openTestUUID,
		})
		liveFacts.Live = []OpenLiveEntry{{PID: 1, SessionID: openTestUUID}}
		live := DecideOpen(liveFacts).Verdict

		resume := DecideOpen(taskFacts(map[string]string{
			"claude_session_id": openTestUUID,
		})).Verdict

		create := DecideOpen(taskFacts(map[string]string{})).Verdict

		missing := taskFacts(map[string]string{})
		missing.Page.Found = false
		refused := DecideOpen(missing).Verdict

		jumpFacts := taskFacts(map[string]string{
			"role": "human", "claude_session_id": openTestUUID,
		})
		jumpFacts.Live = []OpenLiveEntry{{PID: 7, SessionID: openTestUUID}}
		jumpFacts.PaneFor = func(int) string { return "p7" }
		jump := DecideOpen(jumpFacts).Verdict

		expectedKeys := []string{
			"kind", "vault", "name", "role", "role_source", "mode", "mode_note",
			"branch", "path", "window_key", "window_id", "session_id", "pane_id",
			"bind", "reason", "remedy", "message",
		}
		tokens := map[string][]string{
			"role":        {RoleAgent, RoleHuman, RoleManager},
			"role_source": {OpenRoleSourceOnDisk, OpenRoleSourceDefaulted, OpenRoleSourceKind},
			"mode":        {SpawnModeHeadless, SpawnModeInteractive},
			"mode_note":   {"", OpenModeNoteNormalised},
			"branch": {
				OpenBranchJump, OpenBranchLive, OpenBranchResume,
				OpenBranchCreate, OpenBranchYours, OpenBranchRefused,
			},
			"path": {OpenPathHeadless, OpenPathTab, OpenPathNone},
			"reason": {
				OpenReasonMissingRow, OpenReasonRowUnreadable, OpenReasonUndecidableRole,
				OpenReasonManagerModeHeadless, OpenReasonTruncatedID,
				OpenReasonLivenessUnreadable, OpenReasonAnswerPath,
				OpenReasonRoleMapKeyMissing, OpenReasonHoldWindow,
				OpenReasonGoalLoopUnsupported, OpenReasonTargetUnsupported,
				OpenReasonInFlight, OpenReasonSpawnRefused,
			},
		}

		for _, verdict := range []OpenVerdict{yours, live, resume, create, refused, jump} {
			raw, err := json.Marshal(verdict)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var decoded map[string]any
			gomega.Expect(json.Unmarshal(raw, &decoded)).To(gomega.Succeed())
			keys := make([]string, 0, len(decoded))
			for key := range decoded {
				keys = append(keys, key)
			}
			gomega.Expect(keys).To(gomega.ConsistOf(expectedKeys))
			for field, allowed := range tokens {
				value, _ := decoded[field].(string)
				if value == "" {
					continue
				}
				gomega.Expect(allowed).To(gomega.ContainElement(value))
			}
		}
	})
})

var _ = ginkgo.Describe("FormatOpenLogLine", func() {
	ginkgo.It("renders an agent resume line", func() {
		verdict := OpenVerdict{
			Kind:   OpenKindTask,
			Vault:  "v",
			Name:   "T",
			Role:   RoleAgent,
			Mode:   SpawnModeHeadless,
			Branch: OpenBranchResume,
			Path:   OpenPathHeadless,
		}
		gomega.Expect(FormatOpenLogLine(verdict)).To(gomega.Equal(
			"open: row=v/task/T role=agent mode=headless branch=resume path=headless window=none reason=-",
		))
	})

	ginkgo.It("renders a human tab line with its window id", func() {
		verdict := OpenVerdict{
			Kind:     OpenKindTask,
			Vault:    "v",
			Name:     "T",
			Role:     RoleHuman,
			Mode:     SpawnModeInteractive,
			Branch:   OpenBranchCreate,
			Path:     OpenPathTab,
			WindowID: new(12),
		}
		gomega.Expect(FormatOpenLogLine(verdict)).To(gomega.Equal(
			"open: row=v/task/T role=human mode=interactive branch=create path=tab window=12 reason=-",
		))
	})

	ginkgo.It("renders a missing-row refusal with undecided fields as dashes", func() {
		verdict := OpenVerdict{
			Kind:   OpenKindTask,
			Vault:  "v",
			Name:   "T",
			Branch: OpenBranchRefused,
			Path:   OpenPathNone,
			Reason: OpenReasonMissingRow,
		}
		gomega.Expect(FormatOpenLogLine(verdict)).To(gomega.Equal(
			"open: row=v/task/T role=- mode=- branch=refused path=none window=none reason=missing-row",
		))
	})
})
