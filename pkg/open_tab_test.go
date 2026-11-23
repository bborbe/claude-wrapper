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

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fakeScriptRunner records each argv and answers from a map keyed by the script
// path (argv[1]).
type fakeScriptRunner struct {
	results map[string]ScriptResult
	errs    map[string]error
	calls   [][]string
	stdin   []string
}

func (f *fakeScriptRunner) RunScript(
	_ context.Context,
	name string,
	args ...string,
) (ScriptResult, error) {
	argv := append([]string{name}, args...)
	f.calls = append(f.calls, argv)
	key := ""
	if len(argv) > 1 {
		key = argv[1]
	}
	if err, ok := f.errs[key]; ok {
		return f.results[key], err
	}
	return f.results[key], nil
}

// RunScriptWithStdin records the stdin it was handed, then answers from the map.
func (f *fakeScriptRunner) RunScriptWithStdin(
	_ context.Context,
	stdin string,
	name string,
	args ...string,
) (ScriptResult, error) {
	f.stdin = append(f.stdin, stdin)
	return f.RunScript(context.Background(), name, args...)
}

// openScripts answers manager-launcher and topic-spawn successfully.
func openScripts() *fakeScriptRunner {
	return &fakeScriptRunner{results: map[string]ScriptResult{
		"/scripts/manager-launcher.py": {Stdout: "/scripts/cc-mgr\tmanager-default\n"},
		"/scripts/topic-spawn.py":      {Stdout: "open-ok: T\n"},
	}}
}

func newOpenTabTestOpener(
	now libtime.CurrentDateTimeGetter,
	tabs TabClient,
	registry SessionRegistry,
	caps SpawnCaps,
	ledger LedgerReader,
	scripts ScriptRunner,
) *openTabOpener {
	return &openTabOpener{
		tabs:                  tabs,
		registry:              registry,
		vaults:                &fakeVaults{},
		caps:                  caps,
		ledger:                ledger,
		scripts:               scripts,
		now:                   now,
		managerLauncherScript: "/scripts/manager-launcher.py",
		topicSpawnScript:      "/scripts/topic-spawn.py",
		exitRecord:            "/scripts/session-exit-record.py",
		sleep:                 func(time.Duration) {},
	}
}

var _ = ginkgo.Describe("openTabOpener", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("AC7 opens a goal create in window 14 with orange and both start commands", func() {
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		tabs.onSpawn = func() { registry.names["G Manager"] = []string{"sess-g"} }
		ledger := &fakeLedger{}
		scripts := openScripts()
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, nil, ledger, scripts)

		result, err := opener.Open(ctx, OpenTabRequest{
			Kind:     OpenKindGoal,
			Role:     RoleManager,
			Subject:  "G",
			PagePath: "/vault/24 Goals/G.md",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
			WindowID: 14,
			Chip:     "orange",
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(1))
		spawn := tabs.spawned[0]
		gomega.Expect(spawn.WindowID).NotTo(gomega.BeNil())
		gomega.Expect(*spawn.WindowID).To(gomega.Equal(14))
		gomega.Expect(spawn.TabName).To(gomega.Equal("G Manager"))
		gomega.Expect(spawn.Prompt).To(gomega.Equal("/color orange"))
		gomega.Expect(spawn.Launcher).To(gomega.Equal("/scripts/cc-mgr"))
		gomega.Expect(spawn.Resume).To(gomega.Equal(""))
		gomega.Expect(spawn.ResetSessionEnv).To(gomega.BeTrue())
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{
			`/vault-cli:work-on-goal "G"`,
			`/supervisor:goal-loop "G"`,
		}))
		gomega.Expect(scripts.calls).To(gomega.ContainElement([]string{
			"python3", "/scripts/manager-launcher.py", "/scripts/mgr",
			"/vault/24 Goals/G.md", "--default-source", "manager-default",
		}))
		gomega.Expect(result.SessionID).To(gomega.Equal("sess-g"))
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posts[0].ModeSource).To(gomega.Equal(OpenModeSource))
		gomega.Expect(ledger.posts[0].Mode).To(gomega.Equal(SpawnModeInteractive))
		gomega.Expect(ledger.posts[0].PaneID).To(gomega.Equal("pane-1"))
		gomega.Expect(ledger.posts[0].ResumedFrom).To(gomega.Equal(""))
	})

	ginkgo.It("AC7 resumes a goal with the recorded id and re-arms its loop", func() {
		const id = "1b6a7c3e-0000-4000-8000-000000000000"
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		ledger := &fakeLedger{}
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, nil, ledger, openScripts())

		result, err := opener.Open(ctx, OpenTabRequest{
			Kind:     OpenKindGoal,
			Role:     RoleManager,
			Subject:  "G",
			PagePath: "/vault/24 Goals/G.md",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
			WindowID: 14,
			Chip:     "orange",
			Resume:   id,
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(1))
		gomega.Expect(tabs.spawned[0].Resume).To(gomega.Equal(id))
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{
			`/vault-cli:work-on-goal "G"`,
			`/supervisor:goal-loop "G"`,
		}))
		gomega.Expect(result.SessionID).To(gomega.Equal(id))
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posts[0].ResumedFrom).To(gomega.Equal(id))
	})

	ginkgo.It("AC7 runs the topic ceiling on create and sends only the goal loop", func() {
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		tabs.onSpawn = func() { registry.names["T Manager"] = []string{"sess-t"} }
		scripts := openScripts()
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, nil, &fakeLedger{}, scripts)

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:     OpenKindTopic,
			Role:     RoleManager,
			Subject:  "T",
			PagePath: "/vault/23 Topics/T.md",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
			WindowID: 14,
			Chip:     "orange",
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(scripts.calls).To(gomega.ContainElement([]string{
			"python3", "/scripts/topic-spawn.py",
			"--vault", "/vault", "--topic", "T", "--topic-open", "--hand-run",
		}))
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{`/supervisor:goal-loop "T"`}))
	})

	ginkgo.It("AC7 does not run the topic ceiling on resume", func() {
		const id = "1b6a7c3e-0000-4000-8000-000000000001"
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		scripts := openScripts()
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, nil, &fakeLedger{}, scripts)

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:     OpenKindTopic,
			Role:     RoleManager,
			Subject:  "T",
			PagePath: "/vault/23 Topics/T.md",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
			WindowID: 14,
			Chip:     "orange",
			Resume:   id,
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(scripts.calls).To(gomega.HaveLen(1))
		gomega.Expect(scripts.calls[0][1]).To(gomega.Equal("/scripts/manager-launcher.py"))
	})

	ginkgo.It("AC7 opens a human create in window 12 on the worker launcher", func() {
		claudeScript, _ := writeLauncherPair()
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		tabs.onSpawn = func() { registry.names["Task"] = []string{"sess-task"} }
		ledger := &fakeLedger{}
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, nil, ledger, openScripts())

		result, err := opener.Open(ctx, OpenTabRequest{
			Kind:     OpenKindTask,
			Role:     RoleHuman,
			Subject:  "Task",
			PagePath: "/vault/25 Tasks/Task.md",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ClaudeScript: claudeScript,
			},
			WindowID: 12,
			Chip:     "cyan",
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(1))
		spawn := tabs.spawned[0]
		gomega.Expect(*spawn.WindowID).To(gomega.Equal(12))
		gomega.Expect(spawn.TabName).To(gomega.Equal("Task"))
		gomega.Expect(spawn.Prompt).To(gomega.Equal("/color cyan"))
		gomega.Expect(spawn.Launcher).To(gomega.Equal(claudeScript))
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{`/vault-cli:work-on-task "Task"`}))
		gomega.Expect(result.SessionID).To(gomega.Equal("sess-task"))
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posts[0].ModeSource).To(gomega.Equal(OpenModeSource))
		gomega.Expect(ledger.posts[0].PaneID).To(gomega.Equal("pane-1"))
	})

	ginkgo.It("AC7 prefixes a human work command with the first step", func() {
		claudeScript, _ := writeLauncherPair()
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		tabs.onSpawn = func() { registry.names["Task"] = []string{"sess-task"} }
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, nil, &fakeLedger{}, openScripts())

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:      OpenKindTask,
			Role:      RoleHuman,
			Subject:   "Task",
			Vault:     Vault{Name: "private-personal", Path: "/vault", ClaudeScript: claudeScript},
			WindowID:  12,
			Chip:      "cyan",
			FirstStep: "X",
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{
			"X\n\n" + `/vault-cli:work-on-task "Task"`,
		}))
	})

	ginkgo.It("AC7 resumes a human with the recorded id and the same work command", func() {
		const id = "1b6a7c3e-0000-4000-8000-000000000002"
		claudeScript, _ := writeLauncherPair()
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		ledger := &fakeLedger{}
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, nil, ledger, openScripts())

		result, err := opener.Open(ctx, OpenTabRequest{
			Kind:     OpenKindTask,
			Role:     RoleHuman,
			Subject:  "Task",
			Vault:    Vault{Name: "private-personal", Path: "/vault", ClaudeScript: claudeScript},
			WindowID: 12,
			Chip:     "cyan",
			Resume:   id,
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(tabs.spawned[0].Resume).To(gomega.Equal(id))
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{`/vault-cli:work-on-task "Task"`}))
		gomega.Expect(result.SessionID).To(gomega.Equal(id))
		gomega.Expect(ledger.posts[0].ResumedFrom).To(gomega.Equal(id))
	})

	ginkgo.It("AC19 passes a refusing manager-launcher.py line through and spawns nothing", func() {
		tabs := &fakeTabClient{}
		caps := &fakeSpawnCaps{}
		scripts := &fakeScriptRunner{results: map[string]ScriptResult{
			"/scripts/manager-launcher.py": {ExitCode: 1, Stderr: "unsafe launcher: x\n"},
		}}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, &fakeRegistry{names: map[string][]string{}}, caps,
			&fakeLedger{}, scripts,
		)

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:    OpenKindGoal,
			Role:    RoleManager,
			Subject: "G",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
		})

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.Equal("unsafe launcher: x"))
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
		gomega.Expect(caps.workerCalls).To(gomega.BeEmpty())
	})

	ginkgo.It("AC19 passes a cap refusal through and spawns nothing", func() {
		tabs := &fakeTabClient{}
		caps := &fakeSpawnCaps{workerErr: stderrors.New("the worker fleet is FULL")}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, &fakeRegistry{names: map[string][]string{}}, caps,
			&fakeLedger{}, openScripts(),
		)

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:    OpenKindTask,
			Role:    RoleHuman,
			Subject: "Task",
			Vault:   Vault{Name: "private-personal", Path: "/vault", ClaudeScript: "/scripts/cc-private"},
		})

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.Equal("the worker fleet is FULL"))
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("checks the manager cap once for a goal row", func() {
		tabs := &fakeTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		tabs.onSpawn = func() { registry.names["G Manager"] = []string{"sess-g"} }
		caps := &fakeSpawnCaps{}
		opener := newOpenTabTestOpener(steppingClock(), tabs, registry, caps, &fakeLedger{}, openScripts())

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:    OpenKindGoal,
			Role:    RoleManager,
			Subject: "G",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(caps.managerCalls).To(gomega.HaveLen(1))
	})

	ginkgo.It("AC19 refuses a resume of a completed task and spawns nothing", func() {
		const id = "1b6a7c3e-0000-4000-8000-000000000003"
		tabs := &fakeTabClient{}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, &fakeRegistry{names: map[string][]string{}}, nil,
			&fakeLedger{}, openScripts(),
		)

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:       OpenKindTask,
			Role:       RoleHuman,
			Subject:    "Task",
			Vault:      Vault{Name: "private-personal", Path: "/vault"},
			Resume:     id,
			TaskStatus: "completed",
		})

		gomega.Expect(stderrors.Is(err, ErrTaskTerminal)).To(gomega.BeTrue())
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("AC19 passes a refusing topic-spawn.py second line through and spawns nothing", func() {
		tabs := &fakeTabClient{}
		scripts := &fakeScriptRunner{results: map[string]ScriptResult{
			"/scripts/manager-launcher.py": {Stdout: "/scripts/cc-mgr\tmanager-default\n"},
			"/scripts/topic-spawn.py":      {ExitCode: 1, Stdout: "topic-unflagged\nT is not flagged.\n"},
		}}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, &fakeRegistry{names: map[string][]string{}}, nil,
			&fakeLedger{}, scripts,
		)

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:    OpenKindTopic,
			Role:    RoleManager,
			Subject: "T",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
		})

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.Equal("T is not flagged."))
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("reports an unsent start command as a note and still files the ledger row", func() {
		tabs := &notReadyTabClient{}
		registry := &fakeRegistry{names: map[string][]string{}}
		tabs.onSpawn = func() { registry.names["G Manager"] = []string{"sess-g"} }
		ledger := &fakeLedger{}
		opener := newOpenTabTestOpener(
			steppingClockEvery(30*time.Second), tabs, registry, nil, ledger, openScripts(),
		)

		result, err := opener.Open(ctx, OpenTabRequest{
			Kind:    OpenKindGoal,
			Role:    RoleManager,
			Subject: "G",
			Vault: Vault{
				Name: "private-personal", Path: "/vault", ManagerLauncher: "/scripts/mgr",
			},
		})

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.Note).To(gomega.ContainSubstring("not sent"))
		gomega.Expect(tabs.texts).To(gomega.BeEmpty())
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
	})

	ginkgo.It("refuses an agent role with ErrAgentTab and spawns nothing", func() {
		tabs := &fakeTabClient{}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, &fakeRegistry{names: map[string][]string{}}, nil,
			&fakeLedger{}, openScripts(),
		)

		_, err := opener.Open(ctx, OpenTabRequest{
			Kind:    OpenKindTask,
			Role:    RoleAgent,
			Subject: "Task",
			Vault:   Vault{Name: "private-personal", Path: "/vault"},
		})

		gomega.Expect(stderrors.Is(err, ErrAgentTab)).To(gomega.BeTrue())
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("humanLauncher", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("prefers the task's own launcher", func() {
		vaultPath := openTabTempDir()
		writeTaskFile(vaultPath, "T", "launcher: /scripts/task-launcher")
		vault := Vault{
			Name: "v", Path: vaultPath,
			ClaudeScript: "/scripts/cc", ManagerLauncher: "/scripts/mgr",
		}
		launcher, _, err := humanLauncher(ctx, &vault, "T")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(launcher).To(gomega.Equal("/scripts/task-launcher"))
	})

	ginkgo.It("ignores manager_launcher and falls back to claude_script", func() {
		vault := Vault{
			Name: "v", Path: "/nonexistent/open-tab-vault",
			ClaudeScript: "/nonexistent/open-tab/cc-private", ManagerLauncher: "/scripts/mgr",
		}
		launcher, _, err := humanLauncher(ctx, &vault, "T")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(launcher).To(gomega.Equal("/nonexistent/open-tab/cc-private"))
	})

	ginkgo.It("prefers the worker default over claude_script", func() {
		vault := Vault{
			Name: "v", Path: "/nonexistent/open-tab-vault",
			ClaudeScript:   "/nonexistent/open-tab/cc-private",
			WorkerLauncher: "/scripts/cc-private-deepseek",
		}
		launcher, source, err := humanLauncher(ctx, &vault, "T")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(launcher).To(gomega.Equal("/scripts/cc-private-deepseek"))
		gomega.Expect(source).To(gomega.Equal("worker-default"))
	})

	ginkgo.It("falls back to claude_script when no sibling and no manager launcher", func() {
		vault := Vault{
			Name: "v", Path: "/nonexistent/open-tab-vault",
			ClaudeScript: "/nonexistent/open-tab/cc-private",
		}
		launcher, _, err := humanLauncher(ctx, &vault, "T")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(launcher).To(gomega.Equal("/nonexistent/open-tab/cc-private"))
	})

	ginkgo.It("uses a claude_script that is already a -claude script", func() {
		dir := openTabTempDir()
		claudeScript := filepath.Join(dir, "cc-private-claude")
		gomega.Expect(os.WriteFile(claudeScript, []byte("#!/bin/sh\n"), 0o755)).To(gomega.Succeed())
		vault := Vault{Name: "v", Path: "/nonexistent/open-tab-vault", ClaudeScript: claudeScript}
		launcher, _, err := humanLauncher(ctx, &vault, "T")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(launcher).To(gomega.Equal(claudeScript))
	})

	ginkgo.It("refuses an unsafe launcher with ErrNoLauncher", func() {
		vaultPath := openTabTempDir()
		writeTaskFile(vaultPath, "T", "launcher: cc x;rm")
		vault := Vault{Name: "v", Path: vaultPath, ClaudeScript: "/scripts/cc"}
		_, _, err := humanLauncher(ctx, &vault, "T")
		gomega.Expect(stderrors.Is(err, ErrNoLauncher)).To(gomega.BeTrue())
	})
})

// openTabTempDir creates a scratch directory for a spec.
func openTabTempDir() string {
	dir, err := os.MkdirTemp("", "open-tab-*")
	gomega.Expect(err).To(gomega.BeNil())
	return dir
}

// writeLauncherPair creates a claude script and its `-claude` sibling and
// returns both paths.
func writeLauncherPair() (claudeScript, sibling string) {
	dir := openTabTempDir()
	claudeScript = filepath.Join(dir, "cc-private")
	gomega.Expect(os.WriteFile(claudeScript, []byte("#!/bin/sh\n"), 0o755)).To(gomega.Succeed())
	sibling = claudeScript + "-claude"
	gomega.Expect(os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o755)).To(gomega.Succeed())
	return claudeScript, sibling
}

// writeTaskFile writes a task page with the given frontmatter body.
func writeTaskFile(vaultPath, title, frontmatter string) {
	folder := filepath.Join(vaultPath, "25 Tasks")
	gomega.Expect(os.MkdirAll(folder, 0o755)).To(gomega.Succeed())
	body := "---\n" + frontmatter + "\n---\n"
	gomega.Expect(os.WriteFile(filepath.Join(folder, title+".md"), []byte(body), 0o644)).
		To(gomega.Succeed())
}
