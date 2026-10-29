// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// toolSupervisor answers each Send with a `result` event carrying the reply,
// so a spec observes the tool's return rule without a real child.
type toolSupervisor struct {
	mutex        sync.Mutex
	live         chan Event
	emitted      []Event
	sent         []string
	reply        string
	neverClose   bool
	errorReply   bool
	spawnedMode  string
	state        State
	pendingTurns int
	// permissionModes records every mode a set_permission_mode call asked for.
	permissionModes []string
}

func (s *toolSupervisor) Spawn(_ context.Context, options SpawnOptions) (*Session, error) {
	s.mutex.Lock()
	s.spawnedMode = options.PermissionMode
	s.mutex.Unlock()
	return &Session{ID: "s1", State: StateStarting, Model: options.Model}, nil
}

func (s *toolSupervisor) List(context.Context) ([]Session, error) {
	return []Session{{ID: "s1", State: StateIdle}}, nil
}

func (s *toolSupervisor) Get(context.Context, string) (*Session, error) {
	return &Session{ID: "s1", State: s.state, PendingTurns: s.pendingTurns}, nil
}

func (s *toolSupervisor) Answer(context.Context, string, string, Answer) error { return nil }

func (s *toolSupervisor) RegisterQuestion(context.Context, string, Question) error { return nil }

func (s *toolSupervisor) ForgetQuestion(context.Context, string, string) error { return nil }

func (s *toolSupervisor) PollAnswer(context.Context, string) (PendingAnswer, error) {
	return PendingAnswer{}, nil
}

func (s *toolSupervisor) Stop(context.Context, string) error { return nil }

func (s *toolSupervisor) SetPermissionMode(_ context.Context, _ string, mode string) (string, error) {
	s.mutex.Lock()
	s.permissionModes = append(s.permissionModes, mode)
	s.mutex.Unlock()
	return mode, nil
}

func (s *toolSupervisor) Send(_ context.Context, _ string, text string) error {
	s.mutex.Lock()
	index := len(s.sent)
	s.sent = append(s.sent, text)
	s.mutex.Unlock()
	s.emit(Event{"type": "assistant"})
	if s.neverClose {
		return nil
	}
	s.emit(Event{
		"type":         "result",
		"result_index": float64(index),
		"result":       s.reply,
		"is_error":     s.errorReply,
	})
	return nil
}

func (s *toolSupervisor) emit(event Event) {
	s.mutex.Lock()
	s.emitted = append(s.emitted, event)
	s.mutex.Unlock()
	s.live <- event
}

func (s *toolSupervisor) Subscribe(context.Context, string) ([]Event, <-chan Event, func(), error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	replay := make([]Event, len(s.emitted))
	copy(replay, s.emitted)
	return replay, s.live, func() {}, nil
}

// loopbackHost is what a real client sends. httptest.NewRequest defaults Host to
// "example.com", which the loopback guard rejects by design.
const loopbackHost = "127.0.0.1:18090"

// post drives one JSON-RPC message through the real HTTP handler, so the spec
// exercises the wire shape rather than an internal method.
func post(server *MCPToolServer, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Host = loopbackHost
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	return recorder
}

func decode(recorder *httptest.ResponseRecorder) map[string]any {
	var payload map[string]any
	gomega.Expect(json.Unmarshal(recorder.Body.Bytes(), &payload)).To(gomega.Succeed())
	return payload
}

var _ = ginkgo.Describe("MCPToolServer", func() {
	var supervisor *toolSupervisor
	var server *MCPToolServer

	ginkgo.BeforeEach(func() {
		supervisor = &toolSupervisor{
			live:  make(chan Event, 64),
			reply: "SPIKE-OK",
			state: StateIdle,
		}
		server = &MCPToolServer{
			Supervisor:  supervisor,
			Locks:       NewSessionLocks(),
			TurnTimeout: 2 * time.Second,
		}
	})

	ginkgo.It("lists every tool a Codex client needs", func() {
		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))
		payload := decode(recorder)
		result, ok := payload["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		tools, ok := result["tools"].([]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			entry, ok := tool.(map[string]any)
			gomega.Expect(ok).To(gomega.BeTrue())
			name, ok := entry["name"].(string)
			gomega.Expect(ok).To(gomega.BeTrue())
			names = append(names, name)
		}
		gomega.Expect(names).To(gomega.Equal([]string{
			string(ToolSpawnSession),
			string(ToolSendMessage),
			string(ToolListSessions),
			string(ToolGetSession),
			string(ToolKillSession),
			string(ToolGetPending),
			string(ToolAnswerPending),
			string(ToolSetModel),
			string(ToolSetPermissionMode),
		}))
	})

	// The client's own version is echoed: a client handed a version it did not
	// ask for may refuse the session outright.
	ginkgo.It("echoes the client's protocol version on initialize", func() {
		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"initialize",
			"params":{"protocolVersion":"2025-06-18"}}`)

		payload := decode(recorder)
		result, ok := payload["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["protocolVersion"]).To(gomega.Equal("2025-06-18"))
	})

	// A notification carries no id and must never be answered; answering one is
	// a protocol violation the client rejects.
	ginkgo.It("acknowledges a notification without a body", func() {
		recorder := post(server, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusAccepted))
		gomega.Expect(recorder.Body.Len()).To(gomega.Equal(0))
	})

	// The whole point of the tool: the reply, not an acknowledgement.
	ginkgo.It("returns the assistant text from the child's result event", func() {
		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"send_message",
			"arguments":{"session_id":"s1","text":"say hi"}}}`)

		payload := decode(recorder)
		result, ok := payload["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["isError"]).To(gomega.BeFalse())
		content, ok := result["content"].([]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		first, ok := content[0].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(first["text"]).To(gomega.Equal("SPIKE-OK"))
		gomega.Expect(supervisor.sent).To(gomega.Equal([]string{"say hi"}))
	})

	// A turn that never closes must surface as a distinguishable error rather
	// than as a success, which is what the timeout exists for.
	ginkgo.It("reports a turn that never closes as a tool error", func() {
		supervisor.neverClose = true
		server.TurnTimeout = 50 * time.Millisecond

		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"send_message",
			"arguments":{"session_id":"s1","text":"hang"}}}`)

		payload := decode(recorder)
		result, ok := payload["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["isError"]).To(gomega.BeTrue())
	})

	ginkgo.It("answers an unknown method with a JSON-RPC error", func() {
		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"no/such/method"}`)

		payload := decode(recorder)
		rpcErr, ok := payload["error"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(rpcErr["code"]).To(gomega.BeNumerically("==", -32601))
	})
	// spawn must carry the permissive phase-1 default through to the child; the
	// crutch is the whole reason plain turns stay plain.
	ginkgo.It("spawns with the permissive phase-1 default", func() {
		post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"spawn_session","arguments":{"cwd":"/tmp"}}}`)

		gomega.Expect(supervisor.spawnedMode).To(gomega.Equal(string(defaultPermissionMode)))
	})

	// A typo'd mode handed to Spawn unchecked would start a child that behaves
	// differently from what the caller asked for, silently.
	ginkgo.It("rejects an unknown permission_mode", func() {
		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"spawn_session","arguments":{"permission_mode":"nonsense"}}}`)

		result, ok := decode(recorder)["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["isError"]).To(gomega.BeTrue())
		gomega.Expect(supervisor.spawnedMode).To(gomega.BeEmpty())
	})

	ginkgo.It("lists, gets and kills a session", func() {
		for _, call := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_session","arguments":{"session_id":"s1"}}}`,
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"kill_session","arguments":{"session_id":"s1"}}}`,
		} {
			result, ok := decode(post(server, call))["result"].(map[string]any)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(result["isError"]).To(gomega.BeFalse())
		}
	})

	// A turn written while a sequence holds the child would have its result read
	// as the current step's close, firing the next step early.
	ginkgo.It("refuses a turn while a sequence holds the session", func() {
		release, ok := server.Locks.TryAcquire("s1")
		gomega.Expect(ok).To(gomega.BeTrue())
		defer release()

		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"send_message","arguments":{"session_id":"s1","text":"x"}}}`)

		result, ok := decode(recorder)["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["isError"]).To(gomega.BeTrue())
	})

	// A child that reports its turn as failed must not read as an answer.
	ginkgo.It("reports a failed turn as a tool error", func() {
		supervisor.errorReply = true

		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"send_message","arguments":{"session_id":"s1","text":"x"}}}`)

		result, ok := decode(recorder)["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["isError"]).To(gomega.BeTrue())
	})

	ginkgo.It("reports an unknown tool name as a tool error", func() {
		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"no_such_tool","arguments":{}}}`)

		result, ok := decode(recorder)["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["isError"]).To(gomega.BeTrue())
	})

	// DNS rebinding: the attacker's page resolves to 127.0.0.1 and its Origin is
	// its own hostname. Rejecting a non-loopback Origin is what stops it driving
	// spawn_session with every gate disabled.
	ginkgo.It("rejects a non-loopback Origin", func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		request.Host = loopbackHost
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://attacker.example")
		server.ServeHTTP(recorder, request)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusForbidden))
	})

	// A text/plain POST is a CORS simple request and skips preflight, so the
	// body type must be checked rather than assumed.
	ginkgo.It("rejects a non-JSON Content-Type", func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		request.Host = loopbackHost
		request.Header.Set("Content-Type", "text/plain")
		server.ServeHTTP(recorder, request)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusUnsupportedMediaType))
	})

	ginkgo.It("answers GET with 405 and DELETE with 200", func() {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(method, "/mcp", nil)
			request.Host = loopbackHost
			server.ServeHTTP(recorder, request)
			want := http.StatusMethodNotAllowed
			if method == http.MethodDelete {
				want = http.StatusOK
			}
			gomega.Expect(recorder.Code).To(gomega.Equal(want))
		}
	})

	// A server wired without its collaborators must say so, not panic on the
	// first send_message.
	ginkgo.It("answers 503 when not fully wired", func() {
		recorder := httptest.NewRecorder()
		(&MCPToolServer{}).ServeHTTP(recorder,
			httptest.NewRequest(http.MethodPost, "/mcp", nil))

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusServiceUnavailable))
	})
	// The Host header is what a rebinding page cannot forge: it re-resolves the
	// name but the header still carries the attacker's hostname.
	ginkgo.It("rejects a non-loopback Host", func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		request.Host = "attacker.example"
		request.Header.Set("Content-Type", "application/json")
		server.ServeHTTP(recorder, request)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusForbidden))
	})

	// A POST with no Content-Type is also a CORS simple request, so tolerating
	// it would reopen the preflight bypass.
	ginkgo.It("rejects an absent Content-Type", func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		request.Host = loopbackHost
		server.ServeHTTP(recorder, request)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusUnsupportedMediaType))
	})

	// Truncating silently would surface as a -32700 parse error the client
	// cannot tell from malformed JSON.
	ginkgo.It("rejects an oversized body", func() {
		huge := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":"` +
			strings.Repeat("x", maxMCPBody) + `"}`
		recorder := post(server, huge)

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusRequestEntityTooLarge))
	})

	// An explicit mode must reach the child unchanged.
	ginkgo.It("passes an explicit permission_mode through", func() {
		post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"spawn_session",
			"arguments":{"permission_mode":"acceptEdits"}}}`)

		gomega.Expect(supervisor.spawnedMode).To(gomega.Equal("acceptEdits"))
	})

	// The lock excludes writers, but not a turn already open on the child — the
	// timeout path releases while the child is still generating.
	ginkgo.It("refuses a turn while the child already has one open", func() {
		supervisor.pendingTurns = 1

		recorder := post(server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
			"params":{"name":"send_message","arguments":{"session_id":"s1","text":"x"}}}`)

		result, ok := decode(recorder)["result"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(result["isError"]).To(gomega.BeTrue())
		gomega.Expect(supervisor.sent).To(gomega.BeEmpty())
	})
})
