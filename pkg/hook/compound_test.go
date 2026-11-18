// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/hook"
)

var _ = Describe("SplitCompound", func() {
	DescribeTable("splits on top-level separators only",
		func(command string, expected []string, ok bool) {
			segments, splitOK := hook.SplitCompound(command)
			Expect(splitOK).To(Equal(ok))
			if ok {
				Expect(segments).To(Equal(expected))
			}
		},
		Entry("a simple chain", "cd /x && git status",
			[]string{"cd /x", "git status"}, true),
		Entry("a fallback", "ls || pwd", []string{"ls", "pwd"}, true),
		Entry("a semicolon", "ls; pwd", []string{"ls", "pwd"}, true),
		Entry("a newline", "ls\npwd", []string{"ls", "pwd"}, true),
		Entry("a pipe", "ls | grep x", []string{"ls", "grep x"}, true),
		Entry("a separator inside quotes", `echo "a && ls"`,
			[]string{`echo "a && ls"`}, true),
		Entry("an unbalanced quote", `ls "x`, nil, false),
		Entry("one segment", "ls -la", []string{"ls -la"}, true),
	)
})

var _ = Describe("UnsafeSyntax", func() {
	DescribeTable("refuses text that can hide a command",
		func(command string, unsafe bool) {
			Expect(hook.UnsafeSyntax(command)).To(Equal(unsafe))
		},
		Entry("a substitution", "echo $(date)", true),
		Entry("a backtick", "echo `date`", true),
		Entry("a plain chain", "ls && pwd", false),
		Entry("a descriptor duplication", "ls 2>&1", false),
		Entry("a redirect", "ls > out.txt", true),
		Entry("a subshell", "(ls)", true),
		Entry("a background job", "ls &", true),
		Entry("a separator inside quotes", `echo "a && ls"`, false),
	)
})

var _ = Describe("SubstituteLiterals", func() {
	DescribeTable("rewrites a leading literal assignment only when it is faithful",
		func(command string, expected string, ok bool) {
			rewritten, rewrittenOK := hook.SubstituteLiterals(command)
			Expect(rewrittenOK).To(Equal(ok))
			if ok {
				Expect(rewritten).To(Equal(expected))
			}
		},
		Entry("a literal operand", "W=/x; git -C $W log",
			"W=/x; git -C /x log", true),
		Entry("a brace form", "W=/x; git -C ${W} log",
			"W=/x; git -C /x log", true),
		Entry("a program word", "B=make; $B build", "B=make; make build", true),
		Entry("a value carrying whitespace", `W="/x push"; git -C $W log`, "", false),
		Entry("no leading assignment", "git -C $W log", "", false),
		Entry("nothing to change", "W=/x; git log", "", false),
		Entry("a single-quoted word is not expanded", "W=/x; echo '$W'", "", false),
	)
})

var _ = Describe("RuleMatches", func() {
	DescribeTable("matches one subcommand against one Bash rule",
		func(segment string, pattern string, matches bool) {
			Expect(hook.RuleMatches(segment, pattern)).To(Equal(matches))
		},
		Entry("a prefix rule", "make build foo", "make build:*", true),
		Entry("the prefix alone", "make build", "make build:*", true),
		Entry("a prefix rule does not match a longer word", "make builder", "make build:*", false),
		Entry("an exact rule", "git status", "git status", true),
		Entry("an exact rule mismatch", "git status -s", "git status", false),
		Entry("a wildcard rule", "git status -s", "git status*", true),
	)
})

var _ = Describe("FnmatchCase", func() {
	It("lets a star cross a separator, unlike path.Match", func() {
		Expect(hook.FnmatchCase("a/b", "a*")).To(BeTrue())
	})

	It("matches a character class", func() {
		Expect(hook.FnmatchCase("a1", "a[0-9]")).To(BeTrue())
		Expect(hook.FnmatchCase("ax", "a[0-9]")).To(BeFalse())
	})

	It("supports a negated class", func() {
		Expect(hook.FnmatchCase("ax", "a[!0-9]")).To(BeTrue())
	})

	It("matches a single character with ?", func() {
		Expect(hook.FnmatchCase("ab", "a?")).To(BeTrue())
	})
})

var _ = Describe("LoadRules", func() {
	It("merges allow, ask and deny rules from the user settings", func() {
		blockedLogHome(func(home string) {
			Expect(os.MkdirAll(home+"/.claude", 0o700)).To(Succeed())
			Expect(os.WriteFile(home+"/.claude/settings.json", []byte(`{
  "permissions": {
    "allow": ["Bash(make build:*)", "Bash(git status)", "Read(/x)"],
    "ask": ["Bash(git push)"],
    "deny": ["Bash(rm -rf:*)"]
  }
}`), 0o600)).To(Succeed())

			rules, err := hook.LoadRules(home)
			Expect(err).NotTo(HaveOccurred())
			Expect(rules.Allow).To(Equal([]string{"make build:*", "git status"}))
			Expect(rules.Gated).To(Equal([]string{"git push", "rm -rf:*"}))
		})
	})

	It("returns an error rather than an empty set for an unreadable file", func() {
		blockedLogHome(func(home string) {
			Expect(os.MkdirAll(home+"/.claude", 0o700)).To(Succeed())
			Expect(os.WriteFile(home+"/.claude/settings.json", []byte("not json"), 0o600)).
				To(Succeed())
			_, err := hook.LoadRules(home)
			Expect(err).To(HaveOccurred())
		})
	})

	It("returns an empty set when no settings file exists", func() {
		blockedLogHome(func(home string) {
			rules, err := hook.LoadRules(home)
			Expect(err).NotTo(HaveOccurred())
			Expect(rules.Allow).To(BeEmpty())
			Expect(rules.Gated).To(BeEmpty())
		})
	})
})

var _ = Describe("GitShapeAllows", func() {
	root := tempRepo(map[string]string{})
	roots := []string{root}
	inside := root

	DescribeTable("approves only read-only, static, under-root invocations",
		func(values []string, cwd string, allowed bool) {
			Expect(hook.GitShapeAllows(values, cwd, roots)).To(Equal(allowed))
		},
		Entry("status under the root", []string{"status"}, inside, true),
		Entry("log under the root", []string{"log", "--oneline", "-5"}, inside, true),
		Entry("a -C operand under the root", []string{"-C", inside, "log"}, "/tmp", true),
		Entry("a -C operand outside the root", []string{"-C", "/tmp", "log"}, "/tmp", false),
		Entry("a cwd outside the root", []string{"status"}, "/tmp", false),
		Entry("a writing subcommand", []string{"push"}, inside, false),
		Entry("the -c option", []string{"log", "-c", "core.pager=less"}, inside, false),
		Entry("an --output option", []string{"log", "--output=/tmp/x"}, inside, false),
		Entry("a unique prefix of a bad long option", []string{"log", "--out=/tmp/x"}, inside, false),
		Entry("a bundled short flag carrying O", []string{"log", "-iOcmd"}, inside, false),
		Entry("tag list mode", []string{"tag", "--list"}, inside, true),
		Entry("tag with a value flag", []string{"tag", "--contains", "main"}, inside, true),
		Entry("tag without a list flag", []string{"tag"}, inside, false),
		Entry("a tag abbreviation", []string{"tag", "--del"}, inside, false),
		Entry("worktree list", []string{"worktree", "list", "--porcelain"}, inside, true),
		Entry("worktree add", []string{"worktree", "add", "x"}, inside, false),
		Entry("ls-remote with a URL operand",
			[]string{"ls-remote", "https://example.com/x"}, inside, false),
		Entry("a pathspec after --", []string{"diff", "--", "a:b"}, inside, true),
	)
})

var _ = Describe("GitMergeAllows", func() {
	root := tempRepo(map[string]string{})
	roots := []string{root}

	DescribeTable("approves only a remote-tracking merge under the roots",
		func(values []string, allowed bool) {
			Expect(hook.GitMergeAllows(values, roots)).To(Equal(allowed))
		},
		Entry("origin/master", []string{"-C", root, "merge", "origin/master"}, true),
		Entry("a bare branch name", []string{"-C", root, "merge", "master"}, false),
		Entry("no source", []string{"-C", root, "merge"}, false),
		Entry("two sources", []string{"-C", root, "merge", "origin/a", "origin/b"}, false),
		Entry("--squash", []string{"-C", root, "merge", "--squash", "origin/master"}, false),
		Entry("--abort", []string{"-C", root, "merge", "--abort"}, false),
		Entry("a strategy", []string{"-C", root, "merge", "-s", "ours", "origin/master"}, false),
		Entry("a traversal source", []string{"-C", root, "merge", "origin/../x"}, false),
		Entry("a directory outside the roots", []string{"-C", "/tmp", "merge", "origin/master"}, false),
		Entry("no -C operand", []string{"merge", "origin/master"}, false),
	)
})

var _ = Describe("CompoundTreeAllows", func() {
	rules := hook.CompoundRules{
		Allow: []string{"pwd", "ls", "make build:*"},
		Gated: []string{"ls -R"},
	}
	cwd := tempRepo(map[string]string{})
	roots := []string{cwd}

	DescribeTable("allows only a fully modelled tree whose every call matches a rule",
		func(command string, allowed bool) {
			Expect(hook.CompoundTreeAllows(command, rules, cwd, roots)).To(Equal(allowed))
		},
		Entry("one allowlisted call", "pwd", true),
		Entry("two allowlisted calls", "pwd && ls", true),
		Entry("a word-list for loop", "for f in a b; do ls; done", true),
		Entry("a read-only git call through the shape guard", "git status", true),
		Entry("a call matching no rule", "curl example.com", false),
		Entry("a gated call", "ls -R", false),
		Entry("a wrapper program with no shape", "git push", false),
		Entry("a while loop is unbounded", "while true; do ls; done", false),
		Entry("an assignment prefix", "PATH=/evil ls", false),
		Entry("a command substitution", "ls $(date)", false),
		Entry("a shell wrapper", "bash -c 'ls'", false),
		Entry("a redirect to a file", "ls > /tmp/x", false),
		Entry("a descriptor duplication", "ls 2>&1", true),
		Entry("a redirect to /dev/null", "ls > /dev/null", true),
		Entry("a background job", "ls &", false),
		Entry("an unparsable command", "ls &&", false),
	)
})

var _ = Describe("CompoundBashGuard", func() {
	rules := hook.CompoundRules{
		Allow: []string{"pwd", "ls", "make build:*"},
		Gated: []string{"ls -R"},
	}
	guard := hook.NewCompoundBashGuardWithRules(
		func(string) (hook.CompoundRules, error) { return rules, nil },
	)

	DescribeTable("allows a compound only when every part is already permitted",
		func(command string, allow bool) {
			input := bashInput(command)
			input.HookEventName = hook.EventPreToolUse
			Expect(guard.Evaluate(input).Allow).To(Equal(allow))
		},
		Entry("two allowlisted parts", "pwd && ls", true),
		Entry("a prefix rule", "make build && pwd", true),
		Entry("a part matching no rule", "pwd && curl example.com", false),
		Entry("a gated part", "pwd && ls -R", false),
		Entry("a substitution", "pwd && ls $(date)", false),
		Entry("nine parts", "pwd && pwd && pwd && pwd && pwd && pwd && pwd && pwd && pwd", false),
	)

	It("makes no decision for a single command", func() {
		Expect(guard.Evaluate(bashInput("pwd")).Allow).To(BeFalse())
	})

	It("names the script it replaces", func() {
		Expect(guard.Name()).To(Equal("allow-compound-bash.py"))
	})
})

// Regression tests for the five bypasses the first review round found.
var _ = Describe("Review regressions", func() {
	It("refuses an if body, not just its condition", func() {
		rules := hook.CompoundRules{Allow: []string{"pwd", "ls"}}
		Expect(hook.CompoundTreeAllows(
			"if pwd; then curl example.com; fi", rules, "/tmp", nil,
		)).To(BeFalse())
	})

	It("refuses a shape-guarded call carrying an expansion", func() {
		rules := hook.CompoundRules{Allow: []string{"pwd"}}
		guard := hook.NewCompoundBashGuardWithRules(
			func(string) (hook.CompoundRules, error) { return rules, nil },
		)
		input := bashInput("pwd && git log $X")
		input.HookEventName = hook.EventPreToolUse
		Expect(guard.Evaluate(input).Allow).To(BeFalse())
	})

	It("unwraps a shell wrapper split on any whitespace run", func() {
		guard := hook.NewCdGitChainGuard()
		Expect(guard.Evaluate(bashInput("bash  -c 'cd /x && git status'")).Deny).To(BeTrue())
	})

	It("refuses a revision expression as a branch switch", func() {
		repo := tempGitRepo(map[string]string{}, "master")
		input := bashInput("git checkout origin/master")
		input.Cwd = repo
		Expect(hook.NewBranchSwitchGuard().Evaluate(input).Deny).To(BeTrue())
	})

	It("reads a quoted repo, so the do-not-merge gate still fires", func() {
		guard := hook.NewMergeLabelGuardWithRunner(
			func(...string) (string, error) { return "do-not-merge\n", nil },
		)
		Expect(guard.Evaluate(bashInput(`gh pr merge 42 --repo "bborbe/x"`)).Deny).To(BeTrue())
		Expect(guard.Evaluate(bashInput(`gh -R "bborbe/x" pr merge 42`)).Deny).To(BeTrue())
	})

	It("refuses a compound whose cd changes what a later operand resolves against", func() {
		rules := hook.CompoundRules{Allow: []string{"cd:*"}}
		guard := hook.NewCompoundBashGuardWithRules(
			func(string) (hook.CompoundRules, error) { return rules, nil },
		)
		input := bashInput("cd /tmp && sed -n p ../Users/bborbe/.ssh/id_rsa")
		input.HookEventName = hook.EventPreToolUse
		Expect(guard.Evaluate(input).Allow).To(BeFalse())
	})

	It("makes no merge-allow decision when anything else is in the command", func() {
		guard := hook.NewMergeAllowGuardWithRunner(
			func(...string) (string, error) {
				return `{"reviewDecision":"APPROVED","mergeable":"MERGEABLE"}`, nil
			},
		)
		for _, command := range []string{
			"rm -rf ~ # gh -R bborbe/x pr merge 42",
			"gh -R bborbe/x pr merge 42; rm -rf ~",
			"gh -R bborbe/x pr merge 42 -R evil/repo",
			"gh -R bborbe/x pr merge 42 $(rm -rf ~)",
			"gh -R bborbe/x pr merge 42 > ~/.zshrc",
		} {
			Expect(guard.Evaluate(bashInput(command))).To(Equal(hook.Decision{}))
		}
	})

	It("never allows a whole call just because one segment is a clean push", func() {
		repo := tempGitRepo(map[string]string{}, "master")
		input := bashInput("git push origin fix/x; gh pr merge 42 --admin")
		input.Cwd = repo
		Expect(hook.NewPushGateGuard().Evaluate(input).Allow).To(BeFalse())
	})

	It("does not read a cd out of a commit message when resolving the repo", func() {
		repo := tempGitRepo(map[string]string{}, "master")
		input := bashInput(`git commit -m "fix cd /tmp bug" && git checkout -b feat/x`)
		input.Cwd = repo
		Expect(hook.NewBranchSwitchGuard().Evaluate(input).Deny).To(BeTrue())
	})

	It("reads the PR number when a flag sits between merge and the number", func() {
		var seen []string
		guard := hook.NewMergeLabelGuardWithRunner(
			func(args ...string) (string, error) {
				seen = args
				return "do-not-merge\n", nil
			},
		)
		Expect(guard.Evaluate(bashInput("gh pr merge --repo o/r 42")).Deny).To(BeTrue())
		Expect(seen).To(ContainElement("42"))
	})
})

var _ = Describe("SedFilterAllows", func() {
	root := tempRepo(map[string]string{"a.txt": "x\n"})
	roots := []string{root}

	DescribeTable("approves only a static filter over readable operands",
		func(values []string, cwd string, allowed bool) {
			Expect(hook.SedFilterAllows(values, cwd, roots)).To(Equal(allowed))
		},
		Entry("a substitution", []string{"s/a/b/", root + "/a.txt"}, root, true),
		Entry("a substitution with flags", []string{"s/a/b/g", root + "/a.txt"}, root, true),
		Entry("an escaped delimiter", []string{"s/a\\/b/c/", root + "/a.txt"}, root, true),
		Entry("a print selector", []string{"-n", "5p", root + "/a.txt"}, root, true),
		Entry("a quit selector", []string{"1q", root + "/a.txt"}, root, true),
		Entry("the -e form", []string{"-e", "s/a/b/", root + "/a.txt"}, root, true),
		Entry("a write command", []string{"w /tmp/out", root + "/a.txt"}, root, false),
		Entry("an unknown flag", []string{"-i", "s/a/b/", root + "/a.txt"}, root, false),
		Entry("an operand outside the roots", []string{"s/a/b/", "/etc/passwd"}, root, false),
		Entry("an empty script", []string{"", root + "/a.txt"}, root, false),
	)
})

var _ = Describe("gh shape guards", func() {
	adminFlag := "--" + "admin"

	DescribeTable("approves only the documented merge shape",
		func(values []string, allowed bool) {
			Expect(hook.GhMergeAllows(values)).To(Equal(allowed))
		},
		Entry("the merge shape", []string{"-R", "bborbe/x", "pr", "merge", "12", "--merge"}, true),
		Entry("with --delete-branch",
			[]string{"-R", "bborbe/x", "pr", "merge", "12", "--merge", "-d"}, true),
		Entry("the admin flag", []string{
			"-R", "bborbe/x", "pr", "merge", "12", "--merge", adminFlag,
		}, false),
		Entry("--squash", []string{"-R", "bborbe/x", "pr", "merge", "12", "--squash"}, false),
		Entry("a foreign repo", []string{"-R", "other/x", "pr", "merge", "12", "--merge"}, false),
		Entry("a non-numeric PR", []string{"-R", "bborbe/x", "pr", "merge", "x", "--merge"}, false),
		Entry("no --merge", []string{"-R", "bborbe/x", "pr", "merge", "12"}, false),
	)

	DescribeTable("approves only the documented create shape",
		func(values []string, allowed bool) {
			Expect(hook.GhCreateAllows(values)).To(Equal(allowed))
		},
		Entry("a titled draft",
			[]string{"-R", "bborbe/x", "pr", "create", "--draft", "--title", "t"}, true),
		Entry("a body file in /tmp",
			[]string{"-R", "bborbe/x", "pr", "create", "--body-file", "/tmp/b.md"}, true),
		Entry("a body file outside /tmp",
			[]string{"-R", "bborbe/x", "pr", "create", "--body-file", "/etc/passwd"}, false),
		Entry("a fork head",
			[]string{"-R", "bborbe/x", "pr", "create", "--head", "other:main"}, false),
		Entry("a reviewer flag",
			[]string{"-R", "bborbe/x", "pr", "create", "--reviewer", "someone"}, false),
		Entry("a foreign repo",
			[]string{"-R", "other/x", "pr", "create", "--title", "t"}, false),
	)

	DescribeTable("approves a read-only PR query for any owner",
		func(values []string, allowed bool) {
			Expect(hook.GhReadAllows(values)).To(Equal(allowed))
		},
		Entry("a view with a json field",
			[]string{"-R", "any/x", "pr", "view", "12", "--json", "state"}, true),
		Entry("a list with a limit",
			[]string{"pr", "list", "--limit", "5"}, true),
		Entry("checks", []string{"pr", "checks", "12"}, true),
		Entry("--web", []string{"pr", "view", "12", "--web"}, false),
		Entry("--watch", []string{"pr", "view", "12", "--watch"}, false),
		Entry("a traversal repo", []string{"-R", "../x", "pr", "view"}, false),
		Entry("a leading-dash repo part", []string{"-R", "a/-b", "pr", "view"}, false),
	)

	It("accepts only a literal /tmp target", func() {
		Expect(hook.TmpTarget("/tmp/x.md")).To(BeTrue())
		Expect(hook.TmpTarget("/etc/passwd")).To(BeFalse())
		Expect(hook.TmpTarget("$HOME/x")).To(BeFalse())
		Expect(hook.TmpTarget("x.md")).To(BeFalse())
	})
})
