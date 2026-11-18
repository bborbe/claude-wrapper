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
