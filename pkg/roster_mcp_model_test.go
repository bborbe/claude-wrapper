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

// headlessModel is the whole of the headless spawn's model resolution: a child is a
// bare `claude --print`, so whatever this returns is the only model it can start on.
var _ = ginkgo.Describe("RosterToolServer.headlessModel", func() {
	var (
		ctx       context.Context
		dir       string
		launcher  string
		deepseek  = "deepseek-v4-flash-max[1m]"
		server    *RosterToolServer
		taskTitle = "A Task"
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "headless-model-*")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(os.MkdirAll(filepath.Join(dir, "25 Tasks"), 0o755)).To(gomega.BeNil())

		launcher = filepath.Join(dir, "cc-test-deepseek")
		gomega.Expect(os.WriteFile(launcher, []byte(
			"export ANTHROPIC_DEFAULT_OPUS_MODEL=\""+deepseek+"\"\n"+
				"# a launch that passes no --model inherits the ambient\n"+
				"claude --model \"${ANTHROPIC_DEFAULT_OPUS_MODEL}\" \"$@\"\n"), 0o600)).
			To(gomega.BeNil())

		server = &RosterToolServer{Vaults: &fakeVaults{vaults: []Vault{{
			Name:           "test-vault",
			Path:           dir,
			WorkerLauncher: launcher,
		}}}}
	})

	ginkgo.AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	writeTask := func(body string) {
		gomega.Expect(os.WriteFile(filepath.Join(dir, "25 Tasks", taskTitle+".md"),
			[]byte(body), 0o600)).To(gomega.BeNil())
	}

	ginkgo.It("returns the caller's explicit model unchanged", func() {
		model := server.headlessModel(ctx, spawnAgentArgs{
			Vault: "test-vault", Task: taskTitle, Model: "claude-sonnet-5-5",
		}, dir)
		gomega.Expect(model).To(gomega.Equal("claude-sonnet-5-5"))
	})

	ginkgo.It("resolves the task's own launcher model", func() {
		writeTask("---\nlauncher: " + launcher + "\n---\nbody\n")
		model := server.headlessModel(ctx, spawnAgentArgs{Vault: "test-vault", Task: taskTitle}, dir)
		gomega.Expect(model).To(gomega.Equal(deepseek))
	})

	ginkgo.It("falls back to the vault's worker launcher when the task names none", func() {
		writeTask("---\nstatus: in_progress\n---\nbody\n")
		model := server.headlessModel(ctx, spawnAgentArgs{Vault: "test-vault", Task: taskTitle}, dir)
		gomega.Expect(model).To(gomega.Equal(deepseek))
	})

	ginkgo.It("falls back to the vault's worker launcher when the task is absent", func() {
		model := server.headlessModel(ctx, spawnAgentArgs{Vault: "test-vault"}, dir)
		gomega.Expect(model).To(gomega.Equal(deepseek))
	})

	ginkgo.It("returns empty rather than a guess when no vault is wired", func() {
		bare := &RosterToolServer{}
		gomega.Expect(bare.headlessModel(ctx, spawnAgentArgs{Vault: "test-vault"}, dir)).
			To(gomega.BeEmpty())
	})
})
