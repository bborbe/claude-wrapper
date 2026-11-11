// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// These specs drive spawn_agent through the MCP adapter — raw JSON arguments in,
// SpawnOptions out — because the adapter is the layer that must carry `resume`
// from the tool call to the child: a session id that stops here leaves every
// headless resume opening a fresh conversation.
var _ = ginkgo.Describe("RosterToolServer.spawnAgent resume", func() {
	const resumed = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	var (
		ctx        context.Context
		supervisor *toolSupervisor
		server     *RosterToolServer
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		supervisor = &toolSupervisor{}
		server = &RosterToolServer{Supervisor: supervisor, Spawner: &fakeSpawner{}}
	})

	spawn := func(args string) (string, error) {
		return server.spawnAgent(ctx, json.RawMessage(args), "")
	}

	ginkgo.It("forwards a session id to a headless spawn, so the child resumes it", func() {
		_, err := spawn(`{"resume":"` + resumed + `","interactive":false,"cwd":"/tmp"}`)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		options := supervisor.spawned[0]
		gomega.Expect(options.Resume).To(gomega.Equal(resumed))

		// The argv the supervisor builds from exactly these options must carry the
		// id after --resume, and must not mint a second identity alongside it.
		args := buildArgs("11111111-2222-4333-8444-555555555555", options, "/tmp/generated.json")
		gomega.Expect(indexOf(args, "--resume")).To(gomega.BeNumerically(">=", 0))
		gomega.Expect(indexOf(args, "--resume") + 1).To(gomega.Equal(indexOf(args, resumed)))
		gomega.Expect(args).NotTo(gomega.ContainElement("--session-id"))
	})

	ginkgo.It("spawns a fresh conversation when no resume is given", func() {
		_, err := spawn(`{"interactive":false,"cwd":"/tmp"}`)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		gomega.Expect(supervisor.spawned[0].Resume).To(gomega.BeEmpty())
	})

	// The pre-fix shape could not name a conversation. Decoding it must fail and
	// name the field — a silent fresh spawn on a resume request is the defect.
	ginkgo.It("refuses the old boolean shape with an error naming the field", func() {
		_, err := spawn(`{"resume":true,"interactive":false,"cwd":"/tmp"}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("resume"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("still refuses a resume on the tab path", func() {
		_, err := spawn(`{"resume":"` + resumed + `","cwd":"/tmp","prompt":"x"}`)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("headless worker only"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})
})

// The guard exists because the resume that fires in production is
// `spawn_agent(resume=<id>)` with no `task` and no `vault`, so the task has to be
// resolved from the session id the resume already carries. A guard keyed on a
// caller-supplied `task` would never fire on the path that matters.
var _ = ginkgo.Describe("RosterToolServer.spawnAgent resume guard", func() {
	const (
		sessionID = "c0667383-40f0-4599-8630-9a15c63edee6"
		title     = "A Finished Task"
	)

	var (
		ctx        context.Context
		dir        string
		supervisor *toolSupervisor
		server     *RosterToolServer
		logs       *bytes.Buffer
		restoreLog func()
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "resume-guard-*")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(os.MkdirAll(filepath.Join(dir, "25 Tasks"), 0o755)).To(gomega.BeNil())

		// The production call carries a `prompt`, so the fake supervisor must have a
		// live channel to emit on — a nil one blocks Send forever rather than failing.
		supervisor = &toolSupervisor{live: make(chan Event, 64)}
		server = &RosterToolServer{
			Supervisor: supervisor,
			Spawner:    &fakeSpawner{},
			// WorkerLauncher is load-bearing only for the vault-named spec: a spawn
			// that names a vault resolves it to a directory AND a launcher, and one
			// with neither is refused before the guard is ever reached.
			Vaults: &fakeVaults{vaults: []Vault{{
				Name:           "test-vault",
				Path:           dir,
				WorkerLauncher: "/bin/true",
			}}},
		}

		logs = &bytes.Buffer{}
		previous := log.Writer()
		log.SetOutput(logs)
		restoreLog = func() { log.SetOutput(previous) }
	})

	ginkgo.AfterEach(func() {
		restoreLog()
		_ = os.RemoveAll(dir)
	})

	writeTask := func(taskTitle, frontmatter string) {
		gomega.Expect(os.WriteFile(
			filepath.Join(dir, "25 Tasks", taskTitle+".md"),
			[]byte("---\n"+frontmatter+"---\nbody\n"),
			0o600,
		)).To(gomega.BeNil())
	}

	spawn := func(args string) (string, error) {
		return server.spawnAgent(ctx, json.RawMessage(args), "")
	}

	// The production shape: no `task`, no `vault`, an explicit `cwd`.
	productionCall := func() (string, error) {
		return spawn(
			`{"prompt":"continue","resume":"` + sessionID + `","interactive":false,` +
				`"cwd":"` + dir + `"}`,
		)
	}

	ginkgo.It("refuses a headless resume whose task is completed", func() {
		writeTask(title, "status: completed\nclaude_session_id: "+sessionID+"\n")

		_, err := productionCall()

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("task-terminal"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a headless resume whose task was aborted", func() {
		writeTask(title, "status: aborted\nclaude_session_id: "+sessionID+"\n")

		_, err := productionCall()

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("task-terminal"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("names the task and its status in the refusal", func() {
		writeTask(title, "status: completed\nclaude_session_id: "+sessionID+"\n")

		_, err := productionCall()

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(title))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("completed"))
	})

	ginkgo.It("resumes an in_progress task", func() {
		writeTask(title, "status: in_progress\nclaude_session_id: "+sessionID+"\n")

		_, err := productionCall()

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		gomega.Expect(supervisor.spawned[0].Resume).To(gomega.Equal(sessionID))
	})

	// A held task is a wait, not a finish: refusing it would break the leg the
	// guard exists to protect.
	ginkgo.It("resumes a held task", func() {
		writeTask(title, "status: hold\nclaude_session_id: "+sessionID+"\n")

		_, err := productionCall()

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
	})

	// A deferred task is exactly what the park-watch resume brings back.
	ginkgo.It("resumes a task carrying a future defer_date", func() {
		writeTask(
			title,
			"status: in_progress\ndefer_date: 2099-01-01T00:00:00Z\n"+
				"claude_session_id: "+sessionID+"\n",
		)

		_, err := productionCall()

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
	})

	// The by-title fallback: a caller that names its task is checked on it even
	// when the session id resolves to nothing, because a task file may predate its
	// `claude_session_id:` being written.
	ginkgo.It("refuses a caller-named terminal task whose session id resolves to nothing", func() {
		writeTask(title, "status: completed\nclaude_session_id: some-other-session\n")

		_, err := spawn(
			`{"prompt":"continue","resume":"` + sessionID + `","interactive":false,` +
				`"cwd":"` + dir + `","vault":"test-vault","task":"` + title + `"}`,
		)

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("task-terminal"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(title))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	// Fail-open is deliberate — no task claims the session, so nothing can be
	// proven terminal — but it must be VISIBLE, not silent.
	ginkgo.It("allows an id no task carries and logs one line naming it", func() {
		writeTask(title, "status: completed\nclaude_session_id: some-other-session\n")

		_, err := productionCall()

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
		gomega.Expect(logs.String()).To(gomega.ContainSubstring(sessionID))
		gomega.Expect(logs.String()).To(gomega.ContainSubstring("unresolved"))
	})

	// The guard runs before the caps reserve a start slot: a refused resume must
	// not charge the next legitimate spawn for it.
	ginkgo.It("does not spend a start slot on a refused resume", func() {
		writeTask(title, "status: completed\nclaude_session_id: "+sessionID+"\n")
		caps := &fakeSpawnCaps{}
		server.Caps = caps

		_, err := productionCall()

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(caps.startCalls).To(gomega.Equal(0))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})
})
