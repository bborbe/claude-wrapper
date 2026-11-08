// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func boolPtr(value bool) *bool { return &value }

func configMode(value string) json.RawMessage {
	return json.RawMessage(strconvQuote(value))
}

var _ = ginkgo.Describe("ResolveSpawnMode", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.DescribeTable("resolves through the node's four-step chain",
		func(interactive *bool, sources SpawnModeSources, mode string, source string) {
			resolution, err := ResolveSpawnMode(ctx, interactive, sources)
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(resolution).To(gomega.Equal(SpawnModeResolution{Mode: mode, Source: source}))
		},
		// Step 4 alone.
		ginkgo.Entry("nothing configured -> interactive default",
			nil, SpawnModeSources{}, SpawnModeInteractive, SpawnModeSourceDefault),
		// Step 3.
		ginkgo.Entry("spawn.mode headless -> headless from config",
			nil, SpawnModeSources{Config: configMode("headless")}, SpawnModeHeadless, SpawnModeSourceConfig),
		ginkgo.Entry("spawn.mode interactive -> interactive from config",
			nil, SpawnModeSources{Config: configMode("interactive")}, SpawnModeInteractive, SpawnModeSourceConfig),
		// Step 2, and its precedence over step 3.
		ginkgo.Entry("env headless -> headless from env",
			nil, SpawnModeSources{Env: "headless"}, SpawnModeHeadless, SpawnModeSourceEnv),
		ginkgo.Entry("env interactive beats spawn.mode headless",
			nil, SpawnModeSources{Env: "interactive", Config: configMode("headless")},
			SpawnModeInteractive, SpawnModeSourceEnv),
		ginkgo.Entry("env headless beats spawn.mode interactive",
			nil, SpawnModeSources{Env: "headless", Config: configMode("interactive")},
			SpawnModeHeadless, SpawnModeSourceEnv),
		// Step 1, in both directions, over both config sources.
		ginkgo.Entry("interactive:true beats env and spawn.mode headless",
			boolPtr(true), SpawnModeSources{Env: "headless", Config: configMode("headless")},
			SpawnModeInteractive, SpawnModeSourceArgument),
		ginkgo.Entry("interactive:false beats env and spawn.mode interactive",
			boolPtr(false), SpawnModeSources{Env: "interactive", Config: configMode("interactive")},
			SpawnModeHeadless, SpawnModeSourceArgument),
		ginkgo.Entry("interactive:true with nothing configured",
			boolPtr(true), SpawnModeSources{}, SpawnModeInteractive, SpawnModeSourceArgument),
		ginkgo.Entry("interactive:false with nothing configured",
			boolPtr(false), SpawnModeSources{}, SpawnModeHeadless, SpawnModeSourceArgument),
		// Unset shapes of the config value read as unset, not as invalid.
		ginkgo.Entry("spawn.mode null is unset",
			nil, SpawnModeSources{Config: json.RawMessage("null")}, SpawnModeInteractive, SpawnModeSourceDefault),
		ginkgo.Entry("spawn.mode empty string is unset",
			nil, SpawnModeSources{Config: configMode("")}, SpawnModeInteractive, SpawnModeSourceDefault),
	)

	ginkgo.DescribeTable("refuses an invalid source even when the argument would decide",
		func(interactive *bool, sources SpawnModeSources, names string) {
			_, err := ResolveSpawnMode(ctx, interactive, sources)
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring(names))
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("not a spawn mode"))
		},
		ginkgo.Entry("bad env, no argument", nil,
			SpawnModeSources{Env: "headles"}, SpawnModeEnv),
		ginkgo.Entry("bad env, explicit argument", boolPtr(true),
			SpawnModeSources{Env: "tab"}, SpawnModeEnv),
		ginkgo.Entry("bad config, explicit argument", boolPtr(false),
			SpawnModeSources{Config: configMode("cluster"), ConfigPath: "/c.json"}, `"spawn.mode" in /c.json`),
		ginkgo.Entry("non-string config", nil,
			SpawnModeSources{Config: json.RawMessage("true"), ConfigPath: "/c.json"}, `"spawn.mode" in /c.json`),
		ginkgo.Entry("bad config behind a valid env", nil,
			SpawnModeSources{Env: "headless", Config: configMode("Headless"), ConfigPath: "/c.json"}, "spawn.mode"),
	)
})

var _ = ginkgo.Describe("spawnModeSourcesReader", func() {
	var ctx context.Context
	var home string

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		home = ginkgo.GinkgoT().TempDir()
	})

	writeConfig := func(content string) {
		dir := filepath.Join(home, ".config", "claude-supervisor")
		gomega.Expect(os.MkdirAll(dir, 0o755)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600)).
			To(gomega.Succeed())
	}
	noEnv := func(string) string { return "" }

	ginkgo.It("reads spawn.mode and the env variable", func() {
		writeConfig(`{"spawn":{"mode":"headless","maxConcurrent":5}}`)
		reader := NewSpawnModeSourcesReader(home, func(name string) string {
			if name == SpawnModeEnv {
				return " interactive "
			}
			return ""
		})
		sources, err := reader.Read(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(sources.Env).To(gomega.Equal("interactive"))
		gomega.Expect(string(sources.Config)).To(gomega.Equal(`"headless"`))
		gomega.Expect(sources.ConfigPath).To(gomega.Equal(
			filepath.Join(home, ".config", "claude-supervisor", "config.json"),
		))
	})

	ginkgo.It("reads a missing file as no config", func() {
		sources, err := NewSpawnModeSourcesReader(home, noEnv).Read(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(sources.Config).To(gomega.BeEmpty())
	})

	ginkgo.It("reads an unparseable file as no config, as the node does", func() {
		writeConfig(`{not json`)
		sources, err := NewSpawnModeSourcesReader(home, noEnv).Read(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(sources.Config).To(gomega.BeEmpty())
	})

	ginkgo.It("reads a file without spawn.mode as no config", func() {
		writeConfig(`{"a2a":{"defaultCluster":"nukedev"}}`)
		sources, err := NewSpawnModeSourcesReader(home, noEnv).Read(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		resolution, err := ResolveSpawnMode(ctx, nil, sources)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(resolution.Source).To(gomega.Equal(SpawnModeSourceDefault))
	})
})

// fixedModeSources is a SpawnModeSourcesReader over fixed sources.
type fixedModeSources struct {
	sources SpawnModeSources
}

func (f fixedModeSources) Read(context.Context) (SpawnModeSources, error) {
	return f.sources, nil
}

// headlessSupervisor records the options of every headless spawn.
type headlessSupervisor struct {
	rosterSupervisor
	spawned []SpawnOptions
}

func (h *headlessSupervisor) Spawn(_ context.Context, options SpawnOptions) (*Session, error) {
	h.spawned = append(h.spawned, options)
	return &Session{ID: "headless-1", Cwd: options.Cwd, State: StateIdle}, nil
}

func (h *headlessSupervisor) Send(context.Context, string, string) error { return nil }

var _ = ginkgo.Describe("RosterToolServer.spawnLocalTarget mode resolution", func() {
	var ctx context.Context
	var spawner *fakeSpawner
	var supervisor *headlessSupervisor
	var ledger *fakeLedger
	var vaultDir string

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		spawner = &fakeSpawner{}
		supervisor = &headlessSupervisor{}
		ledger = &fakeLedger{}
		vaultDir = ginkgo.GinkgoT().TempDir()
	})

	server := func(sources SpawnModeSources) *RosterToolServer {
		return &RosterToolServer{
			Spawner:    spawner,
			Supervisor: supervisor,
			Ledger:     ledger,
			SpawnMode:  fixedModeSources{sources: sources},
			Vaults: &fakeVaults{vaults: []Vault{{
				Name: "v1", Path: vaultDir, ClaudeScript: "/scripts/cc-v1",
			}}},
		}
	}
	decode := func(response string) map[string]any {
		var body map[string]any
		gomega.Expect(json.Unmarshal([]byte(response), &body)).To(gomega.Succeed())
		return body
	}

	ginkgo.It("SC1: spawn.mode headless + no argument spawns headless, not a tab", func() {
		response, err := server(SpawnModeSources{Config: configMode("headless")}).
			spawnLocalTarget(ctx, spawnAgentArgs{Vault: "v1", Prompt: "go"}, "manager")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		// A vault-only call resolves to the vault's directory, never the daemon's cwd.
		gomega.Expect(supervisor.spawned[0].Cwd).To(gomega.Equal(vaultDir))
		body := decode(response)
		gomega.Expect(body["mode"]).To(gomega.Equal("headless"))
		gomega.Expect(body["mode_source"]).To(gomega.Equal("config"))
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posts[0].Mode).To(gomega.Equal("headless"))
		gomega.Expect(ledger.posts[0].ModeSource).To(gomega.Equal("config"))
	})

	ginkgo.It("SC1: spawn.mode interactive + no argument opens a tab", func() {
		_, err := server(SpawnModeSources{Config: configMode("interactive")}).
			spawnLocalTarget(ctx, spawnAgentArgs{Vault: "v1", Prompt: "go"}, "manager")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
		gomega.Expect(spawner.requests[0].ModeSource).To(gomega.Equal("config"))
	})

	ginkgo.It("SUPERVISOR_SPAWN_MODE beats spawn.mode", func() {
		response, err := server(SpawnModeSources{Env: "headless", Config: configMode("interactive")}).
			spawnLocalTarget(ctx, spawnAgentArgs{Cwd: vaultDir, Prompt: "go"}, "manager")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
		gomega.Expect(decode(response)["mode_source"]).To(gomega.Equal("env"))
	})

	ginkgo.It("SC2: interactive:true opens a tab under spawn.mode headless", func() {
		_, err := server(SpawnModeSources{Config: configMode("headless")}).
			spawnLocalTarget(ctx, spawnAgentArgs{
				Vault: "v1", Prompt: "go", Interactive: boolPtr(true),
			}, "manager")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
		gomega.Expect(spawner.requests[0].ModeSource).To(gomega.Equal("argument"))
	})

	ginkgo.It("SC2: interactive:false spawns headless under spawn.mode interactive", func() {
		response, err := server(SpawnModeSources{Config: configMode("interactive")}).
			spawnLocalTarget(ctx, spawnAgentArgs{
				Cwd: vaultDir, Prompt: "go", Interactive: boolPtr(false),
			}, "manager")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		gomega.Expect(decode(response)["mode_source"]).To(gomega.Equal("argument"))
	})

	ginkgo.It("falls to the interactive default when nothing is configured", func() {
		_, err := server(SpawnModeSources{}).
			spawnLocalTarget(ctx, spawnAgentArgs{Vault: "v1", Prompt: "go"}, "manager")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
		gomega.Expect(spawner.requests[0].ModeSource).To(gomega.Equal("default"))
	})

	ginkgo.It("refuses an invalid spawn.mode before any worker exists", func() {
		_, err := server(SpawnModeSources{Config: configMode("tab"), ConfigPath: "/c.json"}).
			spawnLocalTarget(ctx, spawnAgentArgs{
				Vault: "v1", Prompt: "go", Interactive: boolPtr(true),
			}, "manager")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a headless spawn with neither cwd nor vault", func() {
		_, err := server(SpawnModeSources{Config: configMode("headless")}).
			spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "go"}, "manager")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("requires `cwd` or `vault`"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("tabSpawner mode_source", func() {
	ginkgo.It("carries the request's mode source into the result and the ledger row", func() {
		result := &TabSpawnResult{SessionID: "s1", AgentID: "s1", ModeSource: "config"}
		ledger := &fakeLedger{}
		(&tabSpawner{ledger: ledger}).postLedger(context.Background(), "manager", result)
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posts[0].Mode).To(gomega.Equal("interactive"))
		gomega.Expect(ledger.posts[0].ModeSource).To(gomega.Equal("config"))
	})
})
