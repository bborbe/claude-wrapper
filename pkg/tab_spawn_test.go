// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fakeTabClient records every call and renders a pane that is always ready and
// always confirms a submission.
type fakeTabClient struct {
	spawned  []TabSpawnOptions
	texts    []string
	onSpawn  func()
	spawnErr error
}

func (f *fakeTabClient) Spawn(_ context.Context, options TabSpawnOptions) (string, error) {
	f.spawned = append(f.spawned, options)
	if f.onSpawn != nil {
		f.onSpawn()
	}
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	return "pane-1", nil
}

func (f *fakeTabClient) Activate(context.Context, string) error { return nil }

func (f *fakeTabClient) SendText(_ context.Context, _ string, text string) error {
	f.texts = append(f.texts, text)
	return nil
}

// Text renders the last message followed by a fresh empty composer, which is
// what a submitted message looks like in a real pane.
func (f *fakeTabClient) Text(context.Context, string) (string, error) {
	if len(f.texts) == 0 {
		return promptGlyph + " \n", nil
	}
	return promptGlyph + " \n" + f.texts[len(f.texts)-1] + "\n" + promptGlyph + " \n", nil
}

// scriptedRegistry answers each Named call from a fixed sequence, so a test can
// reproduce a registry that changes between the name choice, the snapshot and
// the poll.
type scriptedRegistry struct {
	responses [][]string
	index     int
}

func (s *scriptedRegistry) Status(context.Context, string) (*string, error) { return nil, nil }

func (s *scriptedRegistry) Cwd(context.Context, string) (*string, error) { return nil, nil }

func (s *scriptedRegistry) Live(context.Context) ([]RegistryEntry, error) { return nil, nil }

func (s *scriptedRegistry) Named(_ context.Context, _ string) ([]string, error) {
	if s.index >= len(s.responses) {
		return nil, nil
	}
	response := s.responses[s.index]
	s.index++
	return response, nil
}

// fakeVaults is a VaultLister over a fixed list.
type fakeVaults struct {
	vaults []Vault
	err    error
}

func (f *fakeVaults) List(context.Context) ([]Vault, error) { return f.vaults, f.err }

// fakeLedger records posts and serves a fixed set of rows.
type fakeLedger struct {
	posts []LedgerRow
	rows  []LedgerRow
	err   error
}

func (f *fakeLedger) Rows(context.Context) ([]LedgerRow, error) { return f.rows, f.err }

func (f *fakeLedger) Post(_ context.Context, _ string, row LedgerRow) error {
	f.posts = append(f.posts, row)
	return nil
}

// steppingClock advances one second per read, so a poll loop's deadline is
// reached without sleeping.
func steppingClock() libtime.CurrentDateTimeGetter {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		now = now.Add(time.Second)
		return libtime.DateTime(now)
	})
}

var _ = ginkgo.Describe("tabSpawner.Spawn", func() {
	var ctx context.Context
	var dir string
	var tabs *fakeTabClient
	var registry *fakeRegistry
	var ledger *fakeLedger
	var spawner TabSpawner

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "spawn-*")
		gomega.Expect(err).To(gomega.BeNil())
		tabs = &fakeTabClient{}
		registry = &fakeRegistry{names: map[string][]string{}}
		ledger = &fakeLedger{}
		spawner = &tabSpawner{
			tabs:     tabs,
			registry: registry,
			roleMap: &fakeRoleMap{entries: map[string]RoleMapEntry{
				"Agents": {Chip: new("pink"), WindowID: new(16)},
			}},
			vaults: &fakeVaults{vaults: []Vault{{
				Name:           "v1",
				Path:           dir,
				ClaudeScript:   "/scripts/cc-v1",
				WorkerLauncher: "/scripts/cc-v1",
			}}},
			ledger: ledger,
			now:    steppingClock(),
			sleep:  func(time.Duration) {},
		}
	})

	ginkgo.AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	ginkgo.It("refuses an empty prompt before opening a tab", func() {
		_, err := spawner.Spawn(ctx, TabSpawnRequest{Vault: "v1", Prompt: "   "})
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("opens one tab under the vault's launcher and files a row carrying the agent id", func() {
		registry.names["⚙ v1"] = nil
		tabs.onSpawn = func() { registry.names["⚙ v1"] = []string{"s1"} }

		result, err := spawner.Spawn(ctx, TabSpawnRequest{Vault: "v1", Prompt: "do it"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(1))
		gomega.Expect(tabs.spawned[0].Launcher).To(gomega.Equal("/scripts/cc-v1"))
		gomega.Expect(tabs.spawned[0].Cwd).To(gomega.Equal(dir))
		gomega.Expect(tabs.spawned[0].TabName).To(gomega.Equal("⚙ v1"))
		gomega.Expect(result.SessionID).To(gomega.Equal("s1"))
		gomega.Expect(result.AgentID).To(gomega.Equal("s1"))
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
		// The row must carry the agent id: an id-less record is skipped by the
		// roster, which leaves the worker unreachable by send_agent_message.
		gomega.Expect(ledger.posts[0].AgentID).To(gomega.Equal("s1"))
		gomega.Expect(ledger.posts[0].PaneID).To(gomega.Equal("pane-1"))
	})

	ginkgo.It("sends the role colour as its own submit", func() {
		tabs.onSpawn = func() { registry.names["⚙ v1"] = []string{"s1"} }
		result, err := spawner.Spawn(ctx, TabSpawnRequest{Vault: "v1", Prompt: "do it"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.texts).To(gomega.ContainElement("/color pink"))
		gomega.Expect(result.ColorApplied).To(gomega.BeTrue())
	})

	ginkgo.It("does not adopt a session that already held the tab name", func() {
		// Three reads: the name choice sees it free, the pre-spawn snapshot sees
		// the stale holder, and the poll sees the stale holder plus the new one.
		spawner = &tabSpawner{
			tabs:     tabs,
			registry: &scriptedRegistry{responses: [][]string{{}, {"old"}, {"old", "new"}}},
			roleMap:  &fakeRoleMap{entries: map[string]RoleMapEntry{}},
			vaults: &fakeVaults{vaults: []Vault{{
				Name: "v1", Path: dir, ClaudeScript: "/scripts/cc-v1",
			}}},
			ledger: ledger,
			now:    steppingClock(),
			sleep:  func(time.Duration) {},
		}

		result, err := spawner.Spawn(ctx, TabSpawnRequest{Vault: "v1", Prompt: "do it"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.SessionID).To(gomega.Equal("new"))
	})

	ginkgo.It("returns an empty identity and files no row when the tab never registers", func() {
		result, err := spawner.Spawn(ctx, TabSpawnRequest{Vault: "v1", Prompt: "do it"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.SessionID).To(gomega.BeEmpty())
		gomega.Expect(result.AgentID).To(gomega.BeEmpty())
		gomega.Expect(ledger.posts).To(gomega.BeEmpty())
	})

	ginkgo.It("suffixes a tab name that is already held", func() {
		registry.names["⚙ v1"] = []string{"old"}
		result, err := spawner.Spawn(ctx, TabSpawnRequest{Vault: "v1", Prompt: "do it"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.TabName).To(gomega.Equal("⚙ v1 (2)"))
	})
})

var _ = ginkgo.Describe("ValidateTabEnv", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() { ctx = context.Background() })

	ginkgo.It("accepts a legal name", func() {
		gomega.Expect(ValidateTabEnv(ctx, map[string]string{"ATTENTION_STORE_URL": "x"})).
			To(gomega.BeNil())
	})

	ginkgo.It("refuses a name that is not a shell variable", func() {
		err := ValidateTabEnv(ctx, map[string]string{"X=1; rm -rf / #": "x"})
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("not a legal shell variable name"))
	})

	ginkgo.It("refuses PERMISSION_MODE, which would widen the worker", func() {
		err := ValidateTabEnv(ctx, map[string]string{"PERMISSION_MODE": "bypassPermissions"})
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("PERMISSION_MODE"))
	})

	ginkgo.It("refuses the worker-mode variable", func() {
		err := ValidateTabEnv(ctx, map[string]string{"SUPERVISOR_WORKER_MODE": "headless"})
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("SUPERVISOR_WORKER_MODE"))
	})
})

// fakeSpawner records spawn requests.
type fakeSpawner struct {
	requests []TabSpawnRequest
}

func (f *fakeSpawner) Spawn(_ context.Context, request TabSpawnRequest) (*TabSpawnResult, error) {
	f.requests = append(f.requests, request)
	return &TabSpawnResult{AgentID: "s1"}, nil
}

var _ = ginkgo.Describe("RosterToolServer.spawnLocalTarget", func() {
	var ctx context.Context
	var server *RosterToolServer
	var spawner *fakeSpawner

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		spawner = &fakeSpawner{}
		server = &RosterToolServer{Spawner: spawner}
	})

	ginkgo.It("serves a local spawn with no target", func() {
		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x"}, "spawner")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.HaveLen(1))
		gomega.Expect(spawner.requests[0].SpawnerSessionID).To(gomega.Equal("spawner"))
	})

	ginkgo.It("refuses an unknown target by name", func() {
		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Target: "bogus"}, "")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses headless rather than serving it as a tab", func() {
		interactive := false
		_, err := server.spawnLocalTarget(
			ctx, spawnAgentArgs{Interactive: &interactive}, "",
		)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("headless"))
	})

	ginkgo.It("refuses resume, which the tab launcher would drop", func() {
		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Resume: true}, "")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("headless worker only"))
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a policy the tab worker would never consult", func() {
		_, err := server.spawnLocalTarget(ctx, spawnAgentArgs{Policy: "/p.json"}, "")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an env override that would widen the worker", func() {
		_, err := server.spawnLocalTarget(
			ctx, spawnAgentArgs{Env: map[string]string{"PERMISSION_MODE": "bypassPermissions"}}, "",
		)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(spawner.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses when no spawner is wired", func() {
		_, err := (&RosterToolServer{}).spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x"}, "")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("not wired"))
	})
})

var _ = ginkgo.Describe("RosterToolServer.sendToTabPane", func() {
	var ctx context.Context
	var tabs *fakeTabClient
	var ledger *fakeLedger

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		tabs = &fakeTabClient{}
		ledger = &fakeLedger{rows: []LedgerRow{{
			SessionID: "s1",
			PaneID:    "pane-1",
			Mode:      "interactive",
		}}}
	})

	ginkgo.It("types into the pane addressed by the session id", func() {
		server := &RosterToolServer{
			Tabs:   tabs,
			Roster: &fakeRoster{},
			Ledger: ledger,
		}
		handled, response, err := server.sendToTabPane(ctx, "s1", "hello")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(handled).To(gomega.BeTrue())
		gomega.Expect(tabs.texts).To(gomega.ConsistOf("hello"))
		gomega.Expect(response).To(gomega.ContainSubstring(`"sent":true`))
	})

	ginkgo.It("falls through for an id no row carries", func() {
		server := &RosterToolServer{
			Tabs:   tabs,
			Roster: &fakeRoster{},
			Ledger: ledger,
		}
		handled, _, err := server.sendToTabPane(ctx, "nope", "hello")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(handled).To(gomega.BeFalse())
		gomega.Expect(tabs.texts).To(gomega.BeEmpty())
	})

	ginkgo.It("leaves a remote worker to the A2A arm", func() {
		// A cluster row carries no pane by design. Claiming it here would answer an
		// A2A worker as unreachable before the A2A arm ever ran.
		ledger.rows = []LedgerRow{{SessionID: "s1", Mode: "cluster"}}
		server := &RosterToolServer{
			Tabs:   tabs,
			Roster: &fakeRoster{},
			Ledger: ledger,
		}
		handled, _, err := server.sendToTabPane(ctx, "s1", "hello")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(handled).To(gomega.BeFalse())
		gomega.Expect(tabs.texts).To(gomega.BeEmpty())
	})

	ginkgo.It("reports a headless worker rather than typing nowhere", func() {
		ledger.rows = []LedgerRow{{SessionID: "s1", Mode: "headless"}}
		server := &RosterToolServer{
			Tabs:   tabs,
			Roster: &fakeRoster{},
			Ledger: ledger,
		}
		handled, _, err := server.sendToTabPane(ctx, "s1", "hello")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(handled).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("no pane"))
	})
})

// fakeRoster serves a fixed roster, so the ledger-only resolution is exercised.
type fakeRoster struct{}

func (f *fakeRoster) List(context.Context) ([]AgentRow, error) { return nil, nil }

func (f *fakeRoster) Get(context.Context, string) (*AgentRow, error) { return nil, nil }

var _ = ginkgo.Describe("within", func() {
	ginkgo.It("accepts a path inside the root", func() {
		gomega.Expect(within("/a/b/c", "/a/b")).To(gomega.BeTrue())
	})

	ginkgo.It("rejects a sibling that merely shares the prefix", func() {
		gomega.Expect(within("/a/bc", "/a/b")).To(gomega.BeFalse())
	})

	ginkgo.It("resolves a symlinked path before comparing", func() {
		root, err := os.MkdirTemp("", "within-*")
		gomega.Expect(err).To(gomega.BeNil())
		defer func() { _ = os.RemoveAll(root) }()
		real := filepath.Join(root, "real")
		link := filepath.Join(root, "link")
		gomega.Expect(os.Mkdir(real, 0o755)).To(gomega.BeNil())
		gomega.Expect(os.Symlink(real, link)).To(gomega.BeNil())

		gomega.Expect(within(link, real)).To(gomega.BeTrue())
	})
})
