// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// indexOf is the position of the first matching element, or -1. Used instead of
// a bare ContainElement where the ORDER is the property under test: a flag that
// is present but in the wrong place reads as a pass to ContainElement.
func indexOf(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

var _ = ginkgo.Describe("buildArgs", func() {
	const (
		id      = "11111111-2222-4333-8444-555555555555"
		resumed = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	)

	ginkgo.It("mints a session id when nothing is resumed", func() {
		args := buildArgs(id, SpawnOptions{}, "/tmp/generated.json")
		gomega.Expect(args).To(gomega.ContainElement("--session-id"))
		gomega.Expect(indexOf(args, "--session-id") + 1).To(gomega.Equal(indexOf(args, id)))
		gomega.Expect(args).NotTo(gomega.ContainElement("--resume"))
	})

	// The two are mutually exclusive. Passing both leaves the CLI holding two
	// identities for one conversation, which is the failure this pins.
	ginkgo.It("resumes instead of minting an id", func() {
		args := buildArgs(id, SpawnOptions{Resume: resumed}, "/tmp/generated.json")
		gomega.Expect(args).To(gomega.ContainElement("--resume"))
		gomega.Expect(indexOf(args, "--resume") + 1).To(gomega.Equal(indexOf(args, resumed)))
		gomega.Expect(args).NotTo(gomega.ContainElement("--session-id"))
		gomega.Expect(args).NotTo(gomega.ContainElement(id))
	})

	// --mcp-config is variadic, so the extra files are only loaded when they sit
	// immediately after the generated one and before the next flag.
	ginkgo.It("loads every mcp config directly after the generated one", func() {
		args := buildArgs(id, SpawnOptions{
			MCPConfigs: []string{"/tmp/launcher.json", "/tmp/extra.json"},
		}, "/tmp/generated.json")
		at := indexOf(args, "--mcp-config")
		gomega.Expect(at).To(gomega.BeNumerically(">=", 0))
		gomega.Expect(args[at+1]).To(gomega.Equal("/tmp/generated.json"))
		gomega.Expect(args[at+2]).To(gomega.Equal("/tmp/launcher.json"))
		gomega.Expect(args[at+3]).To(gomega.Equal("/tmp/extra.json"))
	})

	ginkgo.It("keeps the permission prompt tool after the variadic mcp values", func() {
		args := buildArgs(id, SpawnOptions{MCPConfigs: []string{"/tmp/launcher.json"}}, "/tmp/generated.json")
		gomega.Expect(indexOf(args, "--permission-prompt-tool")).To(
			gomega.BeNumerically(">", indexOf(args, "--mcp-config")))
	})

	// The two options are independent axes, and a reviewer asked for a case that
	// exercises both at once: a resume spawn that also loads extra configs must
	// still emit exactly one identity and every config path, in order.
	ginkgo.It("combines resume with extra mcp configs", func() {
		args := buildArgs(id, SpawnOptions{
			Resume:     resumed,
			MCPConfigs: []string{"/tmp/launcher.json"},
		}, "/tmp/generated.json")
		gomega.Expect(args).NotTo(gomega.ContainElement("--session-id"))
		gomega.Expect(args).NotTo(gomega.ContainElement(id))
		gomega.Expect(indexOf(args, "--resume") + 1).To(gomega.Equal(indexOf(args, resumed)))
		at := indexOf(args, "--mcp-config")
		gomega.Expect(args[at+1]).To(gomega.Equal("/tmp/generated.json"))
		gomega.Expect(args[at+2]).To(gomega.Equal("/tmp/launcher.json"))
	})

	ginkgo.It("still passes the model and permission mode", func() {
		args := buildArgs(id, SpawnOptions{Model: "opus", PermissionMode: "acceptEdits"}, "/tmp/generated.json")
		gomega.Expect(indexOf(args, "--model") + 1).To(gomega.Equal(indexOf(args, "opus")))
		gomega.Expect(indexOf(args, "--permission-mode") + 1).To(gomega.Equal(indexOf(args, "acceptEdits")))
	})
})

var _ = ginkgo.Describe("childEnv", func() {
	ginkgo.It("returns the inherited environment unchanged when nothing is overridden", func() {
		inherited := []string{"PATH=/usr/bin", "HOME=/tmp/home"}
		gomega.Expect(childEnv(inherited, nil)).To(gomega.Equal(inherited))
	})

	// A duplicated key resolves to whichever copy the child reads first, which a
	// caller cannot observe — so the inherited entry must be removed, not kept.
	ginkgo.It("replaces an inherited key instead of appending a second copy", func() {
		merged := childEnv([]string{"PATH=/usr/bin", "MODE=auto"}, map[string]string{"MODE": "acceptEdits"})
		gomega.Expect(merged).To(gomega.Equal([]string{"PATH=/usr/bin", "MODE=acceptEdits"}))
	})

	ginkgo.It("appends a key the inherited environment does not carry", func() {
		merged := childEnv(
			[]string{"PATH=/usr/bin"},
			map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:8788"},
		)
		gomega.Expect(merged).To(gomega.Equal([]string{"PATH=/usr/bin", "ANTHROPIC_BASE_URL=http://127.0.0.1:8788"}))
	})

	ginkgo.It("keeps a value that contains an equals sign", func() {
		gomega.Expect(childEnv(nil, map[string]string{"TOKEN": "a=b"})).To(gomega.Equal([]string{"TOKEN=a=b"}))
	})

	ginkgo.It("emits overrides in sorted order", func() {
		gomega.Expect(childEnv(nil, map[string]string{"B": "2", "A": "1"})).To(gomega.Equal([]string{"A=1", "B=2"}))
	})

	ginkgo.It("keeps an inherited entry that is not KEY=VALUE", func() {
		gomega.Expect(childEnv([]string{"BARE"}, map[string]string{"A": "1"})).To(gomega.Equal([]string{"BARE", "A=1"}))
	})
})

var _ = ginkgo.Describe("validateEnv", func() {
	ginkgo.It("accepts a normal environment", func() {
		gomega.Expect(validateEnv(context.Background(), map[string]string{"PATH": "/usr/bin"})).To(gomega.Succeed())
	})

	ginkgo.It("accepts no environment at all", func() {
		gomega.Expect(validateEnv(context.Background(), nil)).To(gomega.Succeed())
	})

	// Refused rather than filtered: a dropped key is a value the child does not
	// have, and the child then looks exactly like one that was never given it.
	ginkgo.It("refuses an empty key", func() {
		err := validateEnv(context.Background(), map[string]string{"": "x"})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses a key containing an equals sign", func() {
		err := validateEnv(context.Background(), map[string]string{"A=B": "x"})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())
	})

	// A NUL reaches os/exec and fails at cmd.Start with EINVAL, which surfaces as
	// a 500 for what is plainly a caller error.
	ginkgo.It("refuses a value containing a NUL byte", func() {
		err := validateEnv(context.Background(), map[string]string{"A": "x\x00y"})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("validateArgv", func() {
	ginkgo.It("accepts ordinary config paths and a UUID resume", func() {
		gomega.Expect(validateArgv(context.Background(), SpawnOptions{
			MCPConfigs: []string{"/tmp/launcher.json", "relative.json"},
			Resume:     "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		})).To(gomega.Succeed())
	})

	ginkgo.It("accepts no extra configs and no resume", func() {
		gomega.Expect(validateArgv(context.Background(), SpawnOptions{})).To(gomega.Succeed())
	})

	// --mcp-config is variadic, so a flag-shaped entry is parsed as a flag rather
	// than a path. This is the whole reason the check exists: an entry such as
	// --dangerously-skip-permissions would change the child's posture silently.
	ginkgo.It("refuses an mcp config that begins with a dash", func() {
		err := validateArgv(context.Background(), SpawnOptions{
			MCPConfigs: []string{"--dangerously-skip-permissions"},
		})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses an empty mcp config entry", func() {
		err := validateArgv(context.Background(), SpawnOptions{MCPConfigs: []string{""}})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses a resume that is not a UUID", func() {
		err := validateArgv(context.Background(), SpawnOptions{Resume: "--continue"})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())
	})

	// Same failure as a NUL in an env value: os/exec rejects it at cmd.Start and
	// the caller sees a 500 for their own bad input.
	ginkgo.It("refuses an mcp config containing a NUL byte", func() {
		err := validateArgv(context.Background(), SpawnOptions{MCPConfigs: []string{"a\x00b.json"}})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("startChild", func() {
	// The claim this pins is the ORDERING, not the error: validation runs before
	// writeMCPConfig, so a refused spawn leaves no temp file behind. Nothing in
	// the returned error shows that, and a later refactor that moved the write
	// above the check would pass every other test in this file.
	ginkgo.It("leaves no mcp temp file when the env is refused", func() {
		const id = "99999999-8888-4777-8666-555555555555"
		s := &supervisor{
			ctx:       context.Background(),
			claudeBin: "/bin/true",
			now:       libtime.NewCurrentDateTime(),
		}
		_, err := s.startChild(context.Background(), id, SpawnOptions{Env: map[string]string{"": "x"}})
		gomega.Expect(stderrors.Is(err, ErrInvalidSpawnOptions)).To(gomega.BeTrue())

		left, globErr := filepath.Glob(filepath.Join(os.TempDir(), "claude-wrapper-"+id+".mcp.json"))
		gomega.Expect(globErr).NotTo(gomega.HaveOccurred())
		gomega.Expect(left).To(gomega.BeEmpty())
	})

	// The feature's core claim, asserted on a REAL process rather than on childEnv
	// in isolation: a regression that dropped the cmd.Env assignment would leave
	// every other test in this file green while the child silently stopped
	// receiving the environment its caller resolved.
	ginkgo.It("gives the child process the caller's environment", func() {
		const id = "77777777-6666-4555-8444-333333333333"
		dir := ginkgo.GinkgoT().TempDir()
		seen := filepath.Join(dir, "seen.txt")
		fake := filepath.Join(dir, "fake-claude.sh")
		// The fake ignores the argv the wrapper builds — this asserts the process
		// ENVIRONMENT, not argument handling, and a real `claude` would need a
		// model call to prove the same thing.
		script := "#!/bin/sh\nprintf '%s' \"$WRAPPER_TEST_VAR\" > '" + seen + "'\n"
		gomega.Expect(os.WriteFile(fake, []byte(script), 0o700)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			left, _ := filepath.Glob(filepath.Join(os.TempDir(), "claude-wrapper-"+id+".mcp.json"))
			for _, path := range left {
				_ = os.Remove(path)
			}
		})

		s := &supervisor{
			ctx:       context.Background(),
			claudeBin: fake,
			now:       libtime.NewCurrentDateTime(),
		}
		_, err := s.startChild(context.Background(), id, SpawnOptions{
			Cwd: dir,
			Env: map[string]string{"WRAPPER_TEST_VAR": "seen"},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() string {
			content, readErr := os.ReadFile(seen)
			if readErr != nil {
				return ""
			}
			return string(content)
		}, "5s").Should(gomega.Equal("seen"))
	})
})
