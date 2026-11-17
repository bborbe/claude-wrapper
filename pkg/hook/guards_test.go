// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/hook"
)

// bashInput builds a Bash tool call carrying command.
func bashInput(command string) hook.Input {
	payload, err := json.Marshal(map[string]string{"command": command})
	Expect(err).NotTo(HaveOccurred())
	return hook.Input{ToolName: "Bash", ToolInput: payload}
}

// fileInput builds an Edit/Write/Read call carrying path.
func fileInput(tool string, path string) hook.Input {
	payload, err := json.Marshal(map[string]string{"file_path": path})
	Expect(err).NotTo(HaveOccurred())
	return hook.Input{ToolName: tool, ToolInput: payload}
}

// searchInput builds a Glob or Grep call.
func searchInput(tool string, pattern string, path string) hook.Input {
	payload, err := json.Marshal(map[string]string{"pattern": pattern, "path": path})
	Expect(err).NotTo(HaveOccurred())
	return hook.Input{ToolName: tool, ToolInput: payload}
}

var _ = Describe("TypoGuard", func() {
	guard := hook.NewTypoGuard()

	DescribeTable("denies the mistyped home directory and nothing else",
		func(input hook.Input, denied bool, reason string) {
			decision := guard.Evaluate(input)
			Expect(decision.Deny).To(Equal(denied))
			if denied {
				Expect(decision.Reason).To(Equal(reason))
			}
		},
		Entry("bash command with the typo",
			bashInput("ls /Users/bborge/Documents"), true,
			"Typo '/Users/bborge' in command. Use '/Users/bborbe' (your home directory) and retry."),
		Entry("bash command without the typo",
			bashInput("ls /Users/bborbe/Documents"), false, ""),
		Entry("edit path with the typo",
			fileInput("Edit", "/Users/bborge/x.go"), true,
			"Typo in file_path: '/Users/bborge/x.go'. Correct path: '/Users/bborbe/x.go'. "+
				"Retry with the corrected path."),
		Entry("write path with the typo",
			fileInput("Write", "/Users/bborge/y.md"), true,
			"Typo in file_path: '/Users/bborge/y.md'. Correct path: '/Users/bborbe/y.md'. "+
				"Retry with the corrected path."),
		Entry("read path without the typo",
			fileInput("Read", "/Users/bborbe/y.md"), false, ""),
		Entry("glob pattern with the typo",
			searchInput("Glob", "/Users/bborge/**/*.go", ""), true,
			"Typo '/Users/bborge' in Glob args. Use '/Users/bborbe' and retry."),
		Entry("grep path with the typo",
			searchInput("Grep", "package", "/Users/bborge"), true,
			"Typo '/Users/bborge' in Grep args. Use '/Users/bborbe' and retry."),
		Entry("grep with neither argument carrying the typo",
			searchInput("Grep", "package", "/Users/bborbe"), false, ""),
	)

	It("does not apply to a tool with no path or command", func() {
		Expect(guard.Applies(hook.Input{ToolName: "Task"})).To(BeFalse())
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("fix-bborge.sh"))
	})
})

var _ = Describe("SleepGuard", func() {
	guard := hook.NewSleepGuard()

	DescribeTable("denies system sleep and leaves a shell sleep alone",
		func(command string, denied bool) {
			decision := guard.Evaluate(bashInput(command))
			Expect(decision.Deny).To(Equal(denied))
		},
		Entry("pmset sleepnow", "pmset sleepnow", true),
		Entry("pmset with a battery flag", "pmset -a displaysleepnow", true),
		Entry("systemsetup sleepnow", "systemsetup -sleepnow", true),
		Entry("osascript telling System Events to sleep",
			`osascript -e 'tell application "System Events" to sleep'`, true),
		Entry("osascript telling Finder to restart",
			`osascript -e 'tell application "Finder" to restart'`, true),
		Entry("a shell sleep is an ordinary poll loop", "sleep 8", false),
		Entry("an unrelated command", "ls -la", false),
		Entry("pmset reading the battery", "pmset -g batt", false),
	)

	It("keeps the denial text byte-identical to the script", func() {
		decision := guard.Evaluate(bashInput("pmset sleepnow"))
		Expect(decision.Reason).To(Equal(
			"Blocked: this command can suspend the operator's Mac.\n\n" +
				"A session may not put the machine to sleep, wake it in a loop, shut it down or\n" +
				"restart it. On 2026-11-14 a session did exactly that to satisfy a task criterion\n" +
				"and the operator had to kill every Claude process to get the machine back.\n\n" +
				"If the work genuinely needs a sleep window, ask the operator to close the lid or\n" +
				"run it themselves. Shell `sleep N` is fine and is not blocked by this hook."))
	})

	It("does not apply to a non-Bash tool", func() {
		Expect(guard.Applies(hook.Input{ToolName: "Read"})).To(BeFalse())
	})
})

var _ = Describe("Registry", func() {
	It("returns a pass-through when no guard has an opinion", func() {
		registry := hook.NewRegistry(hook.NewTypoGuard(), hook.NewSleepGuard())
		Expect(registry.Evaluate(bashInput("ls -la"))).To(Equal(hook.Decision{}))
	})

	It("lets a deny win over an allow regardless of order", func() {
		registry := hook.NewRegistry(allowGuard{}, denyGuard{})
		Expect(registry.Evaluate(bashInput("anything")).Deny).To(BeTrue())
	})

	It("returns the first allow when nothing denies", func() {
		registry := hook.NewRegistry(allowGuard{})
		decision := registry.Evaluate(bashInput("anything"))
		Expect(decision.Allow).To(BeTrue())
		Expect(decision.Reason).To(Equal("allowed"))
	})

	It("lists the guard names in evaluation order", func() {
		registry := hook.NewRegistry(hook.NewTypoGuard(), hook.NewSleepGuard())
		Expect(registry.Names()).To(Equal([]string{"fix-bborge.sh", "deny-sleep-commands.sh"}))
	})
})

// allowGuard and denyGuard are the two fixed opinions the registry tests need;
// the real guards would make the ordering test depend on their own rules.
type allowGuard struct{}

func (allowGuard) Name() string { return "allow" }

func (allowGuard) Applies(hook.Input) bool { return true }

func (allowGuard) Evaluate(hook.Input) hook.Decision {
	return hook.AllowDecision("allowed")
}

type denyGuard struct{}

func (denyGuard) Name() string { return "deny" }

func (denyGuard) Applies(hook.Input) bool { return true }

func (denyGuard) Evaluate(hook.Input) hook.Decision {
	return hook.DenyDecision("denied")
}
