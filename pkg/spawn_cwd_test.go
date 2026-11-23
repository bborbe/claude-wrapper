// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fakeRoleMap is a RoleMapReader over a fixed map, so the resolution rules are
// tested without depending on the operator's own role map.
type fakeRoleMap struct {
	entries map[string]RoleMapEntry
	err     error
	// reads counts Read calls, so a spec can pin that a role map is consulted
	// once per tab row and never for an agent row.
	reads int
}

func (f *fakeRoleMap) Read(context.Context) (map[string]RoleMapEntry, error) {
	f.reads++
	return f.entries, f.err
}

// fakeRegistry is a SessionRegistry over a fixed name→ids map, and optionally a
// fixed live set so a spec can have several live sessions without real processes.
type fakeRegistry struct {
	names map[string][]string
	live  []RegistryEntry
	err   error
	// liveErr fails Live only, so a spec can distinguish an unreadable registry
	// from an unreadable name lookup.
	liveErr error
}

func (f *fakeRegistry) Status(context.Context, string) (*string, error) { return nil, nil }

func (f *fakeRegistry) Cwd(context.Context, string) (*string, error) { return nil, nil }

func (f *fakeRegistry) Live(context.Context) ([]RegistryEntry, error) {
	return f.live, f.liveErr
}

// Entries answers the bulk read with the same fixed set Live does.
func (f *fakeRegistry) Entries(context.Context) ([]RegistryEntry, error) { return f.live, nil }

func (f *fakeRegistry) Named(_ context.Context, name string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.names[name], nil
}

// NamesForSession inverts the same name→ids map, so a test that seeds one
// direction exercises the other without a second fixture.
func (f *fakeRegistry) NamesForSession(_ context.Context, sessionID string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	names := make([]string, 0, 2)
	for name, ids := range f.names {
		for _, id := range ids {
			if id == sessionID {
				names = append(names, name)
				break
			}
		}
	}
	return names, nil
}

var _ = ginkgo.Describe("ParseLauncherModel", func() {
	ginkgo.It("resolves a variable reference against the script's own export", func() {
		script := "export ANTHROPIC_DEFAULT_OPUS_MODEL=\"deepseek-v4-flash-max[1m]\"\n" +
			"--model \"${ANTHROPIC_DEFAULT_OPUS_MODEL}\" \\\n"
		gomega.Expect(ParseLauncherModel(script)).To(gomega.Equal("deepseek-v4-flash-max[1m]"))
	})

	ginkgo.It("returns a literal model unchanged", func() {
		gomega.Expect(ParseLauncherModel("--model claude-opus-5-5[1m]\n")).
			To(gomega.Equal("claude-opus-5-5[1m]"))
	})

	ginkgo.It("resolves an unbraced reference", func() {
		gomega.Expect(ParseLauncherModel("export M='sonnet-x'\n--model $M\n")).
			To(gomega.Equal("sonnet-x"))
	})

	ginkgo.It("returns empty when the reference has no matching export", func() {
		gomega.Expect(ParseLauncherModel("--model \"${NOPE}\"\n")).To(gomega.BeEmpty())
	})

	ginkgo.It("returns empty rather than guessing when no --model is present", func() {
		gomega.Expect(ParseLauncherModel("#!/bin/sh\nclaude\n")).To(gomega.BeEmpty())
	})

	ginkgo.It("does not match a longer flag that starts with --model", func() {
		gomega.Expect(ParseLauncherModel("--model-name foo\n")).To(gomega.BeEmpty())
	})

	ginkgo.It("does not match the real launcher's comment prose", func() {
		// Verbatim from cc-private-deepseek:30 — NO backticks. The earlier fixture
		// had them, which is why the naive scan looked safe while the real file made
		// it return "inherits" as the model.
		line := "# Pin the model. Without this, a launch that passes no --model inherits the ambient\n"
		gomega.Expect(ParseLauncherModel(line)).To(gomega.BeEmpty())
	})

	ginkgo.It("ignores a comment line above a real --model flag", func() {
		script := "# Pin the model. Without this, a launch that passes no --model inherits the ambient\n" +
			"export ANTHROPIC_DEFAULT_OPUS_MODEL=\"deepseek-v4-flash-max[1m]\"\n" +
			"--model \"${ANTHROPIC_DEFAULT_OPUS_MODEL}\" \\\n"
		gomega.Expect(ParseLauncherModel(script)).To(gomega.Equal("deepseek-v4-flash-max[1m]"))
	})
})

var _ = ginkgo.Describe("ResolveRole", func() {
	var reader *fakeRoleMap
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		reader = &fakeRoleMap{entries: map[string]RoleMapEntry{
			"Managers": {Chip: new("orange"), WindowID: new(14)},
			"Direct":   {Chip: new("cyan"), WindowID: new(12)},
		}}
	})

	ginkgo.It("refuses an empty role, which defaults to agent", func() {
		resolution, err := ResolveRole(ctx, reader, "")
		gomega.Expect(resolution).To(gomega.BeNil())
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(stderrors.Is(err, ErrAgentTab)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("Rule 2"))
	})

	ginkgo.It("resolves manager to the Managers window and chip", func() {
		resolution, err := ResolveRole(ctx, reader, RoleManager)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(resolution.Role).To(gomega.Equal(RoleManager))
		gomega.Expect(resolution.Purpose).To(gomega.Equal("Managers"))
		gomega.Expect(resolution.Chip).To(gomega.Equal("orange"))
		gomega.Expect(*resolution.WindowID).To(gomega.Equal(14))
		gomega.Expect(resolution.Resolved).To(gomega.BeTrue())
	})

	ginkgo.It("resolves human to the Direct window", func() {
		resolution, err := ResolveRole(ctx, reader, RoleHuman)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(resolution.Role).To(gomega.Equal(RoleHuman))
		gomega.Expect(resolution.Purpose).To(gomega.Equal("Direct"))
		gomega.Expect(resolution.Chip).To(gomega.Equal("cyan"))
		gomega.Expect(*resolution.WindowID).To(gomega.Equal(12))
		gomega.Expect(resolution.Resolved).To(gomega.BeTrue())
	})

	ginkgo.It("refuses an unknown role rather than defaulting", func() {
		_, err := ResolveRole(ctx, reader, "worker")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is not a role"))
	})

	ginkgo.It("returns an unresolved resolution when the role map lacks the purpose", func() {
		resolution, err := ResolveRole(ctx, &fakeRoleMap{entries: map[string]RoleMapEntry{}}, "human")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(resolution.Resolved).To(gomega.BeFalse())
		gomega.Expect(resolution.Chip).To(gomega.BeEmpty())
		gomega.Expect(resolution.WindowID).To(gomega.BeNil())
	})
})

var _ = ginkgo.Describe("ChipForRole", func() {
	ginkgo.It("returns the role chip for a non-human role", func() {
		resolution := &RoleResolution{Role: RoleAgent, Chip: "pink"}
		gomega.Expect(ChipForRole(resolution, "private-personal")).To(gomega.Equal("pink"))
	})

	ginkgo.It("returns the vault family chip for a human role", func() {
		resolution := &RoleResolution{Role: RoleHuman, Chip: "cyan"}
		gomega.Expect(ChipForRole(resolution, "private-personal")).To(gomega.Equal("cyan"))
		gomega.Expect(ChipForRole(resolution, "seibert-brogrammers")).To(gomega.Equal("green"))
	})

	ginkgo.It("falls back to the role chip for an unknown family", func() {
		resolution := &RoleResolution{Role: RoleHuman, Chip: "cyan"}
		gomega.Expect(ChipForRole(resolution, "other-vault")).To(gomega.Equal("cyan"))
	})

	ginkgo.It("returns empty for a nil resolution", func() {
		gomega.Expect(ChipForRole(nil, "private-personal")).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("TabNameFor and UniqueTabName", func() {
	ginkgo.It("prefixes the label with the glyph", func() {
		gomega.Expect(TabNameFor("go-port-drill")).To(gomega.Equal("⚙ go-port-drill"))
	})

	ginkgo.It("returns the base name when nothing holds it", func() {
		registry := &fakeRegistry{}
		gomega.Expect(UniqueTabName(context.Background(), registry, "⚙ x")).To(gomega.Equal("⚙ x"))
	})

	ginkgo.It("suffixes (2) when the base name is held", func() {
		registry := &fakeRegistry{names: map[string][]string{"⚙ x": {"s1"}}}
		gomega.Expect(UniqueTabName(context.Background(), registry, "⚙ x")).
			To(gomega.Equal("⚙ x (2)"))
	})

	ginkgo.It("suffixes (3) when both the base and (2) are held", func() {
		registry := &fakeRegistry{names: map[string][]string{
			"⚙ x":     {"s1"},
			"⚙ x (2)": {"s2"},
		}}
		gomega.Expect(UniqueTabName(context.Background(), registry, "⚙ x")).
			To(gomega.Equal("⚙ x (3)"))
	})
})

var _ = ginkgo.Describe("FindRegisteredByName", func() {
	ginkgo.It("returns the id registered under the name", func() {
		registry := &fakeRegistry{names: map[string][]string{"⚙ x (2)": {"s2"}}}
		id, err := FindRegisteredByName(context.Background(), registry, "⚙ x (2)", nil)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(id).To(gomega.Equal("s2"))
	})

	ginkgo.It("skips a holder in the exclude set, which is what makes the join sound", func() {
		registry := &fakeRegistry{names: map[string][]string{"⚙ x": {"old", "new"}}}
		id, err := FindRegisteredByName(
			context.Background(), registry, "⚙ x",
			map[string]struct{}{"old": {}},
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(id).To(gomega.Equal("new"))
	})

	ginkgo.It("returns empty when every holder is excluded", func() {
		registry := &fakeRegistry{names: map[string][]string{"⚙ x": {"old"}}}
		id, err := FindRegisteredByName(
			context.Background(), registry, "⚙ x",
			map[string]struct{}{"old": {}},
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(id).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("ResolveWorkerTarget", func() {
	var ctx context.Context
	var vaults []Vault

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		vaults = []Vault{{
			Name:           "private-personal",
			Path:           "/Users/x/Documents/Obsidian/private-personal",
			ClaudeScript:   "/scripts/cc-private-deepseek",
			WorkerLauncher: "/scripts/cc-private-deepseek",
		}}
	})

	ginkgo.It("refuses a call naming neither vault nor cwd", func() {
		_, err := ResolveWorkerTarget(ctx, vaults, "", "", "/server/cwd")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("requires `cwd` or `vault`"))
	})

	ginkgo.It("resolves a vault to its directory and launcher", func() {
		target, err := ResolveWorkerTarget(ctx, vaults, "", "private-personal", "/server/cwd")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(target.Cwd).To(gomega.Equal(vaults[0].Path))
		gomega.Expect(target.Vault).To(gomega.Equal("private-personal"))
		gomega.Expect(target.Launcher).To(gomega.Equal("/scripts/cc-private-deepseek"))
		gomega.Expect(target.Source).To(gomega.Equal("worker-default"))
	})

	ginkgo.It("refuses an unknown vault rather than borrowing another's launcher", func() {
		_, err := ResolveWorkerTarget(ctx, vaults, "", "nope", "/server/cwd")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is not configured"))
	})

	ginkgo.It("refuses a cwd outside every vault", func() {
		_, err := ResolveWorkerTarget(ctx, vaults, "/tmp/elsewhere", "", "/server/cwd")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("outside every configured vault"))
	})

	ginkgo.It("refuses a cwd that disagrees with the named vault", func() {
		_, err := ResolveWorkerTarget(
			ctx, vaults, "/tmp/elsewhere", "private-personal", "/server/cwd",
		)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is not inside vault"))
	})

	ginkgo.It("falls back to claude_script when no worker_launcher is set", func() {
		bare := []Vault{{Name: "v", Path: "/v", ClaudeScript: "/scripts/cc-v"}}
		target, err := ResolveWorkerTarget(ctx, bare, "", "v", "/server/cwd")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(target.Launcher).To(gomega.Equal("/scripts/cc-v"))
		gomega.Expect(target.Source).To(gomega.Equal("vault"))
	})

	ginkgo.It("refuses a vault with no launcher at all", func() {
		bare := []Vault{{Name: "v", Path: "/v"}}
		_, err := ResolveWorkerTarget(ctx, bare, "", "v", "/server/cwd")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("no launcher"))
	})

	ginkgo.It("refuses a launcher carrying shell metacharacters", func() {
		bad := []Vault{{Name: "v", Path: "/v", ClaudeScript: "/scripts/x; rm -rf /"}}
		_, err := ResolveWorkerTarget(ctx, bad, "", "v", "/server/cwd")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).
			To(gomega.ContainSubstring("is not a plain script name or path"))
	})
})

var _ = ginkgo.Describe("SessionRegistry.Named", func() {
	var dir string
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "registry-*")
		gomega.Expect(err).To(gomega.BeNil())
	})

	ginkgo.AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	ginkgo.It("returns the session ids registered under a name", func() {
		write := func(file, body string) {
			gomega.Expect(os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600)).
				To(gomega.BeNil())
		}
		write("1.json", `{"pid":1,"sessionId":"s1","name":"⚙ x"}`)
		write("2.json", `{"pid":2,"sessionId":"s2","name":"⚙ x"}`)
		write("3.json", `{"pid":3,"sessionId":"s3","name":"other"}`)

		ids, err := NewSessionRegistry(dir).Named(ctx, "⚙ x")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(ids).To(gomega.ConsistOf("s1", "s2"))
	})

	ginkgo.It("returns an empty slice for a name nothing holds", func() {
		ids, err := NewSessionRegistry(dir).Named(ctx, "⚙ absent")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(ids).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("TaskLauncher", func() {
	var dir string

	ginkgo.BeforeEach(func() {
		var err error
		dir, err = os.MkdirTemp("", "task-launcher-*")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(os.MkdirAll(filepath.Join(dir, "25 Tasks"), 0o755)).To(gomega.BeNil())
	})

	ginkgo.AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	writeTask := func(name, body string) {
		gomega.Expect(os.WriteFile(filepath.Join(dir, "25 Tasks", name), []byte(body), 0o600)).
			To(gomega.BeNil())
	}

	ginkgo.It("reads the launcher a task names in its frontmatter", func() {
		writeTask("A Task.md", "---\nstatus: in_progress\nlauncher: cc-private-deepseek\n---\nbody\n")
		gomega.Expect(TaskLauncher(dir, "A Task")).To(gomega.Equal("cc-private-deepseek"))
	})

	ginkgo.It("returns empty when the task names no launcher", func() {
		writeTask("A Task.md", "---\nstatus: in_progress\n---\nbody\n")
		gomega.Expect(TaskLauncher(dir, "A Task")).To(gomega.BeEmpty())
	})

	ginkgo.It("returns empty when the task file does not exist", func() {
		gomega.Expect(TaskLauncher(dir, "Missing")).To(gomega.BeEmpty())
	})

	ginkgo.It("does not read a launcher named in the body rather than the frontmatter", func() {
		writeTask("A Task.md", "---\nstatus: in_progress\n---\nlauncher: cc-private-claude\n")
		gomega.Expect(TaskLauncher(dir, "A Task")).To(gomega.BeEmpty())
	})

	ginkgo.It("resolves the task launcher's model through its own export", func() {
		launcher := filepath.Join(dir, "cc-test-deepseek")
		gomega.Expect(os.WriteFile(launcher, []byte(
			"export ANTHROPIC_DEFAULT_OPUS_MODEL=\"deepseek-v4-flash-max[1m]\"\n"+
				"# a launch that passes no --model inherits the ambient\n"+
				"claude --model \"${ANTHROPIC_DEFAULT_OPUS_MODEL}\" \"$@\"\n"), 0o600)).
			To(gomega.BeNil())
		writeTask("A Task.md", "---\nlauncher: "+launcher+"\n---\nbody\n")
		gomega.Expect(TaskLauncherModel(dir, "A Task")).To(gomega.Equal("deepseek-v4-flash-max[1m]"))
	})
})

// The resume guard runs this on the request path. An UNBOUNDED child inherits the
// MCP client's own timeout, so a hung `vault-cli` is only killed once the client
// has already given up — and the guard then reports `signal: killed`, naming the
// child as the fault while the real cause is the client's timeout.
var _ = ginkgo.Describe("vaultCLILister", func() {
	ginkgo.It("kills a hung vault-cli at its own bound and reports the kill", func() {
		dir := ginkgo.GinkgoT().TempDir()
		shim := filepath.Join(dir, "vault-cli")
		// `exec` replaces the shell, so killing the child kills the sleep too and no
		// grandchild outlives the spec.
		gomega.Expect(os.WriteFile(shim, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755)).
			To(gomega.Succeed())
		ginkgo.GinkgoT().Setenv(
			"PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		)

		lister := &vaultCLILister{timeout: 100 * time.Millisecond}
		start := time.Now()
		_, err := lister.List(context.Background())
		elapsed := time.Since(start)

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("was killed"))
		gomega.Expect(stderrors.Is(err, ErrVaultListUnreadable)).To(gomega.BeTrue())
		// The bound is what matters: the call reaches a decision on its own, well
		// before the request's own timeout could have killed it.
		gomega.Expect(elapsed).To(gomega.BeNumerically("<", 2*time.Second))
	})

	ginkgo.It("returns a readable list unchanged, never a fabricated empty one", func() {
		dir := ginkgo.GinkgoT().TempDir()
		shim := filepath.Join(dir, "vault-cli")
		gomega.Expect(os.WriteFile(
			shim,
			[]byte("#!/bin/sh\nprintf '%s' '[{\"name\":\"v\",\"path\":\"/tmp/v\"}]'\n"),
			0o755,
		)).To(gomega.Succeed())
		ginkgo.GinkgoT().Setenv(
			"PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		)

		vaults, err := (&vaultCLILister{timeout: time.Second}).List(context.Background())

		gomega.Expect(err).To(gomega.Succeed())
		gomega.Expect(vaults).To(gomega.HaveLen(1))
		gomega.Expect(vaults[0].Name).To(gomega.Equal("v"))
	})
})

var _ = ginkgo.Describe("taskText", func() {
	ginkgo.It("reads a task page from the lowercase tasks folder the older vaults use", func() {
		vault := ginkgo.GinkgoT().TempDir()
		const title = "Check Unassigned Tasks - 2026W48-mon"
		gomega.Expect(os.MkdirAll(filepath.Join(vault, "tasks"), 0o755)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(
			filepath.Join(vault, "tasks", title+".md"), []byte("body"), 0o600,
		)).To(gomega.Succeed())

		gomega.Expect(taskText(vault, title)).To(gomega.Equal("body"))
	})

	ginkgo.It("still reads the 25 Tasks and 24 Tasks folders", func() {
		vault := ginkgo.GinkgoT().TempDir()
		for _, folder := range []string{"25 Tasks", "24 Tasks"} {
			gomega.Expect(os.MkdirAll(filepath.Join(vault, folder), 0o755)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(
				filepath.Join(vault, folder, folder+".md"), []byte(folder), 0o600,
			)).To(gomega.Succeed())
			gomega.Expect(taskText(vault, folder)).To(gomega.Equal(folder))
		}
	})

	// The opener and the row lister must probe the same folders. They were two
	// hand-kept lists and they drifted: `tasks` reached the lister but not the
	// opener, so a row read from `tasks/` was refused `missing-row`.
	ginkgo.It("probes exactly the folders the row lister probes", func() {
		gomega.Expect(taskFolders).To(gomega.Equal(taskDirsFromKindDirs()))
		gomega.Expect(taskFolders).To(gomega.ContainElement("tasks"))
	})
})
