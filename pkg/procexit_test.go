// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// An adopted child is not this process's child, so its exit is not collectable
// with `Wait` — the only observable is the pid going away. These specs pin that
// the wait returns on that transition, and returns immediately when it has
// already happened (a child that exited between adoption and the registration).
var _ = ginkgo.Describe("waiting for an adopted child to exit", func() {
	ginkgo.It("returns as soon as the process leaves", func() {
		// The pid must NOT be this process's child. A killed child stays a
		// zombie until it is reaped, and a zombie still answers kill(pid, 0),
		// so on Linux the watcher never sees it leave and this spec fails —
		// while macOS reaps it eagerly and the spec passes for the wrong
		// reason. A grandchild is reaped by init, which is the adopted-child
		// shape the spec is actually about.
		cmd := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!")
		out, err := cmd.Output()
		gomega.Expect(err).To(gomega.Succeed())
		pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
		gomega.Expect(err).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

		done := make(chan struct{})
		go func() {
			waitForProcessExit(pid)
			close(done)
		}()

		// Give the watcher time to register before the process leaves; without
		// this the test would pass on the already-dead path instead.
		time.Sleep(200 * time.Millisecond)
		gomega.Expect(syscall.Kill(pid, syscall.SIGKILL)).To(gomega.Succeed())

		gomega.Eventually(done, "3s").Should(gomega.BeClosed())
	})

	ginkgo.It("returns immediately for a pid that is already gone", func() {
		cmd := exec.Command("sleep", "30")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		pid := cmd.Process.Pid
		gomega.Expect(cmd.Process.Kill()).To(gomega.Succeed())
		_, _ = cmd.Process.Wait()

		done := make(chan struct{})
		go func() {
			waitForProcessExit(pid)
			close(done)
		}()
		gomega.Eventually(done, "2s").Should(gomega.BeClosed())
	})

	ginkgo.It("returns immediately for a pid that never existed", func() {
		done := make(chan struct{})
		go func() {
			waitForProcessExit(0)
			close(done)
		}()
		gomega.Eventually(done, "2s").Should(gomega.BeClosed())
	})
})
