// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook_test

import (
	"encoding/json"
	"os"

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

var _ = Describe("AttributionGuard", func() {
	guard := hook.NewAttributionGuard()

	DescribeTable("denies an attribution line and leaves ordinary prose alone",
		func(command string, denied bool) {
			Expect(guard.Evaluate(bashInput(command)).Deny).To(Equal(denied))
		},
		Entry("a plain commit", `git commit -m "feat: add the hook binary"`, false),
		Entry("a generated-with line", `git commit -m "x\n\nGenerated with Claude Code"`, true),
		Entry("a co-authored-by trailer", `git commit -m "x\n\nCo-Authored-By: DeepSeek v4.1"`, true),
		Entry("an ordinary commit about cursor", `git commit -m "feat: add cursor support"`, false),
		Entry("an ordinary commit about codex", `git commit -m "docs: codex notes"`, false),
		Entry("a PR body carrying the marker", `gh pr create --body "Generated with Claude Code"`, true),
		Entry("an unrelated command", `ls -la`, false),
	)

	It("reads a --body-file and denies on its contents", func() {
		file, err := os.CreateTemp("", "pr-body-*.md")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.Remove(file.Name())).To(Succeed()) }()
		_, err = file.WriteString("body\n\nGenerated with Claude Code\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Close()).To(Succeed())
		command := "gh pr create --body-file " + file.Name()
		Expect(guard.Evaluate(bashInput(command)).Deny).To(BeTrue())
	})

	It("does not apply to a Bash command that publishes nothing", func() {
		Expect(guard.Applies(bashInput("ls -la"))).To(BeFalse())
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("deny-attribution.sh"))
	})
})

// tempRepo writes files into a fresh directory and returns it.
func tempRepo(files map[string]string) string {
	dir, err := os.MkdirTemp("", "hook-repo-*")
	Expect(err).NotTo(HaveOccurred())
	for name, content := range files {
		Expect(os.WriteFile(dir+"/"+name, []byte(content), 0o600)).To(Succeed())
	}
	return dir
}

var _ = Describe("ProdDeployGuard", func() {
	guard := hook.NewProdDeployGuard()

	prodRepo := tempRepo(map[string]string{
		"Makefile.env": "ifeq ($(BRANCH),master)\nSTAGE := prod\ninclude prod.env\nendif\n",
	})
	devRepo := tempRepo(map[string]string{
		"Makefile.env": "ifeq ($(BRANCH),dev)\nSTAGE := dev\ninclude dev.env\nendif\n",
	})
	plainRepo := tempRepo(map[string]string{"Makefile.env": "STAGE := dev\n"})

	It("denies BRANCH=prod make apply", func() {
		decision := guard.Evaluate(bashInput("BRANCH=prod make apply"))
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(HavePrefix(
			"Refused: production deploy (BRANCH=prod make buca/apply/deploy). "))
	})

	It("denies a make inside a *-prod worktree", func() {
		decision := guard.Evaluate(bashInput("cd ../trading-prod && make buca"))
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(HavePrefix(
			"Refused: make buca/apply/deploy inside a *-prod worktree. "))
	})

	It("denies a make whose session cwd is a *-prod worktree", func() {
		input := bashInput("make apply")
		input.Cwd = "/Users/bborbe/Documents/workspaces/trading-prod"
		decision := guard.Evaluate(input)
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(ContainSubstring("trading-prod"))
	})

	It("lets a make with its own cd through the cwd arm", func() {
		input := bashInput("cd /tmp && make apply")
		input.Cwd = "/Users/bborbe/Documents/workspaces/trading-prod"
		Expect(guard.Evaluate(input).Deny).To(BeFalse())
	})

	It("lets a dev deploy through when the repo maps dev onto a dev stage", func() {
		input := bashInput("BRANCH=dev make apply")
		input.Cwd = devRepo
		Expect(guard.Evaluate(input).Deny).To(BeFalse())
	})

	It("denies a dev deploy whose repo maps no dev stage", func() {
		input := bashInput("BRANCH=dev make apply")
		input.Cwd = plainRepo
		decision := guard.Evaluate(input)
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(HavePrefix(
			"Refused: dev deploy whose target repo does not map BRANCH=dev onto a dev stage"))
	})

	It("denies BRANCH=master make apply where master maps to prod", func() {
		input := bashInput("BRANCH=master make apply")
		input.Cwd = prodRepo
		decision := guard.Evaluate(input)
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(HavePrefix(
			"Refused: production deploy (BRANCH=master maps to prod in "))
	})

	It("denies BRANCH=master make upgrade where master maps to prod", func() {
		input := bashInput("BRANCH=master make upgrade")
		input.Cwd = prodRepo
		decision := guard.Evaluate(input)
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(HavePrefix(
			"Refused: production deploy (BRANCH=master make install/upgrade maps to prod in "))
	})

	It("does not read BRANCH=master as prod where the repo does not map it", func() {
		input := bashInput("BRANCH=master make apply")
		input.Cwd = plainRepo
		Expect(guard.Evaluate(input).Deny).To(BeFalse())
	})

	It("denies a destructive verb on a prod kubectl wrapper", func() {
		decision := guard.Evaluate(bashInput("kubectlnukeprod -n prod delete pod x"))
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(HavePrefix(
			"Refused: destructive kubectl verb on a production target."))
	})

	It("lets a read-only verb on a prod wrapper through", func() {
		Expect(guard.Evaluate(bashInput("kubectlprod -n prod get pods")).Deny).To(BeFalse())
	})

	It("lets an approve-run hand-over through", func() {
		command := "python3 ~/.claude/scripts/approve-run.py --cmd 'BRANCH=prod make apply'"
		Expect(guard.Evaluate(bashInput(command)).Deny).To(BeFalse())
	})

	It("does not read a commit message that mentions a prod deploy as one", func() {
		command := `git commit -m "BRANCH=prod make apply is now gated"`
		Expect(guard.Evaluate(bashInput(command)).Deny).To(BeFalse())
	})

	It("keeps the shared hand-over tail byte-identical", func() {
		decision := guard.Evaluate(bashInput("BRANCH=prod make apply"))
		Expect(decision.Reason).To(Equal(
			"Refused: production deploy (BRANCH=prod make buca/apply/deploy). " +
				"Prod deploys are operator-gated — hand it over as " +
				"`python3 ~/.claude/scripts/approve-run.py --cmd '<the same command>' --cwd <dir>`: " +
				"that posts an approval card and runs the command once they answer `run it`. " +
				"Never ask the operator to type it."))
	})
})

var _ = Describe("NukeShutdownGuard", func() {
	guard := hook.NewNukeShutdownGuard()

	DescribeTable("denies nuke power-down and leaves inspection and other hosts alone",
		func(command string, denied bool) {
			Expect(guard.Evaluate(bashInput(command)).Deny).To(Equal(denied))
		},
		Entry("the k3s shutdown script", "remote-k3s-shutdown-nuke.sh", true),
		Entry("the shutdown script via sudo and a path",
			"sudo /opt/bin/remote-k3s-shutdown-nuke.sh --yes", true),
		Entry("the VM power-off script", "virsh-shutdown-all.sh", true),
		Entry("virsh destroy inside an ssh payload",
			`ssh nuke1 "sudo virsh destroy vm1"`, true),
		Entry("virsh list is read-only", "virsh list --all", false),
		Entry("stopping k3s", "sudo systemctl stop k3s", true),
		Entry("querying k3s", "systemctl is-active k3s", false),
		Entry("rebooting a nuke host over ssh", "ssh nuke reboot", true),
		Entry("rebooting a non-nuke host over ssh", "ssh fire reboot", false),
		Entry("a commit message that merely names the script",
			`git commit -m "remote-k3s-shutdown-nuke.sh is operator-only"`, false),
		Entry("an unrelated command", "ls -la", false),
	)

	It("keeps the denial text byte-identical, including the approve-run contradiction", func() {
		decision := guard.Evaluate(bashInput("remote-k3s-shutdown-nuke.sh"))
		Expect(decision.Reason).To(Equal(
			"Refused: nuke K3s shutdown / VM power-down is operator-only " +
				"(operator ruling 2026-11-14). No AI session may run it — the operator runs it " +
				"by hand in their own shell. Do not route it through approve-run.py either; " +
				"hand the exact command to the operator as a `you run:` line."))
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("deny-nuke-shutdown.sh"))
	})
})

var _ = Describe("SecretReadGuard", func() {
	guard := hook.NewSecretReadGuard()

	DescribeTable("denies a value-printing read and allows the key-name forms",
		func(command string, denied bool) {
			Expect(guard.Evaluate(bashInput(command)).Deny).To(Equal(denied))
		},
		Entry("get secret -o yaml",
			"kubectl -n prod get secret db -o yaml", true),
		Entry("get cm -o json",
			"kubectl get cm app-config -o json", true),
		Entry("get config -o yaml",
			"kubectl get config agent -o yaml", true),
		Entry("edit a secret",
			"kubectl edit secret db", true),
		Entry("jsonpath into .data",
			"kubectl get secret db -o jsonpath='{.data}'", true),
		Entry("jsonpath into a non-value field",
			"kubectl get secret db -o jsonpath='{.metadata.name}'", false),
		Entry("--raw on secrets",
			"kubectl --raw /api/v1/namespaces/prod/secrets/db", true),
		Entry("get without an output format",
			"kubectl get secrets", false),
		Entry("describe a secret", "kubectl describe secret db", false),
		Entry("get with jq keys as the last stage",
			`kubectl get secret db -o json | jq '.data | keys'`, false),
		Entry("get with a trailing stage after jq",
			`kubectl get secret db -o json | jq '.data | keys' | head -3`, true),
		Entry("an unrelated command", "ls -la", false),
	)

	It("keeps the -o yaml denial text byte-identical", func() {
		decision := guard.Evaluate(bashInput("kubectl get secret db -o yaml"))
		Expect(decision.Reason).To(HavePrefix(
			"Refused: printing a Secret/ConfigMap/Config with -o yaml/json leaks values"))
		Expect(decision.Reason).To(ContainSubstring(
			`"-o json | jq '<path> | keys'" as the last stage for key names`))
	})

	It("does not apply to a Bash command that names no kubectl", func() {
		Expect(guard.Applies(bashInput("ls -la"))).To(BeFalse())
	})
})
