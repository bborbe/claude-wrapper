// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ParseLauncherMCPConfig", func() {
	ginkgo.It("returns the config the launcher passes", func() {
		script := "#!/bin/bash\nclaude --mcp-config ~/.claude/mcp-private-personal.json\n"
		home, err := os.UserHomeDir()
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(ParseLauncherMCPConfig(script)).
			To(gomega.Equal(filepath.Join(home, ".claude/mcp-private-personal.json")))
	})

	// The shape the real cc-* launchers actually write. A regex requiring a space
	// after the flag passes every fixture above and matches none of these — the
	// defect this case exists to catch, measured on cc-private-deepseek.
	ginkgo.It("accepts the equals-sign form with $HOME, which the real launchers use", func() {
		home, err := os.UserHomeDir()
		gomega.Expect(err).To(gomega.BeNil())
		script := "#!/bin/bash\n--mcp-config=\"$HOME/.claude/mcp-private-personal.json\" \\\n"
		gomega.Expect(ParseLauncherMCPConfig(script)).
			To(gomega.Equal(filepath.Join(home, ".claude/mcp-private-personal.json")))
	})

	ginkgo.It("accepts the equals-sign form unquoted", func() {
		script := "claude --mcp-config=/etc/mcp.json\n"
		gomega.Expect(ParseLauncherMCPConfig(script)).To(gomega.Equal("/etc/mcp.json"))
	})

	ginkgo.It("accepts the double-quoted form", func() {
		script := "claude --mcp-config \"/etc/mcp.json\"\n"
		gomega.Expect(ParseLauncherMCPConfig(script)).To(gomega.Equal("/etc/mcp.json"))
	})

	ginkgo.It("accepts the single-quoted form", func() {
		script := "claude --mcp-config '/etc/mcp.json'\n"
		gomega.Expect(ParseLauncherMCPConfig(script)).To(gomega.Equal("/etc/mcp.json"))
	})

	// The real cc-* launchers mention the flag in prose above the call. A naive
	// scan reads the comment as the answer, which is the trap ParseLauncherModel
	// documents for `--model`.
	ginkgo.It("ignores the flag when it appears only in a comment", func() {
		script := "#!/bin/bash\n# passes no --mcp-config of its own\nclaude --print\n"
		gomega.Expect(ParseLauncherMCPConfig(script)).To(gomega.BeEmpty())
	})

	ginkgo.It("returns empty when the launcher passes no config", func() {
		gomega.Expect(ParseLauncherMCPConfig("claude --print\n")).To(gomega.BeEmpty())
	})

	ginkgo.It("does not match a longer flag that starts with the same prefix", func() {
		gomega.Expect(ParseLauncherMCPConfig("claude --mcp-config-dir /tmp/x\n")).To(gomega.BeEmpty())
	})

	ginkgo.It("returns empty for an empty script", func() {
		gomega.Expect(ParseLauncherMCPConfig("")).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("LauncherMCPConfig", func() {
	ginkgo.It("reads the config path out of a launcher script on disk", func() {
		dir := ginkgo.GinkgoT().TempDir()
		config := filepath.Join(dir, "mcp.json")
		gomega.Expect(os.WriteFile(config, []byte(`{"mcpServers":{}}`), 0o600)).To(gomega.Succeed())
		launcher := filepath.Join(dir, "cc-test")
		gomega.Expect(os.WriteFile(launcher, []byte(
			"#!/bin/bash\nclaude --mcp-config \""+config+"\"\n"), 0o755)).To(gomega.Succeed())
		gomega.Expect(LauncherMCPConfig(launcher)).To(gomega.Equal(config))
	})

	// The path is handed to the child as --mcp-config, so a launcher naming a file
	// that no longer exists would fail every spawn — worse than the fewer servers
	// this fix restores.
	ginkgo.It("returns empty when the named config file does not exist", func() {
		dir := ginkgo.GinkgoT().TempDir()
		launcher := filepath.Join(dir, "cc-stale")
		gomega.Expect(os.WriteFile(launcher, []byte(
			"#!/bin/bash\nclaude --mcp-config \"/nonexistent/mcp.json\"\n"), 0o755)).To(gomega.Succeed())
		gomega.Expect(LauncherMCPConfig(launcher)).To(gomega.BeEmpty())
	})

	// A missing launcher is a MISSING CONFIG, not a failed spawn: the worker must
	// still open with fewer servers rather than be refused.
	ginkgo.It("returns empty for a launcher that does not exist", func() {
		gomega.Expect(LauncherMCPConfig("/nonexistent/cc-nope")).To(gomega.BeEmpty())
	})

	ginkgo.It("returns empty for a bare name rather than resolving PATH", func() {
		gomega.Expect(LauncherMCPConfig("claude")).To(gomega.BeEmpty())
	})
})
