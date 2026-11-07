// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fakeRoleMap is a RoleMapReader over a fixed map, so the resolution rules are
// tested without depending on the operator's own role map.
type fakeRoleMap struct {
	entries map[string]RoleMapEntry
	err     error
}

func (f *fakeRoleMap) Read(context.Context) (map[string]RoleMapEntry, error) {
	return f.entries, f.err
}

// fakeRegistry is a SessionRegistry over a fixed name→ids map.
type fakeRegistry struct {
	names map[string][]string
	err   error
}

func (f *fakeRegistry) Status(context.Context, string) (*string, error) { return nil, nil }

func (f *fakeRegistry) Cwd(context.Context, string) (*string, error) { return nil, nil }

func (f *fakeRegistry) Live(context.Context) ([]RegistryEntry, error) { return nil, nil }

func (f *fakeRegistry) Named(_ context.Context, name string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.names[name], nil
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
		// Verbatim from cc-private-deepseek:30. The backtick closes the flag
		// immediately, so there is no whitespace for the pattern to match on —
		// which is the only reason a naive grep over the file does not return
		// "inherits" as the model.
		line := "# Pin the model. Without this, a launch that passes no `--model` inherits the ambient\n"
		gomega.Expect(ParseLauncherModel(line)).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("ResolveRole", func() {
	var reader *fakeRoleMap
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		reader = &fakeRoleMap{entries: map[string]RoleMapEntry{
			"Agents":   {Chip: new("pink"), WindowID: new(16)},
			"Managers": {Chip: new("orange"), WindowID: new(14)},
			"Direct":   {Chip: new("cyan"), WindowID: new(12)},
		}}
	})

	ginkgo.It("defaults an empty role to agent", func() {
		resolution, err := ResolveRole(ctx, reader, "")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(resolution.Role).To(gomega.Equal(RoleAgent))
		gomega.Expect(resolution.Chip).To(gomega.Equal("pink"))
		gomega.Expect(*resolution.WindowID).To(gomega.Equal(16))
		gomega.Expect(resolution.Resolved).To(gomega.BeTrue())
	})

	ginkgo.It("refuses an unknown role rather than defaulting", func() {
		_, err := ResolveRole(ctx, reader, "worker")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is not a role"))
	})

	ginkgo.It("returns an unresolved resolution when the role map lacks the purpose", func() {
		resolution, err := ResolveRole(ctx, &fakeRoleMap{entries: map[string]RoleMapEntry{}}, "agent")
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
