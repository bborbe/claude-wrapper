// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

// permissionTierTestScript is the probe path the fixtures wire. It matches no real
// file: the runner is a fake, and a real path would invite a spec that silently
// depends on the operator's marketplace clone.
const permissionTierTestScript = "/scripts/permission-tier.py"

// fakeTierScripts answers the permission-tier probe with a fixed result.
type fakeTierScripts struct {
	result pkg.ScriptResult
	calls  [][]string
}

func (f *fakeTierScripts) RunScript(
	_ context.Context, name string, args ...string,
) (pkg.ScriptResult, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.result, nil
}

func (f *fakeTierScripts) RunScriptWithStdin(
	_ context.Context, _ string, name string, args ...string,
) (pkg.ScriptResult, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.result, nil
}

func operatorTierProbe() pkg.ScriptResult {
	return pkg.ScriptResult{
		ExitCode: 0,
		Stdout:   `{"tier":"operator","form":"cluster-unknown-env","reason":"operator-only form: cluster-unknown-env"}`,
	}
}

func managerTierProbe() pkg.ScriptResult {
	return pkg.ScriptResult{ExitCode: 0, Stdout: `{"tier":"manager","form":null,"reason":"manager"}`}
}

// SC1 — the REST `answer` route was the third unguarded answer surface. A manager
// that reached a park over REST settled an operator-tier gate with no tier check.
// The route now applies the same guard as answer_permission, before the answer
// lands.
var _ = ginkgo.Describe("REST answer route permission-tier guard", func() {
	var (
		scripts *fakeTierScripts
		guard   pkg.PermissionTierGuard
	)

	ginkgo.BeforeEach(func() {
		scripts = &fakeTierScripts{result: managerTierProbe()}
		guard = pkg.PermissionTierGuard{
			Scripts:              scripts,
			PermissionTierScript: permissionTierTestScript,
		}
	})

	// postAnswer drives the answer route with the wired guard.
	postAnswer := func(sup *fakeSupervisor, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/sessions/a/answer", strings.NewReader(body))
		handler.Session(sup, nil, pkg.NewSessionLocks(), guard).ServeHTTP(recorder, req)
		return recorder
	}

	ginkgo.It("refuses an operator-tier park before the answer lands", func() {
		scripts.result = operatorTierProbe()
		sup := &fakeSupervisor{question: &pkg.Question{
			RequestID: "r1",
			Kind:      pkg.AnswerKindPermission,
			ToolName:  "Bash",
			Input:     map[string]any{"command": "kubectl apply -f x.yaml"},
		}}

		recorder := postAnswer(sup, `{"request_id":"r1","behavior":"allow"}`)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusForbidden))
		gomega.Expect(recorder.Body.String()).To(gomega.ContainSubstring("operator-tier"))
		gomega.Expect(sup.answerCalls).To(gomega.Equal(0))
	})

	// The positive half: a guard that refused every answer would satisfy the
	// refusal assertion above and break the product.
	ginkgo.It("answers a manager-tier park", func() {
		sup := &fakeSupervisor{question: &pkg.Question{
			RequestID: "r1",
			Kind:      pkg.AnswerKindPermission,
			ToolName:  "Bash",
			Input:     map[string]any{"command": "ls -la"},
		}}

		recorder := postAnswer(sup, `{"request_id":"r1","behavior":"allow"}`)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))
		gomega.Expect(sup.answerCalls).To(gomega.Equal(1))
	})

	ginkgo.It("refuses when the request_id does not match the parked prompt", func() {
		sup := &fakeSupervisor{question: &pkg.Question{
			RequestID: "other",
			Kind:      pkg.AnswerKindPermission,
			ToolName:  "Bash",
			Input:     map[string]any{"command": "ls -la"},
		}}

		recorder := postAnswer(sup, `{"request_id":"r1","behavior":"allow"}`)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusForbidden))
		gomega.Expect(recorder.Body.String()).To(gomega.ContainSubstring("does not match the parked prompt"))
		gomega.Expect(sup.answerCalls).To(gomega.Equal(0))
	})

	// An unwired guard refuses: the daemon cannot prove an operator-tier gate is
	// safe to answer, so it must not answer one.
	ginkgo.It("refuses an operator-tier park when the guard is unwired", func() {
		scripts.result = operatorTierProbe()
		guard = pkg.PermissionTierGuard{}
		sup := &fakeSupervisor{question: &pkg.Question{
			RequestID: "r1",
			Kind:      pkg.AnswerKindPermission,
			ToolName:  "Bash",
			Input:     map[string]any{"command": "kubectl apply -f x.yaml"},
		}}

		recorder := postAnswer(sup, `{"request_id":"r1","behavior":"allow"}`)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusForbidden))
		gomega.Expect(sup.answerCalls).To(gomega.Equal(0))
	})

	// The route with no park still answers: the gate must not turn every answer
	// into a refusal.
	ginkgo.It("answers when the session holds no park", func() {
		sup := &fakeSupervisor{}

		recorder := postAnswer(sup, `{"request_id":"r1","behavior":"allow"}`)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))
		gomega.Expect(sup.answerCalls).To(gomega.Equal(1))
	})
})
