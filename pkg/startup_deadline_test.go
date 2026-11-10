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
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("startup deadline", func() {
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	bound := 300 * time.Second

	ginkgo.DescribeTable("shouldTerminateStarting",
		func(state State, model string, elapsed time.Duration, expect bool) {
			session := Session{State: state, Model: model, StartedAt: now.Add(-elapsed)}
			gomega.Expect(shouldTerminateStarting(session, now, bound)).To(gomega.Equal(expect))
		},
		ginkgo.Entry("starting, no model, past the bound", StateStarting, "", 301*time.Second, true),
		ginkgo.Entry("starting, no model, far past the bound", StateStarting, "", 3*time.Hour, true),
		ginkgo.Entry("starting, no model, exactly at the bound", StateStarting, "", 300*time.Second, false),
		ginkgo.Entry("starting, no model, inside the bound", StateStarting, "", 10*time.Second, false),
		ginkgo.Entry("starting but a model is known — slow, not stuck", StateStarting, "deepseek-v4-flash-max", 3*time.Hour, false),
		ginkgo.Entry("idle is never a stuck start", StateIdle, "", 3*time.Hour, false),
		ginkgo.Entry("generating is never a stuck start", StateGenerating, "", 3*time.Hour, false),
		ginkgo.Entry("exited is left alone", StateExited, "", 3*time.Hour, false),
	)

	ginkgo.It("skips a session with no start time rather than reading it as infinitely old", func() {
		session := Session{State: StateStarting}
		gomega.Expect(shouldTerminateStarting(session, now, bound)).To(gomega.BeFalse())
	})

	ginkgo.It("is disabled by a zero bound", func() {
		session := Session{State: StateStarting, StartedAt: now.Add(-3 * time.Hour)}
		gomega.Expect(shouldTerminateStarting(session, now, 0)).To(gomega.BeFalse())
	})

	ginkgo.Describe("MaxStarting", func() {
		var ctx context.Context

		ginkgo.BeforeEach(func() {
			ctx = context.Background()
		})

		write := func(body string) string {
			path := filepath.Join(ginkgo.GinkgoT().TempDir(), "config.json")
			gomega.Expect(os.WriteFile(path, []byte(body), 0o600)).To(gomega.Succeed())
			return path
		}

		ginkgo.It("defaults when the key is absent", func() {
			path := write(`{"spawn":{"maxConcurrent":60}}`)
			gomega.Expect(LoadStartupConfig(ctx, path).MaxStarting(ctx, path)).
				To(gomega.Equal(300 * time.Second))
		})

		ginkgo.It("defaults when the file is absent", func() {
			path := filepath.Join(ginkgo.GinkgoT().TempDir(), "missing.json")
			gomega.Expect(LoadStartupConfig(ctx, path).MaxStarting(ctx, path)).
				To(gomega.Equal(300 * time.Second))
		})

		ginkgo.It("reads the configured seconds", func() {
			path := write(`{"startup":{"maxStartingSeconds":42}}`)
			gomega.Expect(LoadStartupConfig(ctx, path).MaxStarting(ctx, path)).
				To(gomega.Equal(42 * time.Second))
		})

		ginkgo.It("treats zero as the off switch", func() {
			path := write(`{"startup":{"maxStartingSeconds":0}}`)
			gomega.Expect(LoadStartupConfig(ctx, path).MaxStarting(ctx, path)).
				To(gomega.Equal(time.Duration(0)))
		})

		ginkgo.It("falls back to the default on a value that is not an integer", func() {
			path := write(`{"startup":{"maxStartingSeconds":"soon"}}`)
			gomega.Expect(LoadStartupConfig(ctx, path).MaxStarting(ctx, path)).
				To(gomega.Equal(300 * time.Second))
		})

		ginkgo.It("falls back to the default on a negative value", func() {
			path := write(`{"startup":{"maxStartingSeconds":-1}}`)
			gomega.Expect(LoadStartupConfig(ctx, path).MaxStarting(ctx, path)).
				To(gomega.Equal(300 * time.Second))
		})
	})

	ginkgo.Describe("terminateStarting", func() {
		var ctx context.Context

		ginkgo.BeforeEach(func() {
			ctx = context.Background()
		})

		// startChild runs a real process and returns it with a supervisor that
		// holds it. A real collaborator, not a mock: the thing under test kills by
		// pid, so a fake pid would prove nothing.
		startChild := func(session Session) (*supervisor, *exec.Cmd) {
			cmd := exec.Command("sleep", "60")
			gomega.Expect(cmd.Start()).To(gomega.Succeed())
			c := &child{
				id:      session.ID,
				pid:     cmd.Process.Pid,
				now:     libtime.NewCurrentDateTime(),
				session: session,
			}
			return &supervisor{
				now:      libtime.NewCurrentDateTime(),
				sessions: map[string]*child{session.ID: c},
				mutex:    sync.RWMutex{},
			}, cmd
		}

		// awaitExit reports whether the process left within timeout.
		awaitExit := func(cmd *exec.Cmd, timeout time.Duration) bool {
			done := make(chan struct{})
			go func() {
				_, _ = cmd.Process.Wait()
				close(done)
			}()
			select {
			case <-done:
				return true
			case <-time.After(timeout):
				return false
			}
		}

		ginkgo.It("terminates a child that never initialised", func() {
			s, cmd := startChild(Session{
				ID:        "stuck",
				State:     StateStarting,
				StartedAt: time.Now().Add(-10 * time.Minute),
			})
			defer func() { _ = cmd.Process.Kill() }()

			gomega.Expect(s.terminateStarting(ctx, 5*time.Minute)).To(gomega.Equal(1))
			gomega.Expect(awaitExit(cmd, 5*time.Second)).To(gomega.BeTrue(),
				"the stuck child was not terminated")
		})

		ginkgo.It("leaves a child that is inside the bound alone", func() {
			s, cmd := startChild(Session{
				ID:        "young",
				State:     StateStarting,
				StartedAt: time.Now().Add(-10 * time.Second),
			})
			defer func() { _ = cmd.Process.Kill() }()

			gomega.Expect(s.terminateStarting(ctx, 5*time.Minute)).To(gomega.Equal(0))
			gomega.Expect(awaitExit(cmd, 300*time.Millisecond)).To(gomega.BeFalse(),
				"a child inside the bound must not be killed")
		})

		ginkgo.It("leaves an idle child alone however old it is", func() {
			s, cmd := startChild(Session{
				ID:        "idle",
				State:     StateIdle,
				Model:     "deepseek-v4-flash-max",
				StartedAt: time.Now().Add(-10 * time.Hour),
			})
			defer func() { _ = cmd.Process.Kill() }()

			gomega.Expect(s.terminateStarting(ctx, 5*time.Minute)).To(gomega.Equal(0))
			gomega.Expect(awaitExit(cmd, 300*time.Millisecond)).To(gomega.BeFalse(),
				"an idle child must never be killed by the startup deadline")
		})

		ginkgo.It("leaves a starting child that has a model alone", func() {
			s, cmd := startChild(Session{
				ID:        "slow",
				State:     StateStarting,
				Model:     "deepseek-v4-flash-max",
				StartedAt: time.Now().Add(-10 * time.Minute),
			})
			defer func() { _ = cmd.Process.Kill() }()

			gomega.Expect(s.terminateStarting(ctx, 5*time.Minute)).To(gomega.Equal(0))
			gomega.Expect(awaitExit(cmd, 300*time.Millisecond)).To(gomega.BeFalse(),
				"a populated model proves the child did real work")
		})

		ginkgo.It("skips a session with no pid instead of signalling pid zero", func() {
			c := &child{
				id:      "no-pid",
				pid:     0,
				now:     libtime.NewCurrentDateTime(),
				session: Session{ID: "no-pid", State: StateStarting, StartedAt: time.Now().Add(-time.Hour)},
			}
			s := &supervisor{
				now:      libtime.NewCurrentDateTime(),
				sessions: map[string]*child{"no-pid": c},
				mutex:    sync.RWMutex{},
			}
			gomega.Expect(s.terminateStarting(ctx, time.Minute)).To(gomega.Equal(0))
			gomega.Expect(syscall.Kill(os.Getpid(), 0)).To(gomega.Succeed(),
				"the sweeper must still be running after a pid-0 session")
		})

		ginkgo.It("is a no-op when the deadline is disabled", func() {
			s, cmd := startChild(Session{
				ID:        "stuck",
				State:     StateStarting,
				StartedAt: time.Now().Add(-10 * time.Hour),
			})
			defer func() { _ = cmd.Process.Kill() }()

			gomega.Expect(s.terminateStarting(ctx, 0)).To(gomega.Equal(0))
			gomega.Expect(awaitExit(cmd, 300*time.Millisecond)).To(gomega.BeFalse())
		})
	})
})
