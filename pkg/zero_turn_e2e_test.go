// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// zeroTurnSuite reproduces, on REAL processes, the close that loses a worker's
// work: a headless child whose first turn ends with a bare `result` — no
// assistant message at all — which is what a stream that died mid-flight looks
// like (measured 2026-11-22: `data-flow-incomplete codes=[6,10]`).
//
// On the pre-fix daemon the child is SIGKILLed one second after the prompt and
// its task file is never touched. The fix re-sends the same turn once, so the
// second attempt is the one that produces output.
//
// It builds and runs the actual binary because the claim is about process
// lifetime: only a real child can be killed. Opt-in (CLAUDE_WRAPPER_ZERO_TURN_E2E=1)
// so `make test` stays hermetic. Point CLAUDE_WRAPPER_BIN at a prebuilt binary
// to run the same spec against another revision — that is how the defect is
// reproduced against master.
var _ = ginkgo.Describe("a turn that closes with no assistant message", ginkgo.Label("e2e"), func() {
	var (
		dir       string
		stateDir  string
		binary    string
		child     string
		attempts  string
		port      int
		daemonLog string
	)

	ginkgo.BeforeEach(func() {
		if os.Getenv("CLAUDE_WRAPPER_ZERO_TURN_E2E") != "1" {
			ginkgo.Skip("set CLAUDE_WRAPPER_ZERO_TURN_E2E=1 to run the real-process zero-turn proof")
		}
		dir = ginkgo.GinkgoT().TempDir()
		stateDir = filepath.Join(dir, "state")
		attempts = filepath.Join(dir, "attempts.txt")
		daemonLog = filepath.Join(dir, "daemon.log")

		// The child reproduces the observed shape exactly: it announces itself,
		// then the FIRST turn ends with a bare result and no assistant message.
		// Every later turn answers normally, which is what makes the retry
		// observable rather than assumed.
		child = filepath.Join(dir, "fake-claude.sh")
		script := "#!/bin/sh\n" +
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"fake-model\",\"permissionMode\":\"auto\"}'\n" +
			"n=0\n" +
			"while IFS= read -r line; do\n" +
			"  n=$((n+1))\n" +
			"  printf '%s\\n' \"attempt $n\" >> " + attempts + "\n" +
			"  if [ \"$n\" -eq 1 ]; then\n" +
			"    printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true}'\n" +
			"  else\n" +
			"    printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"model\":\"fake-model\"}}'\n" +
			"    printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\n" +
			"  fi\n" +
			"done\n"
		gomega.Expect(os.WriteFile(child, []byte(script), 0o700)).To(gomega.Succeed())

		binary = os.Getenv("CLAUDE_WRAPPER_BIN")
		if binary == "" {
			binary = filepath.Join(dir, "claude-wrapper")
			build := exec.Command("go", "build", "-o", binary, ".")
			build.Dir = repoRoot()
			build.Stdout = os.Stderr
			build.Stderr = os.Stderr
			gomega.Expect(build.Run()).To(gomega.Succeed())
		}

		port = freePort()

		cmd := exec.Command(
			binary,
			"-listen", fmt.Sprintf("localhost:%d", port),
			"-claude-bin", child,
			"-attention-url", "http://localhost:1",
			"-drain-bound", "2s",
		)
		cmd.Env = append(os.Environ(), "CLAUDE_WRAPPER_STATE_DIR="+stateDir)
		logFile, err := os.Create(daemonLog)
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
	})

	attemptCount := func() int {
		raw, err := os.ReadFile(attempts)
		if err != nil {
			return 0
		}
		return len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
	}

	sessionAssistantTurns := func(id string) int {
		resp, err := httpGet(port, "/api/1.0/sessions/"+id)
		if err != nil {
			return -1
		}
		var payload struct {
			AssistantTurns int `json:"assistant_turns"`
		}
		if decodeErr := json.Unmarshal(resp, &payload); decodeErr != nil {
			return -1
		}
		return payload.AssistantTurns
	}

	ginkgo.It("re-sends the turn so the child produces output instead of dying silently", func() {
		id := spawnHeadlessSession(port, dir)
		postJSON(port, "/api/1.0/sessions/"+id+"/message", `{"text":"do the thing"}`)

		// The retry is the claim: the child must see a SECOND turn. Without the
		// fix it sees exactly one and is killed, which is the stranded worker this
		// task exists for.
		gomega.Eventually(attemptCount, "20s").Should(gomega.Equal(2))

		// And the second attempt is the one that produced something, so the
		// session carries a non-zero turn count rather than the zero-turn close.
		gomega.Eventually(func() int { return sessionAssistantTurns(id) }, "10s").
			Should(gomega.BeNumerically(">", 0))

		// The daemon's own record must say what happened, not merely that the turn
		// closed — that wording is identical for a healthy end.
		logRaw, err := os.ReadFile(daemonLog)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(logRaw)).To(gomega.ContainSubstring("re-sending the same turn"))
	})
})

// spawnHeadlessSession creates one headless session and returns its id. The mode
// is explicit: the default is interactive, and only a headless worker is subject
// to the turn-end stop this spec exercises.
func spawnHeadlessSession(port int, cwd string) string {
	body := postJSON(port, "/api/1.0/sessions", fmt.Sprintf(`{"cwd":%q,"mode":"headless"}`, cwd))
	var payload struct {
		ID string `json:"id"`
	}
	gomega.Expect(json.Unmarshal(body, &payload)).To(gomega.Succeed())
	gomega.Expect(payload.ID).NotTo(gomega.BeEmpty())
	return payload.ID
}

// httpGet reads one daemon route and returns its body.
func httpGet(port int, path string) ([]byte, error) {
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d%s", port, path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}
