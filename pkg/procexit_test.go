// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os/exec"
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
		cmd := exec.Command("sleep", "30")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		pid := cmd.Process.Pid
		ginkgo.DeferCleanup(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		})

		done := make(chan struct{})
		go func() {
			waitForProcessExit(pid)
			close(done)
		}()

		// Give the watcher time to register before the process leaves; without
		// this the test would pass on the already-dead path instead.
		time.Sleep(200 * time.Millisecond)
		gomega.Expect(cmd.Process.Signal(syscall.SIGKILL)).To(gomega.Succeed())

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
