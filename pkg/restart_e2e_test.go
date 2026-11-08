// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// restartSuite proves the claim this whole change exists for, on REAL processes:
// a daemon is started, a child is spawned and left mid-turn, the daemon is killed
// with SIGTERM, and a second daemon is started over the same state directory and
// asked to carry on.
//
// It builds and runs the actual binary, because every part of the claim is about
// process lifetime and file descriptors: a pipe dies with its parent, a FIFO does
// not, and no in-process test can tell the two apart. It is opt-in
// (CLAUDE_WRAPPER_RESTART_E2E=1) so `make test` stays hermetic; the task that
// introduced it runs it explicitly.
var _ = ginkgo.Describe("restart across a daemon death", ginkgo.Label("e2e"), func() {
	var (
		dir      string
		stateDir string
		binary   string
		child    string
		port     int
	)

	ginkgo.BeforeEach(func() {
		if os.Getenv("CLAUDE_WRAPPER_RESTART_E2E") != "1" {
			ginkgo.Skip("set CLAUDE_WRAPPER_RESTART_E2E=1 to run the real-process restart proof")
		}
		dir = ginkgo.GinkgoT().TempDir()
		stateDir = filepath.Join(dir, "state")
		binary = filepath.Join(dir, "claude-wrapper")

		// The child speaks just enough of the protocol to drive state: init once,
		// then one open-and-closing turn per stdin line. The second of sleep inside
		// the turn is what creates the mid-turn window the restart lands in.
		child = filepath.Join(dir, "fake-claude.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"fake-model\",\"permissionMode\":\"auto\"}'\n" +
			"while IFS= read -r line; do\n" +
			"  printf '%s\\n' '{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\"}}'\n" +
			"  sleep 6\n" +
			"  printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"model\":\"fake-model\"}}'\n" +
			"  printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\n" +
			"done\n"
		gomega.Expect(os.WriteFile(child, []byte(script), 0o700)).To(gomega.Succeed())

		build := exec.Command("go", "build", "-o", binary, ".")
		build.Dir = repoRoot()
		build.Stdout = os.Stderr
		build.Stderr = os.Stderr
		gomega.Expect(build.Run()).To(gomega.Succeed())

		port = freePort()
	})

	startDaemon := func() *exec.Cmd {
		cmd := exec.Command(
			binary,
			"-listen", fmt.Sprintf("localhost:%d", port),
			"-claude-bin", child,
			"-attention-url", "http://localhost:1",
			"-drain-bound", "2s",
		)
		cmd.Env = append(os.Environ(), "CLAUDE_WRAPPER_STATE_DIR="+stateDir)
		logFile, err := os.Create(filepath.Join(dir, fmt.Sprintf("daemon-%d.log", time.Now().UnixNano())))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			_ = cmd.Process.Signal(syscall.SIGKILL)
			_, _ = cmd.Process.Wait()
			_ = logFile.Close()
		})
		waitHealthy(port)
		return cmd
	}

	ginkgo.It("keeps the child, adopts it, and finishes its turn", func() {
		first := startDaemon()

		id := spawnSession(port, dir)
		gomega.Eventually(func() string { return sessionState(port, id) }, "10s").
			Should(gomega.Equal("idle"))

		// A turn is written and the daemon is killed while the child is inside it.
		postJSON(port, "/api/1.0/sessions/"+id+"/message", `{"text":"hello"}`)
		gomega.Eventually(func() string { return sessionState(port, id) }, "5s").
			Should(gomega.Equal("generating"))

		childPID := recordPID(stateDir, id)
		gomega.Expect(childPID).To(gomega.BeNumerically(">", 0))

		// SC1: one SIGTERM ends the daemon, within the drain bound and without a
		// SIGKILL. A daemon that ignored the signal is the defect this replaces.
		gomega.Expect(first.Process.Signal(syscall.SIGTERM)).To(gomega.Succeed())
		exited := make(chan error, 1)
		go func() { exited <- first.Wait() }()
		gomega.Eventually(exited, "30s").Should(gomega.Receive(gomega.BeNil()))

		// SC2: the child outlived the daemon that started it, and still holds its
		// own stdin.
		gomega.Expect(processAlive(childPID)).To(gomega.BeTrue())

		// SC3: a fresh daemon over the same state adopts it and serves it again.
		second := startDaemon()
		gomega.Expect(sessionState(port, id)).To(gomega.Equal("generating"))
		gomega.Eventually(func() string { return sessionState(port, id) }, "10s").
			Should(gomega.Equal("idle"))

		// SC4: the same child answers a further turn — the session did not restart,
		// it continued. A resumed conversation would carry a different child pid.
		gomega.Expect(recordPID(stateDir, id)).To(gomega.Equal(childPID))
		postJSON(port, "/api/1.0/sessions/"+id+"/message", `{"text":"again"}`)
		// Wait for the turn to START before waiting for it to end. Without this the
		// idle check below is satisfied instantly by the PREVIOUS turn's state, and
		// the result-event assertion runs while the child is still mid-turn — the
		// second turn's `message_start` is on disk but its `result` is not yet.
		gomega.Eventually(func() string { return sessionState(port, id) }, "10s").
			Should(gomega.Equal("generating"))
		gomega.Eventually(func() string { return sessionState(port, id) }, "10s").
			Should(gomega.Equal("idle"))

		// Both turns are recorded in ONE event file, in one continuous sequence —
		// which is what a reconnecting subscriber resumes against. Asserting on the
		// two result events rather than a line count: the count depends on how many
		// events landed on each side of the restart, and that is timing, not the
		// property under test.
		events := readEvents(stateDir, id)
		gomega.Expect(strings.Count(events, `"type":"result"`)).To(gomega.Equal(2),
			"event file:\n"+events)

		gomega.Expect(second.Process.Signal(syscall.SIGTERM)).To(gomega.Succeed())
		_ = second.Wait()
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	})
})

// repoRoot resolves the module root from the package directory the test runs in.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Dir(dir)
}

// freePort asks the kernel for an unused loopback port.
func freePort() int {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 18089
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 18089
	}
	return addr.Port
}

// waitHealthy blocks until the daemon answers its health route.
func waitHealthy(port int) {
	gomega.Eventually(func() error {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/healthz", port))
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}, "20s").Should(gomega.Succeed())
}

// spawnSession creates one session and returns its id.
func spawnSession(port int, cwd string) string {
	body := postJSON(port, "/api/1.0/sessions", fmt.Sprintf(`{"cwd":%q}`, cwd))
	var payload struct {
		ID string `json:"id"`
	}
	gomega.Expect(json.Unmarshal(body, &payload)).To(gomega.Succeed())
	gomega.Expect(payload.ID).NotTo(gomega.BeEmpty())
	return payload.ID
}

// sessionState reads the state the daemon reports for one session.
func sessionState(port int, id string) string {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/api/1.0/sessions/%s", port, id))
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var payload struct {
		State string `json:"state"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&payload); decodeErr != nil {
		return ""
	}
	return payload.State
}

// postJSON sends one JSON body and returns the response body.
func postJSON(port int, path string, body string) []byte {
	resp, err := http.Post(
		fmt.Sprintf("http://localhost:%d%s", port, path),
		"application/json",
		strings.NewReader(body),
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() { _ = resp.Body.Close() }()
	encoded, err := io.ReadAll(resp.Body)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return encoded
}

// recordPID reads the child pid from the session's durable record.
func recordPID(stateDir string, id string) int {
	encoded, err := os.ReadFile(filepath.Join(stateDir, "sessions", id, "record.json"))
	if err != nil {
		return 0
	}
	var record struct {
		ChildPID int `json:"child_pid"`
	}
	if decodeErr := json.Unmarshal(encoded, &record); decodeErr != nil {
		return 0
	}
	return record.ChildPID
}

// readEvents returns everything the child has written to its event file.
func readEvents(stateDir string, id string) string {
	encoded, err := os.ReadFile(filepath.Join(stateDir, "sessions", id, "events.jsonl"))
	if err != nil {
		return ""
	}
	return string(encoded)
}
