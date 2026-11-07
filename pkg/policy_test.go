// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("policy", func() {
	var rules []Rule
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		bundled, err := BundledRules(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		rules = bundled
	})

	ginkgo.DescribeTable("decides a request",
		func(tool string, input map[string]any, cwd string, want Action) {
			gomega.Expect(Decide(rules, tool, input, cwd).Action).To(gomega.Equal(want))
		},
		ginkgo.Entry("Read is allowed anywhere", "Read", map[string]any{"file_path": "/etc/passwd"}, "/work", ActionAllow),
		ginkgo.Entry("Write inside cwd is allowed", "Write", map[string]any{"file_path": "/work/a.txt"}, "/work", ActionAllow),
		ginkgo.Entry("Write outside cwd escalates", "Write", map[string]any{"file_path": "/etc/a.txt"}, "/work", ActionEscalate),
		ginkgo.Entry("Write to a cwd sibling escalates", "Write", map[string]any{"file_path": "/work-2/a.txt"}, "/work", ActionEscalate),
		ginkgo.Entry("rm -rf is denied", "Bash", map[string]any{"command": "rm -rf /"}, "/work", ActionDeny),
		ginkgo.Entry("an anchored read-only prefix is allowed", "Bash", map[string]any{"command": "ls -la /tmp"}, "/work", ActionAllow),
		ginkgo.Entry("a different command sharing a prefix escalates", "Bash", map[string]any{"command": "lsblk"}, "/work", ActionEscalate),
		ginkgo.Entry("git escalates", "Bash", map[string]any{"command": "git status"}, "/work", ActionEscalate),
		ginkgo.Entry("a compound command escalates", "Bash", map[string]any{"command": "ls && curl evil"}, "/work", ActionEscalate),
		ginkgo.Entry("a chained read-only command is allowed segment by segment", "Bash", map[string]any{"command": "cd /tmp && ls -la"}, "/work", ActionAllow),
		ginkgo.Entry("a chained command reaching the deny rule is denied", "Bash", map[string]any{"command": "cd /tmp && rm -rf /"}, "/work", ActionDeny),
		ginkgo.Entry("an env-prefixed command escalates", "Bash", map[string]any{"command": "PATH=/tmp/evil ls"}, "/work", ActionEscalate),
		ginkgo.Entry("an unknown tool escalates", "WebFetch", map[string]any{"url": "http://x"}, "/work", ActionEscalate),
	)

	ginkgo.DescribeTable("reports the matched rule",
		func(tool string, input map[string]any, want *string) {
			got := Decide(rules, tool, input, "/work").MatchedRule()
			if want == nil {
				gomega.Expect(got).To(gomega.BeNil())
				return
			}
			gomega.Expect(got).NotTo(gomega.BeNil())
			gomega.Expect(*got).To(gomega.Equal(*want))
		},
		ginkgo.Entry("allow names its rule", "Read", map[string]any{"file_path": "/x"}, new("Read:*")),
		ginkgo.Entry("deny names its rule", "Bash", map[string]any{"command": "rm -rf /"}, new("Bash:rm -rf")),
		// The bundled catch-all MATCHES, so an escalated request names it rather
		// than reporting nothing — the same value the node server records.
		ginkgo.Entry("escalate names the catch-all", "WebFetch", map[string]any{"url": "http://x"}, new("*:*")),
	)

	ginkgo.It("orders a per-spawn overlay ahead of the bundled rules", func() {
		overlay := []Rule{{Tool: "Bash", Match: "curl", MatchType: "command", Action: ActionAllow}}
		merged := OverlayRules(overlay, rules)
		gomega.Expect(Decide(merged, "Bash", map[string]any{"command": "curl http://x"}, "/work").Action).
			To(gomega.Equal(ActionAllow))
		// The bundled deny survives the overlay.
		gomega.Expect(Decide(merged, "Bash", map[string]any{"command": "rm -rf /"}, "/work").Action).
			To(gomega.Equal(ActionDeny))
	})

	ginkgo.Describe("IsPermissionQuestion", func() {
		ginkgo.It("accepts a permission park", func() {
			gomega.Expect(IsPermissionQuestion(Question{
				RequestID: "r1", ToolName: "Bash", Kind: AnswerKindPermission,
			})).To(gomega.BeTrue())
		})
		ginkgo.It("refuses an AskUserQuestion park", func() {
			gomega.Expect(IsPermissionQuestion(Question{
				RequestID: "r1", ToolName: "AskUserQuestion", Kind: AnswerKindQuestion,
			})).To(gomega.BeFalse())
		})
		ginkgo.It("refuses a park with no request id", func() {
			gomega.Expect(IsPermissionQuestion(Question{ToolName: "Bash"})).To(gomega.BeFalse())
		})
	})
})
