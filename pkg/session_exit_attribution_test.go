// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	libtime "github.com/bborbe/time"
)

// capturingRecorder keeps every ExitStatus the supervisor hands it, so a spec
// can assert what a kill actually recorded rather than what a formatter would
// print from a hand-built value.
type capturingRecorder struct {
	mutex  sync.Mutex
	status []ExitStatus
}

func (r *capturingRecorder) Record(_ Session, _ int, status ExitStatus) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.status = append(r.status, status)
}

func (r *capturingRecorder) count() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return len(r.status)
}

func (r *capturingRecorder) last() ExitStatus {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if len(r.status) == 0 {
		return ExitStatus{}
	}
	return r.status[len(r.status)-1]
}

var _ = ginkgo.Describe("session exit attribution", func() {
	var (
		ctx context.Context
		dir string
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
	})

	// silentChild announces itself and then reads its stdin forever without
	// answering, so the only thing that ends it is the signal under test.
	silentChild := func() string {
		path := filepath.Join(dir, "silent-child.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"m\",\"permissionMode\":\"auto\"}'\n" +
			"while IFS= read -r line; do\n" +
			"  :\n" +
			"done\n"
		gomega.Expect(os.WriteFile(path, []byte(script), 0o700)).To(gomega.Succeed())
		return path
	}

	newSupervisor := func(bin string) *supervisor {
		return &supervisor{
			ctx:       ctx,
			claudeBin: bin,
			now:       libtime.NewCurrentDateTime(),
			selfURL:   "http://localhost:0",
			store:     NewSessionStore(filepath.Join(dir, "sessions")),
			sessions:  map[string]*child{},
			pending:   NewPending(),
		}
	}

	ginkgo.It("names the closing job and the signal when StopAs ends a session", func() {
		recorder := &capturingRecorder{}
		sup := newSupervisor(silentChild())
		sup.SetSessionExitRecorder(recorder)

		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(sup.StopAs(ctx, session.ID, IdleReaperJobName)).To(gomega.Succeed())
		gomega.Eventually(recorder.count, "5s").Should(gomega.Equal(1))

		status := recorder.last()
		gomega.Expect(status.Sender).To(gomega.Equal(IdleReaperJobName))
		gomega.Expect(status.Signal).To(gomega.Equal(syscall.SIGKILL))
		gomega.Expect(status.Code).NotTo(gomega.BeNil())
		gomega.Expect(*status.Code).To(gomega.Equal(-1))
	})

	// The HTTP API and the MCP tool both reach Stop, and the daemon sends that
	// kill — so the record must name it rather than calling the signal external.
	ginkgo.It("names the API as the sender when Stop ends a session", func() {
		recorder := &capturingRecorder{}
		sup := newSupervisor(silentChild())
		sup.SetSessionExitRecorder(recorder)

		session, err := sup.Spawn(ctx, SpawnOptions{Cwd: dir})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(sup.Stop(ctx, session.ID)).To(gomega.Succeed())
		gomega.Eventually(recorder.count, "5s").Should(gomega.Equal(1))
		gomega.Expect(recorder.last().Sender).To(gomega.Equal("stop-api"))
	})

	ginkgo.It("recovers the killing signal from the reaped wait status", func() {
		cmd := exec.Command("sleep", "30")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		gomega.Expect(cmd.Process.Kill()).To(gomega.Succeed())
		gomega.Expect(signalFromExitErr(cmd.Wait())).To(gomega.Equal(syscall.SIGKILL))
	})

	ginkgo.It("reports no signal for a clean exit", func() {
		gomega.Expect(signalFromExitErr(exec.Command("true").Run())).
			To(gomega.Equal(syscall.Signal(0)))
	})

	// A kill that never landed must not leave its name behind: the child is still
	// running, and its eventual natural exit would be attributed to this sender.
	ginkgo.It("clears the sender when the kill fails", func() {
		c := &child{pid: 999999}
		gomega.Expect(c.killLocked(IdleReaperJobName)).NotTo(gomega.Succeed())
		gomega.Expect(c.lastKillSender).To(gomega.BeEmpty())
		gomega.Expect(c.lastKillSignal).To(gomega.Equal(syscall.Signal(0)))
	})
})
