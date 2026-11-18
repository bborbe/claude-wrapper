// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

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

var _ = Describe("CredentialTraceGuard", func() {
	guard := hook.NewCredentialTraceGuard()

	secretScript := tempRepo(map[string]string{
		"fetch.sh": "#!/bin/bash\nPASSWORD=$(teamvault-cli password team/app)\n",
		"plain.sh": "#!/bin/bash\necho hello\n",
	})

	DescribeTable("denies a spliced lookup and a trace of a secret-reading script",
		func(command string, denied bool) {
			Expect(guard.Evaluate(bashInput(command)).Deny).To(Equal(denied))
		},
		Entry("a lookup spliced into argv",
			`echo "$(teamvault-cli password team/app)"`, true),
		Entry("a lookup piped onward",
			"teamvault-cli password team/app | shasum -a 256", false),
		Entry("a trace of a secret-reading script",
			"bash -x "+secretScript+"/fetch.sh", true),
		Entry("a trace of a plain script",
			"bash -x "+secretScript+"/plain.sh", false),
		Entry("an untraced run of the secret-reading script",
			"bash "+secretScript+"/fetch.sh", false),
		Entry("set -x before a secret-reading script",
			"set -x && bash "+secretScript+"/fetch.sh", true),
		Entry("an unrelated command", "ls -la", false),
	)

	It("names the resolved script path in the trace denial", func() {
		decision := guard.Evaluate(bashInput("bash -x " + secretScript + "/fetch.sh"))
		Expect(decision.Reason).To(HavePrefix(
			"Refused: a shell trace (`-x`) on " + secretScript + "/fetch.sh"))
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("deny-credential-trace.sh"))
	})
})

// tempGitRepo writes a minimal git repository with HEAD on branch and returns it.
func tempGitRepo(files map[string]string, branch string) string {
	dir := tempRepo(files)
	Expect(os.MkdirAll(dir+"/.git", 0o700)).To(Succeed())
	Expect(os.WriteFile(
		dir+"/.git/HEAD", []byte("ref: refs/heads/"+branch+"\n"), 0o600,
	)).To(Succeed())
	return dir
}

var _ = Describe("PushGateGuard", func() {
	guard := hook.NewPushGateGuard()

	withMaintainer := tempGitRepo(map[string]string{".maintainer.yaml": "release:\n"}, "master")
	withoutMaintainer := tempGitRepo(map[string]string{}, "master")

	DescribeTable("gates a push on what it does",
		func(command string, cwd string, denied bool, allow bool) {
			input := bashInput(command)
			input.Cwd = cwd
			decision := guard.Evaluate(input)
			Expect(decision.Deny).To(Equal(denied))
			Expect(decision.Allow).To(Equal(allow))
		},
		Entry("master in a maintainer repo", "git push origin master", withMaintainer, true, false),
		Entry("master in a repo with no maintainer file",
			"git push origin master", withoutMaintainer, false, false),
		Entry("a feature branch", "git push -u origin feat/x", withMaintainer, false, true),
		Entry("a fix branch", "git push origin fix/x", withMaintainer, false, true),
		Entry("a Seibert ticket branch", "git push origin BRO-21462-fix", withMaintainer, false, true),
		Entry("an environment branch", "git push origin dev", withMaintainer, false, true),
		Entry("a force push", "git push --force origin feat/x", withMaintainer, true, false),
		Entry("a ref deletion", "git push origin :feat/x", withMaintainer, true, false),
		Entry("a tag", "git push origin v1.2.3", withMaintainer, true, false),
		Entry("no arguments", "git push", withMaintainer, true, false),
		Entry("no refspec", "git push origin", withMaintainer, true, false),
		Entry("an unknown branch shape",
			"git push origin scratch/verify", withMaintainer, true, false),
		Entry("HEAD", "git push origin HEAD", withMaintainer, true, false),
		Entry("a task title that merely names git push",
			`vault-cli task get "The git push Ask-Rule Fires on Feature Branches"`,
			withMaintainer, false, false),
	)

	It("substitutes the current-branch idiom before gating", func() {
		repo := tempGitRepo(map[string]string{}, "feat/live")
		input := bashInput(`git push origin "$(git branch --show-current)"`)
		input.Cwd = repo
		Expect(guard.Evaluate(input).Allow).To(BeTrue())
	})

	It("keeps the default suffix byte-identical", func() {
		input := bashInput("git push --force origin feat/x")
		input.Cwd = withMaintainer
		Expect(guard.Evaluate(input).Reason).To(Equal(
			"Refused: force-push (--force/-f/--force-with-lease/--mirror) is gated. " +
				"The operator must approve this push explicitly. Hand it over as " +
				"`python3 ~/.claude/scripts/approve-run.py --cmd '<the same git push>' --cwd <repo>` " +
				"— that posts an approval card and runs the push once they answer `run it`. " +
				"Retrying cannot clear this: the gate evaluates the command, not the conversation."))
	})

	It("gives an under-specified push an agent-clearable remedy, not the operator suffix", func() {
		input := bashInput("git push origin")
		input.Cwd = withMaintainer
		Expect(guard.Evaluate(input).Reason).To(ContainSubstring(
			"No operator approval is needed for this."))
	})
})

var _ = Describe("CdGitChainGuard", func() {
	guard := hook.NewCdGitChainGuard()

	DescribeTable("denies a cd followed by a git segment",
		func(command string, denied bool) {
			Expect(guard.Evaluate(bashInput(command)).Deny).To(Equal(denied))
		},
		Entry("cd then git", "cd /tmp && git status", true),
		Entry("cd then git with a semicolon", "cd /tmp; git status", true),
		Entry("cd then git on the next line", "cd /tmp\ngit status", true),
		Entry("git then cd", "git status && cd /tmp", false),
		Entry("cd then a non-git command", "cd /tmp && ls -la", false),
		Entry("a git with no cd", "ls && git status", false),
		Entry("a parenthesised loop body", "(cd /tmp && git status)", true),
		Entry("a do-prefixed loop body", "do cd /tmp && git status", true),
		Entry("a bash -c payload", `bash -c 'cd /tmp && git status'`, true),
		Entry("a heredoc body that mentions the chain",
			"cat <<'EOF'\ncd /x && git status\nEOF", false),
		Entry("a real chain followed by a heredoc", "cd /x && git status <<EOF\nbody\nEOF", true),
		Entry("an unbalanced quote is left to the normal flow",
			`cd /x && git status "`, false),
		Entry("an unrelated command", "ls -la", false),
	)

	It("keeps the denial text byte-identical", func() {
		Expect(guard.Evaluate(bashInput("cd /tmp && git status")).Reason).To(HavePrefix(
			"Refused: this command chains `cd <dir>` with `git`, which the rules forbid " +
				"(~/.claude/CLAUDE.md § Git — No Git -C Flag)."))
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("deny-cd-git-chain.py"))
	})
})

// tempWorktree builds a linked-worktree checkout: a .git pointer file whose
// target carries a commondir, so the git dir and the common dir differ.
func tempWorktree(main string) string {
	wt := tempRepo(map[string]string{})
	gitdir := main + "/.git/worktrees/wt"
	Expect(os.MkdirAll(gitdir, 0o700)).To(Succeed())
	Expect(os.WriteFile(gitdir+"/commondir", []byte("../..\n"), 0o600)).To(Succeed())
	Expect(os.WriteFile(
		wt+"/.git", []byte("gitdir: "+gitdir+"\n"), 0o600,
	)).To(Succeed())
	return wt
}

var _ = Describe("BranchSwitchGuard", func() {
	guard := hook.NewBranchSwitchGuard()

	main := tempGitRepo(map[string]string{}, "master")
	Expect(os.MkdirAll(main+"/.git/refs/heads", 0o700)).To(Succeed())
	Expect(os.WriteFile(
		main+"/.git/refs/heads/existing", []byte("0000000000000000000000000000000000000000\n"), 0o600,
	)).To(Succeed())
	Expect(os.WriteFile(main+"/README.md", []byte("x\n"), 0o600)).To(Succeed())
	linked := tempWorktree(main)

	DescribeTable("denies a branch switch in a main checkout only",
		func(command string, cwd string, denied bool) {
			input := bashInput(command)
			input.Cwd = cwd
			Expect(guard.Evaluate(input).Deny).To(Equal(denied))
		},
		Entry("checkout -b in the main checkout", "git checkout -b feat/x", main, true),
		Entry("switch -c in the main checkout", "git switch -c feat/x", main, true),
		Entry("switching back to trunk", "git checkout master", main, false),
		Entry("a path restore with --", "git checkout -- file.txt", main, false),
		Entry("an existing branch", "git checkout existing", main, true),
		Entry("a path that names no ref", "git checkout README.md", main, false),
		Entry("the same switch inside a linked worktree",
			"git checkout -b feat/x", linked, false),
		Entry("a switch after a fetch", "git fetch && git checkout -b feat/x", main, true),
		Entry("an allowed switch followed by a denied one",
			"git checkout master && git checkout -b feat/x", main, true),
		Entry("quoted prose naming a switch",
			`echo "git checkout -b x"`, main, false),
		Entry("an unrelated command", "ls -la", main, false),
	)

	It("keeps the refusal text byte-identical", func() {
		input := bashInput("git checkout -b feat/x")
		input.Cwd = main
		Expect(guard.Evaluate(input).Reason).To(Equal(
			"Refused: `git checkout -b feat/x` switches the branch of a repo's main checkout (" +
				main + "). The main checkout stays on main/master — a branch lives in a worktree. " +
				"Create it with:\n" +
				"  git fetch && git worktree add --no-track ../<repo>-<name> -b <branch> " +
				"origin/master\n" +
				"Or switch back to trunk. Work inside an existing linked worktree is unaffected."))
	})
})

// runToolLog evaluates the log guard against a temp HOME and returns the record
// it appended.
func runToolLog(command string) map[string]any {
	home := tempRepo(map[string]string{})
	previous := os.Getenv("HOME")
	Expect(os.Setenv("HOME", home)).To(Succeed())
	defer func() { Expect(os.Setenv("HOME", previous)).To(Succeed()) }()

	input := bashInput(command)
	input.HookEventName = hook.EventPostToolUse
	input.SessionID = "sess-1"
	input.ToolUseID = "tool-1"
	input.Cwd = "/tmp"
	Expect(hook.NewToolLogGuard().Evaluate(input).Deny).To(BeFalse())

	entries, err := os.ReadDir(home + "/.claude/state/tool-executions")
	Expect(err).NotTo(HaveOccurred())
	Expect(entries).To(HaveLen(1))
	data, err := os.ReadFile(home + "/.claude/state/tool-executions/" + entries[0].Name())
	Expect(err).NotTo(HaveOccurred())
	var record map[string]any
	Expect(json.Unmarshal(data, &record)).To(Succeed())
	return record
}

var _ = Describe("ToolLogGuard", func() {
	guard := hook.NewToolLogGuard()

	DescribeTable("classifies the shape of a command",
		func(command string, classes []any) {
			Expect(runToolLog(command)["cls"]).To(Equal(classes))
		},
		Entry("a loop", "for i in 1 2; do echo $i; done", []any{"loop", "chain"}),
		Entry("a substitution", "echo $(date)", []any{"subst"}),
		Entry("a redirect", "ls > out.txt", []any{"redir"}),
		Entry("a pipe", "ls | grep x", []any{"pipe"}),
		Entry("a chain", "ls && pwd", []any{"chain"}),
		Entry("a fallback is a chain, not a pipe", "ls || true", []any{"chain"}),
		Entry("a plain command", "ls -la", []any{}),
	)

	It("records the identifying fields and never the command", func() {
		record := runToolLog("ls -la")
		Expect(record["session_id"]).To(Equal("sess-1"))
		Expect(record["tool_name"]).To(Equal("Bash"))
		Expect(record["tool_use_id"]).To(Equal("tool-1"))
		Expect(record["cwd"]).To(Equal("/tmp"))
		Expect(record["ts"]).To(HaveSuffix("Z"))
		Expect(record).NotTo(HaveKey("command"))
	})

	It("does not apply to a PreToolUse payload", func() {
		Expect(guard.Applies(hook.Input{HookEventName: hook.EventPreToolUse, ToolName: "Bash"})).
			To(BeFalse())
	})
})

// blockedLogHome points HOME at a fresh directory for the duration of fn.
func blockedLogHome(fn func(home string)) {
	home := tempRepo(map[string]string{})
	previous := os.Getenv("HOME")
	Expect(os.Setenv("HOME", home)).To(Succeed())
	defer func() { Expect(os.Setenv("HOME", previous)).To(Succeed()) }()
	fn(home)
}

// blockedLogDB returns the raw blocked-permission lines.
func blockedLogDB(home string) []string {
	data, err := os.ReadFile(home + "/.claude/state/blocked-permissions.jsonl")
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

var _ = Describe("BlockedLogGuard", func() {
	guard := hook.NewBlockedLogGuard()

	// emit runs one event for the given command.
	emit := func(event string, command string) {
		input := bashInput(command)
		input.HookEventName = event
		input.SessionID = "sess-1"
		Expect(guard.Evaluate(input).Deny).To(BeFalse())
	}

	It("adds an entry on a permission request", func() {
		blockedLogHome(func(home string) {
			emit(hook.EventPermissionRequest, "ls -la")
			lines := blockedLogDB(home)
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]).To(ContainSubstring(`"tool":"Bash"`))
			page, err := os.ReadFile(home + "/.claude/blocked.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(page)).To(ContainSubstring("**sess-1** · `Bash` ·"))
		})
	})

	It("does not add the same entry twice", func() {
		blockedLogHome(func(home string) {
			emit(hook.EventPermissionRequest, "ls -la")
			emit(hook.EventPermissionRequest, "ls -la")
			Expect(blockedLogDB(home)).To(HaveLen(1))
		})
	})

	It("removes the entry when the tool ran", func() {
		blockedLogHome(func(home string) {
			emit(hook.EventPermissionRequest, "ls -la")
			emit(hook.EventPostToolUse, "ls -la")
			Expect(blockedLogDB(home)).To(BeEmpty())
			page, err := os.ReadFile(home + "/.claude/blocked.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(page)).To(Equal("# Blocked permissions\n\nNone pending.\n"))
		})
	})

	It("removes the entry when the prompt was denied", func() {
		blockedLogHome(func(home string) {
			emit(hook.EventPermissionRequest, "ls -la")
			emit(hook.EventPermissionDenied, "ls -la")
			Expect(blockedLogDB(home)).To(BeEmpty())
		})
	})

	It("keys on the tool input, so two commands are two entries", func() {
		blockedLogHome(func(home string) {
			emit(hook.EventPermissionRequest, "ls -la")
			emit(hook.EventPermissionRequest, "pwd")
			Expect(blockedLogDB(home)).To(HaveLen(2))
		})
	})

	It("does not apply to a PreToolUse payload", func() {
		Expect(guard.Applies(hook.Input{HookEventName: hook.EventPreToolUse, ToolName: "Bash"})).
			To(BeFalse())
	})
})

var _ = Describe("MergeLabelGuard", func() {
	var labels string
	var runnerErr error
	guard := hook.NewMergeLabelGuardWithRunner(func(args ...string) (string, error) {
		if runnerErr != nil {
			return "", runnerErr
		}
		return labels, nil
	})

	BeforeEach(func() {
		labels = ""
		runnerErr = nil
	})

	It("denies a merge while the do-not-merge label is on", func() {
		labels = "do-not-merge\nenhancement\n"
		decision := guard.Evaluate(bashInput("gh pr merge 42 --repo o/r"))
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(HavePrefix(
			"Blocked by block-do-not-merge-merge: PR carries the do-not-merge label"))
	})

	It("allows a merge with other labels", func() {
		labels = "enhancement\n"
		Expect(guard.Evaluate(bashInput("gh pr merge 42 --repo o/r")).Deny).To(BeFalse())
	})

	It("denies two merge targets in one command", func() {
		command := "gh pr merge 1 --repo o/r && gh pr merge 2 --repo o/r"
		Expect(guard.Evaluate(bashInput(command)).Reason).To(Equal(
			"Blocked by block-do-not-merge-merge: more than one gh pr merge target in one " +
				"command — run each merge as its own call."))
	})

	It("does not read a quoted mention as a merge", func() {
		command := `gh pr comment 1 --body "gh pr merge 2"`
		Expect(guard.Evaluate(bashInput(command)).Deny).To(BeFalse())
	})

	It("fails open when the label lookup errors", func() {
		runnerErr = errors.New("network down")
		Expect(guard.Evaluate(bashInput("gh pr merge 42 --repo o/r")).Deny).To(BeFalse())
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("block-do-not-merge-merge.sh"))
	})
})

var _ = Describe("MergeAllowGuard", func() {
	var state string
	var runnerErr error
	guard := hook.NewMergeAllowGuardWithRunner(func(args ...string) (string, error) {
		if runnerErr != nil {
			return "", runnerErr
		}
		return state, nil
	})

	BeforeEach(func() {
		state = `{"reviewDecision":"APPROVED","mergeable":"MERGEABLE",` +
			`"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`
		runnerErr = nil
	})

	It("allows a positively green merge", func() {
		decision := guard.Evaluate(bashInput("gh pr merge 42 --repo o/r"))
		Expect(decision.Allow).To(BeTrue())
		Expect(decision.Reason).To(Equal(
			"bot-APPROVED on head, mergeable, every check completed-passing for: o/r#42"))
	})

	It("refuses --admin on the text alone", func() {
		decision := guard.Evaluate(bashInput("gh pr merge 42 --repo o/r --admin"))
		Expect(decision.Deny).To(BeTrue())
		Expect(decision.Reason).To(Equal(
			"gh pr merge --admin bypasses branch protection; this hook refuses it for every " +
				"repo, including owners no deny rule covers."))
	})

	DescribeTable("makes no decision when the state is not positively green",
		func(payload string) {
			state = payload
			Expect(guard.Evaluate(bashInput("gh pr merge 42 --repo o/r"))).To(Equal(
				hook.Decision{}))
		},
		Entry("no approval",
			`{"reviewDecision":"CHANGES_REQUESTED","mergeable":"MERGEABLE"}`),
		Entry("mergeable unknown",
			`{"reviewDecision":"APPROVED","mergeable":"UNKNOWN"}`),
		Entry("a check still running", `{"reviewDecision":"APPROVED","mergeable":"MERGEABLE",`+
			`"statusCheckRollup":[{"status":"IN_PROGRESS","conclusion":""}]}`),
		Entry("a failing check", `{"reviewDecision":"APPROVED","mergeable":"MERGEABLE",`+
			`"statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"}]}`),
	)

	It("makes no decision for an operator-declined repo", func() {
		Expect(guard.Evaluate(bashInput("gh pr merge 42 --repo bborbe/vault-cli"))).To(Equal(
			hook.Decision{}))
	})

	It("makes no decision when the target repo cannot be read", func() {
		Expect(guard.Evaluate(bashInput("gh pr merge 42"))).To(Equal(hook.Decision{}))
	})

	It("makes no decision when the API cannot be read", func() {
		runnerErr = errors.New("network down")
		Expect(guard.Evaluate(bashInput("gh pr merge 42 --repo o/r"))).To(Equal(hook.Decision{}))
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("allow-gh-pr-merge.py"))
	})
})
