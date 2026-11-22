// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const (
	openAnswerPathScript = "/scripts/answer-path-check.py"
	openTaskTestFolder   = "25 Tasks"
	openGoalTestFolder   = "24 Goals"
	openTopicTestFolder  = "23 Topics"
)

// openExecTestUUID returns a full 36-character UUID-shaped id.
func openExecTestUUID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// writeOpenPage writes a page with the given frontmatter lines.
func writeOpenPage(dir, folder, name string, frontmatter ...string) {
	gomega.Expect(os.MkdirAll(filepath.Join(dir, folder), 0o755)).To(gomega.Succeed())
	body := "---\n" + strings.Join(frontmatter, "\n") + "\n---\n"
	gomega.Expect(os.WriteFile(
		filepath.Join(dir, folder, name+".md"), []byte(body), 0o644,
	)).To(gomega.Succeed())
}

// orderLog records calls made by different fakes in one shared sequence.
type orderLog struct{ events []string }

func (o *orderLog) add(event string) { o.events = append(o.events, event) }

// logCapture records formatted log lines.
type logCapture struct{ lines []string }

func (l *logCapture) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

// fakeHeadlessOpener records every headless request.
type fakeHeadlessOpener struct {
	requests []OpenHeadlessRequest
	err      error
	block    chan struct{}
	entered  chan struct{}
	order    *orderLog
}

func (f *fakeHeadlessOpener) SpawnOpenHeadless(
	ctx context.Context,
	request OpenHeadlessRequest,
) (*Session, *OpenBind, error) {
	f.requests = append(f.requests, request)
	if f.order != nil {
		f.order.add("headless")
	}
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, nil, f.err
	}
	id := request.Resume
	if id == "" {
		id = openExecTestUUID(500)
	}
	return &Session{ID: id}, &OpenBind{Task: request.Task, SessionID: id, Action: "stamped"}, nil
}

// failingSendSupervisor makes the opening turn fail and records the stop.
type failingSendSupervisor struct {
	headlessSupervisor
	sentErr error
	stopped []string
}

func (s *failingSendSupervisor) Send(context.Context, string, string) error { return s.sentErr }

func (s *failingSendSupervisor) Stop(_ context.Context, id string) error {
	s.stopped = append(s.stopped, id)
	return nil
}

// fakeTabOpener records every tab request and returns a fixed result.
type fakeTabOpener struct {
	requests []OpenTabRequest
	result   OpenTabResult
	err      error
}

func (f *fakeTabOpener) Open(
	_ context.Context,
	request OpenTabRequest,
) (*OpenTabResult, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return nil, f.err
	}
	result := f.result
	if result.PaneID == "" {
		result.PaneID = "pane-tab"
	}
	if result.SessionID == "" {
		result.SessionID = openExecTestUUID(900)
	}
	if result.TabName == "" {
		result.TabName = request.Subject
	}
	return &result, nil
}

// recordingCommandRunner records every argv.
type recordingCommandRunner struct {
	argvs [][]string
	err   error
	order *orderLog
}

func (r *recordingCommandRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	argv := append([]string{name}, args...)
	r.argvs = append(r.argvs, argv)
	if r.order != nil {
		r.order.add(strings.Join(argv, " "))
	}
	return nil, r.err
}

// probeCalls counts the answer-path probe invocations.
func probeCalls(scripts *fakeScriptRunner) int {
	count := 0
	for _, argv := range scripts.calls {
		if len(argv) > 1 && argv[1] == openAnswerPathScript {
			count++
		}
	}
	return count
}

// openProbeScripts answers the probe and both manager scripts successfully.
func openProbeScripts() *fakeScriptRunner {
	return &fakeScriptRunner{results: map[string]ScriptResult{
		openAnswerPathScript:           {ExitCode: 0},
		"/scripts/manager-launcher.py": {Stdout: "/scripts/cc-mgr\tmanager-default\n"},
		"/scripts/topic-spawn.py":      {Stdout: "open-ok: T\n"},
	}}
}

// openExecutorFixture wires the executor over recording fakes.
type openExecutorFixture struct {
	dir       string
	vault     Vault
	vaults    *fakeVaults
	registry  *fakeRegistry
	roleMap   *fakeRoleMap
	tabs      *fakeTabClient
	joiner    *fakeJoiner
	headless  *fakeHeadlessOpener
	tabOpener *fakeTabOpener
	runner    *recordingCommandRunner
	scripts   *fakeScriptRunner
	logs      *logCapture
	executor  OpenExecutor
}

func newOpenExecutorFixture() *openExecutorFixture {
	dir := ginkgo.GinkgoT().TempDir()
	vault := Vault{
		Name:            "v1",
		Path:            dir,
		ClaudeScript:    "/scripts/cc-v1",
		ManagerLauncher: "/scripts/mgr",
	}
	f := &openExecutorFixture{
		dir:      dir,
		vault:    vault,
		vaults:   &fakeVaults{vaults: []Vault{vault}},
		registry: &fakeRegistry{names: map[string][]string{}},
		roleMap: &fakeRoleMap{entries: map[string]RoleMapEntry{
			"Direct":   {Chip: new("cyan"), WindowID: new(12)},
			"Managers": {Chip: new("orange"), WindowID: new(14)},
			"Hold":     {WindowID: new(2)},
		}},
		tabs:      &fakeTabClient{},
		joiner:    &fakeJoiner{panes: map[int]string{}},
		headless:  &fakeHeadlessOpener{},
		tabOpener: &fakeTabOpener{},
		runner:    &recordingCommandRunner{},
		scripts:   openProbeScripts(),
		logs:      &logCapture{},
	}
	f.executor = f.build()
	return f
}

func (f *openExecutorFixture) deps() OpenExecutorDeps {
	return OpenExecutorDeps{
		Vaults:           f.vaults,
		Registry:         f.registry,
		Panes:            f.joiner,
		RoleMap:          f.roleMap,
		Tabs:             f.tabs,
		TabOpener:        f.tabOpener,
		Headless:         f.headless,
		Runner:           f.runner,
		Scripts:          f.scripts,
		AnswerPathScript: openAnswerPathScript,
		Logf:             f.logs.logf,
	}
}

func (f *openExecutorFixture) build() OpenExecutor {
	return NewOpenExecutor(f.deps())
}

func openTaskRow(name string) OpenRow {
	return OpenRow{Kind: OpenKindTask, Vault: "v1", Name: name}
}

var _ = ginkgo.Describe("OpenExecutor", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("AC6: an agent row never gets a tab, even with stored mode interactive", func() {
		f := newOpenExecutorFixture()
		id := openExecTestUUID(1)
		writeOpenPage(f.dir, openTaskTestFolder, "A",
			"role: agent", "mode: interactive", "claude_session_id: "+id)
		writeOpenPage(f.dir, openTaskTestFolder, "B", "role: agent")

		verdicts := f.executor.OpenRequest(
			ctx, []OpenRow{openTaskRow("A"), openTaskRow("B")}, "caller-1",
		)

		gomega.Expect(verdicts).To(gomega.HaveLen(2))
		gomega.Expect(verdicts[0].Path).To(gomega.Equal(OpenPathHeadless))
		gomega.Expect(verdicts[0].Mode).To(gomega.Equal(SpawnModeHeadless))
		gomega.Expect(verdicts[0].ModeNote).To(gomega.Equal(OpenModeNoteNormalised))
		gomega.Expect(verdicts[0].Branch).To(gomega.Equal(OpenBranchResume))
		gomega.Expect(verdicts[1].Branch).To(gomega.Equal(OpenBranchCreate))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(2))
		gomega.Expect(f.headless.requests[0].Resume).To(gomega.Equal(id))
		gomega.Expect(f.headless.requests[1].Resume).To(gomega.BeEmpty())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
		gomega.Expect(f.roleMap.reads).To(gomega.Equal(0))
	})

	ginkgo.It("AC10: the answer-path probe gates every request-mode headless spawn", func() {
		// Exit 1 refuses with the probe's stdout.
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		f.scripts.results[openAnswerPathScript] = ScriptResult{
			ExitCode: 1, Stdout: "no manager loop armed for this session\n",
		}
		verdict := f.executor.OpenRequest(ctx, []OpenRow{openTaskRow("A")}, "caller-1")[0]
		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonAnswerPath))
		gomega.Expect(verdict.Message).To(gomega.Equal("no manager loop armed for this session"))
		gomega.Expect(verdict.Remedy).To(gomega.Equal(OpenAnswerPathRemedy))
		gomega.Expect(verdict.Remedy).NotTo(gomega.ContainSubstring("interactive"))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
		gomega.Expect(f.scripts.calls).To(gomega.ContainElement([]string{
			"python3", openAnswerPathScript, "--session-id", "caller-1",
		}))

		// A run error refuses with its own text, never a pass.
		f = newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		f.scripts.errs = map[string]error{
			openAnswerPathScript: stderrors.New("probe crashed"),
		}
		verdict = f.executor.OpenRequest(ctx, []OpenRow{openTaskRow("A")}, "caller-1")[0]
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonAnswerPath))
		gomega.Expect(verdict.Message).To(gomega.Equal("probe crashed"))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())

		// No caller session refuses before the probe is run.
		f = newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		verdict = f.executor.OpenRequest(ctx, []OpenRow{openTaskRow("A")}, "")[0]
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonAnswerPath))
		gomega.Expect(verdict.Message).To(gomega.ContainSubstring("no X-Claude-Code-Session-Id header"))
		gomega.Expect(f.scripts.calls).To(gomega.BeEmpty())
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())

		// The probe is not run for jump, live, yours or tab rows.
		f = newOpenExecutorFixture()
		humanID := openExecTestUUID(2)
		liveID := openExecTestUUID(3)
		writeOpenPage(f.dir, openTaskTestFolder, "H", "role: human", "claude_session_id: "+humanID)
		writeOpenPage(f.dir, openTaskTestFolder, "L", "role: agent", "claude_session_id: "+liveID)
		writeOpenPage(f.dir, openTaskTestFolder, "Y", "role: manager")
		writeOpenPage(f.dir, openTaskTestFolder, "C", "role: human")
		f.registry.live = []RegistryEntry{
			{PID: 9, SessionID: humanID, Name: "H"},
			{PID: 10, SessionID: liveID, Name: "L"},
		}
		f.joiner.panes = map[int]string{9: "p9"}
		verdicts := f.executor.OpenRequest(ctx, []OpenRow{
			openTaskRow("H"), openTaskRow("L"), openTaskRow("Y"), openTaskRow("C"),
		}, "caller-1")
		gomega.Expect(verdicts[0].Branch).To(gomega.Equal(OpenBranchJump))
		gomega.Expect(verdicts[1].Branch).To(gomega.Equal(OpenBranchLive))
		gomega.Expect(verdicts[2].Branch).To(gomega.Equal(OpenBranchYours))
		gomega.Expect(verdicts[3].Branch).To(gomega.Equal(OpenBranchCreate))
		gomega.Expect(probeCalls(f.scripts)).To(gomega.Equal(0))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("AC8/AC9: a tab row refuses a missing key and the Hold window", func() {
		f := newOpenExecutorFixture()
		f.roleMap.entries = map[string]RoleMapEntry{
			"Managers": {Chip: new("orange"), WindowID: new(14)},
			"Hold":     {WindowID: new(2)},
		}
		writeOpenPage(f.dir, openTaskTestFolder, "H", "role: human")
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")

		verdicts := f.executor.OpenRequest(
			ctx, []OpenRow{openTaskRow("H"), openTaskRow("A")}, "caller-1",
		)

		gomega.Expect(verdicts[0].Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(verdicts[0].Reason).To(gomega.Equal(OpenReasonRoleMapKeyMissing))
		gomega.Expect(verdicts[0].Message).To(gomega.ContainSubstring("Direct"))
		gomega.Expect(verdicts[0].WindowKey).To(gomega.Equal("Direct"))
		gomega.Expect(verdicts[0].WindowID).To(gomega.BeNil())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))

		hold := newOpenExecutorFixture()
		hold.roleMap.entries = map[string]RoleMapEntry{
			"Direct": {Chip: new("cyan"), WindowID: new(2)},
			"Hold":   {WindowID: new(2)},
		}
		writeOpenPage(hold.dir, openTaskTestFolder, "H", "role: human")
		verdict := hold.executor.OpenRequest(ctx, []OpenRow{openTaskRow("H")}, "caller-1")[0]
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonHoldWindow))
		gomega.Expect(hold.tabOpener.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("AC12: every row, session and unported-field arm refuses with no side effect", func() {
		cases := []struct {
			name  string
			setup func(f *openExecutorFixture)
			row   OpenRow
			want  string
		}{
			{
				name:  "missing-row",
				setup: func(f *openExecutorFixture) {},
				row:   openTaskRow("Absent"),
				want:  OpenReasonMissingRow,
			},
			{
				name: "row-unreadable",
				setup: func(f *openExecutorFixture) {
					gomega.Expect(os.MkdirAll(
						filepath.Join(f.dir, openTaskTestFolder), 0o755,
					)).To(gomega.Succeed())
					path := filepath.Join(f.dir, openTaskTestFolder, "Broken.md")
					gomega.Expect(os.Symlink(path, path)).To(gomega.Succeed())
				},
				row:  openTaskRow("Broken"),
				want: OpenReasonRowUnreadable,
			},
			{
				name: "truncated-id",
				setup: func(f *openExecutorFixture) {
					writeOpenPage(f.dir, openTaskTestFolder, "T", "role: agent", "claude_session_id: 1234")
				},
				row:  openTaskRow("T"),
				want: OpenReasonTruncatedID,
			},
			{
				name: "liveness-unreadable",
				setup: func(f *openExecutorFixture) {
					writeOpenPage(f.dir, openTaskTestFolder, "T", "role: agent")
					f.registry.liveErr = stderrors.New("registry down")
				},
				row:  openTaskRow("T"),
				want: OpenReasonLivenessUnreadable,
			},
			{
				name: "goal-loop-unsupported",
				setup: func(f *openExecutorFixture) {
					writeOpenPage(f.dir, openTaskTestFolder, "T", "role: agent", "goal_loop: true")
				},
				row:  openTaskRow("T"),
				want: OpenReasonGoalLoopUnsupported,
			},
			{
				name: "target-unsupported",
				setup: func(f *openExecutorFixture) {
					writeOpenPage(f.dir, openTaskTestFolder, "T", "role: agent", "target: k8s")
				},
				row:  openTaskRow("T"),
				want: OpenReasonTargetUnsupported,
			},
		}
		for _, tc := range cases {
			ginkgo.By(tc.name)
			f := newOpenExecutorFixture()
			tc.setup(f)
			verdict := f.executor.OpenRequest(ctx, []OpenRow{tc.row}, "caller-1")[0]
			gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchRefused))
			gomega.Expect(verdict.Reason).To(gomega.Equal(tc.want))
			gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
			gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
			gomega.Expect(probeCalls(f.scripts)).To(gomega.Equal(0))
			gomega.Expect(f.runner.argvs).To(gomega.BeEmpty())
		}
	})

	ginkgo.It("AC12: shipping reaches the headless spawn with the shipping permission mode", func() {
		f := newOpenExecutorFixture()
		supervisor := &headlessSupervisor{}
		ledger := &fakeLedger{}
		server := &RosterToolServer{
			Supervisor: supervisor,
			Vaults:     f.vaults,
			Ledger:     ledger,
		}
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent", "shipping: true")
		deps := f.deps()
		deps.Headless = server
		executor := NewOpenExecutor(deps)

		verdict := executor.OpenRequest(ctx, []OpenRow{openTaskRow("A")}, "caller-1")[0]

		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchCreate))
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		gomega.Expect(supervisor.spawned[0].PermissionMode).To(gomega.Equal(ShippingPermissionMode))
		gomega.Expect(supervisor.spawned[0].Role).To(gomega.Equal(RoleAgent))
		gomega.Expect(supervisor.spawned[0].Label).To(gomega.Equal("A"))
		gomega.Expect(ledger.posts).To(gomega.HaveLen(1))
		gomega.Expect(ledger.posts[0].ModeSource).To(gomega.Equal(OpenModeSource))
	})

	ginkgo.It("AC13: a live human or manager row jumps and the pane is activated", func() {
		f := newOpenExecutorFixture()
		humanID := openExecTestUUID(10)
		goalID := openExecTestUUID(11)
		writeOpenPage(f.dir, openTaskTestFolder, "H", "role: human", "claude_session_id: "+humanID)
		writeOpenPage(f.dir, openGoalTestFolder, "G")
		f.registry.live = []RegistryEntry{
			{PID: 9, SessionID: humanID, Name: "H"},
			{PID: 10, SessionID: goalID, Name: "G Manager"},
		}
		f.joiner.panes = map[int]string{9: "p9", 10: "p10"}

		verdicts := f.executor.OpenRequest(ctx, []OpenRow{
			openTaskRow("H"),
			{Kind: OpenKindGoal, Vault: "v1", Name: "G"},
		}, "caller-1")

		gomega.Expect(verdicts[0].Branch).To(gomega.Equal(OpenBranchJump))
		gomega.Expect(verdicts[0].PaneID).To(gomega.Equal("p9"))
		gomega.Expect(verdicts[1].Branch).To(gomega.Equal(OpenBranchJump))
		gomega.Expect(verdicts[1].PaneID).To(gomega.Equal("p10"))
		gomega.Expect(f.tabs.activated).To(gomega.Equal([]string{"p9", "p10"}))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())

		f = newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "H", "role: human", "claude_session_id: "+humanID)
		f.registry.live = []RegistryEntry{{PID: 9, SessionID: humanID, Name: "H"}}
		f.joiner.panes = map[int]string{9: "p9"}
		f.tabs.activateErr = stderrors.New("no such pane")
		verdict := f.executor.OpenRequest(ctx, []OpenRow{openTaskRow("H")}, "caller-1")[0]
		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonSpawnRefused))
		gomega.Expect(verdict.Message).To(gomega.Equal("activate pane p9: no such pane"))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("AC14: a defaulted role and normalised mode are written back before the spawn", func() {
		f := newOpenExecutorFixture()
		order := &orderLog{}
		f.runner.order = order
		f.headless.order = order
		writeOpenPage(f.dir, openTaskTestFolder, "T", "mode: interactive")

		verdict := f.executor.OpenRequest(ctx, []OpenRow{openTaskRow("T")}, "caller-1")[0]

		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchCreate))
		gomega.Expect(f.runner.argvs).To(gomega.Equal([][]string{
			{"vault-cli", "--vault", "v1", "task", "set", "T", "role", "agent"},
			{"vault-cli", "--vault", "v1", "task", "set", "T", "mode", "headless"},
		}))
		gomega.Expect(order.events).To(gomega.Equal([]string{
			"vault-cli --vault v1 task set T role agent",
			"vault-cli --vault v1 task set T mode headless",
			"headless",
		}))

		// A failed write is a WARNING and never changes the verdict.
		f = newOpenExecutorFixture()
		id := openExecTestUUID(20)
		f.runner.err = stderrors.New("vault-cli exploded")
		writeOpenPage(f.dir, openTaskTestFolder, "R", "role: agent", "claude_session_id: "+id)
		verdict = f.executor.OpenRequest(ctx, []OpenRow{openTaskRow("R")}, "caller-1")[0]
		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchResume))
		warnings := 0
		for _, line := range f.logs.lines {
			if strings.Contains(line, "WARNING") && strings.Contains(line, "write-back") {
				warnings++
			}
		}
		gomega.Expect(warnings).To(gomega.Equal(1))

		// Goal and topic rows never write back.
		f = newOpenExecutorFixture()
		writeOpenPage(f.dir, openGoalTestFolder, "G")
		writeOpenPage(f.dir, openTopicTestFolder, "Tp")
		f.executor.OpenRequest(ctx, []OpenRow{
			{Kind: OpenKindGoal, Vault: "v1", Name: "G"},
			{Kind: OpenKindTopic, Vault: "v1", Name: "Tp"},
		}, "caller-1")
		gomega.Expect(f.runner.argvs).To(gomega.BeEmpty())
	})

	ginkgo.It("AC15: a concurrent open of one row is refused in-flight", func() {
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "T", "role: agent")
		f.headless.block = make(chan struct{})
		f.headless.entered = make(chan struct{}, 1)
		f.executor = f.build()
		rows := []OpenRow{openTaskRow("T")}

		done := make(chan []OpenVerdict, 1)
		go func() {
			done <- f.executor.OpenRequest(context.Background(), rows, "caller-1")
		}()
		<-f.headless.entered

		second := f.executor.OpenRequest(context.Background(), rows, "caller-2")
		gomega.Expect(second).To(gomega.HaveLen(1))
		gomega.Expect(second[0].Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(second[0].Reason).To(gomega.Equal(OpenReasonInFlight))

		close(f.headless.block)
		first := <-done
		gomega.Expect(first[0].Branch).To(gomega.Equal(OpenBranchCreate))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("AC16: every row writes exactly one log line", func() {
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		writeOpenPage(f.dir, openTaskTestFolder, "H", "role: human")

		f.executor.OpenRequest(ctx, []OpenRow{openTaskRow("A"), openTaskRow("H")}, "caller-1")

		gomega.Expect(f.logs.lines).To(gomega.HaveLen(2))
		gomega.Expect(f.logs.lines[0]).To(gomega.Equal(
			"open: row=v1/task/A role=agent mode=headless branch=create path=headless window=none reason=-",
		))
		gomega.Expect(f.logs.lines[1]).To(gomega.Equal(
			"open: row=v1/task/H role=human mode=interactive branch=create path=tab window=12 reason=-",
		))
	})

	ginkgo.It("AC18: a refused row never ends the batch", func() {
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		writeOpenPage(f.dir, openTaskTestFolder, "C", "role: agent")

		verdicts := f.executor.OpenRequest(ctx, []OpenRow{
			openTaskRow("A"), openTaskRow("B"), openTaskRow("C"),
		}, "caller-1")

		gomega.Expect(verdicts).To(gomega.HaveLen(3))
		gomega.Expect(verdicts[0].Branch).To(gomega.Equal(OpenBranchCreate))
		gomega.Expect(verdicts[1].Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(verdicts[1].Reason).To(gomega.Equal(OpenReasonMissingRow))
		gomega.Expect(verdicts[2].Branch).To(gomega.Equal(OpenBranchCreate))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(2))
		gomega.Expect(f.headless.requests[0].Task).To(gomega.Equal("A"))
		gomega.Expect(f.headless.requests[1].Task).To(gomega.Equal("C"))
	})

	ginkgo.It("AC19: a spawn refusal passes the component's message through", func() {
		// The spawn cap refuses.
		f := newOpenExecutorFixture()
		supervisor := &headlessSupervisor{}
		server := &RosterToolServer{
			Supervisor: supervisor,
			Vaults:     f.vaults,
			Caps:       &fakeSpawnCaps{workerErr: stderrors.New("the worker fleet is FULL (51 of 50)")},
		}
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		deps := f.deps()
		deps.Headless = server
		verdict := NewOpenExecutor(deps).
			OpenRequest(ctx, []OpenRow{openTaskRow("A")}, "caller-1")[0]
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonSpawnRefused))
		gomega.Expect(verdict.Message).To(gomega.Equal("the worker fleet is FULL (51 of 50)"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())

		// The terminal-task guard refuses.
		f = newOpenExecutorFixture()
		supervisor = &headlessSupervisor{}
		server = &RosterToolServer{Supervisor: supervisor, Vaults: f.vaults}
		id := openExecTestUUID(30)
		writeOpenPage(f.dir, openTaskTestFolder, "T",
			"role: agent", "status: completed", "claude_session_id: "+id)
		deps = f.deps()
		deps.Headless = server
		verdict = NewOpenExecutor(deps).
			OpenRequest(ctx, []OpenRow{openTaskRow("T")}, "caller-1")[0]
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonSpawnRefused))
		gomega.Expect(verdict.Message).To(gomega.ContainSubstring("refused"))
		gomega.Expect(verdict.Message).To(gomega.ContainSubstring("completed"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())

		// manager-launcher.py refuses.
		f = newOpenExecutorFixture()
		tabs := &fakeTabClient{}
		scripts := &fakeScriptRunner{results: map[string]ScriptResult{
			openAnswerPathScript:           {ExitCode: 0},
			"/scripts/manager-launcher.py": {ExitCode: 1, Stderr: "unsafe launcher: x\n"},
		}}
		opener := &openTabOpener{
			tabs:                  tabs,
			registry:              f.registry,
			vaults:                f.vaults,
			ledger:                &fakeLedger{},
			scripts:               scripts,
			now:                   steppingClock(),
			managerLauncherScript: "/scripts/manager-launcher.py",
			topicSpawnScript:      "/scripts/topic-spawn.py",
			exitRecord:            "/scripts/exit.py",
			sleep:                 func(time.Duration) {},
		}
		writeOpenPage(f.dir, openGoalTestFolder, "G")
		deps = f.deps()
		deps.TabOpener = opener
		verdict = NewOpenExecutor(deps).
			OpenRequest(ctx, []OpenRow{{Kind: OpenKindGoal, Vault: "v1", Name: "G"}}, "caller-1")[0]
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonSpawnRefused))
		gomega.Expect(verdict.Message).To(gomega.Equal("unsafe launcher: x"))
		gomega.Expect(tabs.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("stops a child whose opening turn fails and refuses the row", func() {
		f := newOpenExecutorFixture()
		supervisor := &failingSendSupervisor{sentErr: stderrors.New("stdin closed")}
		server := &RosterToolServer{Supervisor: supervisor, Vaults: f.vaults}
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		deps := f.deps()
		deps.Headless = server

		verdict := NewOpenExecutor(deps).
			OpenRequest(ctx, []OpenRow{openTaskRow("A")}, "caller-1")[0]

		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonSpawnRefused))
		gomega.Expect(verdict.Message).To(gomega.ContainSubstring("deliver opening turn failed"))
		gomega.Expect(supervisor.stopped).To(gomega.Equal([]string{"headless-1"}))
	})

	ginkgo.It("opens no row once the caller has disconnected", func() {
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		verdicts := f.executor.OpenRequest(cancelled, []OpenRow{openTaskRow("A")}, "caller-1")

		gomega.Expect(verdicts).To(gomega.BeEmpty())
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
		gomega.Expect(f.logs.lines).To(gomega.BeEmpty())
	})

	ginkgo.It("AC20: first_step prefixes the headless and tab work commands", func() {
		f := newOpenExecutorFixture()
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent")
		writeOpenPage(f.dir, openTaskTestFolder, "A2", "role: agent")
		f.executor.OpenRequest(ctx, []OpenRow{
			{Kind: OpenKindTask, Vault: "v1", Name: "A", FirstStep: "X"},
			openTaskRow("A2"),
		}, "caller-1")
		gomega.Expect(f.headless.requests[0].Prompt).To(gomega.Equal(
			"X\n\n" + `/vault-cli:work-on-task "A"`,
		))
		gomega.Expect(f.headless.requests[1].Prompt).To(gomega.Equal(
			`/vault-cli:work-on-task "A2"`,
		))

		// A human row through the real opener receives the same text.
		f = newOpenExecutorFixture()
		claudeScript, _ := writeLauncherPair()
		f.vault.ClaudeScript = claudeScript
		f.vaults.vaults = []Vault{f.vault}
		writeOpenPage(f.dir, openTaskTestFolder, "H", "role: human")
		writeOpenPage(f.dir, openTaskTestFolder, "H2", "role: human")
		tabs := &fakeTabClient{}
		tabs.onSpawn = func() {
			last := tabs.spawned[len(tabs.spawned)-1]
			f.registry.names[last.TabName] = []string{"sess-" + last.TabName}
		}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, f.registry, nil, &fakeLedger{}, openProbeScripts(),
		)
		deps := f.deps()
		deps.TabOpener = opener
		NewOpenExecutor(deps).OpenRequest(ctx, []OpenRow{
			{Kind: OpenKindTask, Vault: "v1", Name: "H", FirstStep: "X"},
			openTaskRow("H2"),
		}, "caller-1")
		gomega.Expect(tabs.texts).To(gomega.Equal([]string{
			"X\n\n" + `/vault-cli:work-on-task "H"`,
			`/vault-cli:work-on-task "H2"`,
		}))
	})

	ginkgo.It("AC24: the tick entry point runs no probe and activates no pane", func() {
		f := newOpenExecutorFixture()
		humanID := openExecTestUUID(40)
		agentID := openExecTestUUID(41)
		writeOpenPage(f.dir, openTaskTestFolder, "H", "role: human", "claude_session_id: "+humanID)
		writeOpenPage(f.dir, openTaskTestFolder, "A", "role: agent", "claude_session_id: "+agentID)
		f.registry.live = []RegistryEntry{{PID: 9, SessionID: humanID, Name: "H"}}
		f.joiner.panes = map[int]string{9: "p9"}
		rows := []OpenRow{openTaskRow("H"), openTaskRow("A")}

		tick := f.executor.OpenTick(ctx, rows)
		gomega.Expect(tick).To(gomega.HaveLen(2))
		gomega.Expect(tick[0].Branch).To(gomega.Equal(OpenBranchJump))
		gomega.Expect(f.tabs.activated).To(gomega.BeEmpty())
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
		gomega.Expect(f.headless.requests[0].CallerSessionID).To(gomega.BeEmpty())
		gomega.Expect(probeCalls(f.scripts)).To(gomega.Equal(0))
		gomega.Expect(f.logs.lines).To(gomega.HaveLen(2))
		gomega.Expect(f.runner.argvs).NotTo(gomega.BeEmpty())

		request := f.executor.OpenRequest(ctx, rows, "caller-1")
		gomega.Expect(request).To(gomega.HaveLen(2))
		gomega.Expect(request[0].Branch).To(gomega.Equal(OpenBranchJump))
		gomega.Expect(f.tabs.activated).To(gomega.Equal([]string{"p9"}))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(2))
		gomega.Expect(probeCalls(f.scripts)).To(gomega.Equal(1))
		gomega.Expect(f.logs.lines).To(gomega.HaveLen(4))
	})
})
