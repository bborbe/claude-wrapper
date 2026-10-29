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
	mutex      sync.Mutex
	live       chan Event
	emitted    []Event
	sent       []string
	reply      string
	neverClose bool
}

func (s *toolSupervisor) Spawn(_ context.Context, options SpawnOptions) (*Session, error) {
	return &Session{ID: "s1", State: StateStarting, Model: options.Model}, nil
}

func (s *toolSupervisor) List(context.Context) ([]Session, error) {
	return []Session{{ID: "s1", State: StateIdle}}, nil
}

func (s *toolSupervisor) Get(context.Context, string) (*Session, error) {
	return &Session{ID: "s1", State: StateIdle}, nil
}

func (s *toolSupervisor) Answer(context.Context, string, string, string) error { return nil }

func (s *toolSupervisor) RegisterQuestion(context.Context, string, Question) error { return nil }

func (s *toolSupervisor) PollAnswer(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (s *toolSupervisor) Stop(context.Context, string) error { return nil }

func (s *toolSupervisor) Send(_ context.Context, _ string, text string) error {
	s.mutex.Lock()
	index := len(s.sent)
	s.sent = append(s.sent, text)
	s.mutex.Unlock()
	s.emit(Event{"type": "assistant"})
	if s.neverClose {
		return nil
	}
	s.emit(Event{"type": "result", "result_index": float64(index), "result": s.reply})
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

// post drives one JSON-RPC message through the real HTTP handler, so the spec
// exercises the wire shape rather than an internal method.
func post(server *MCPToolServer, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
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
		}
		server = &MCPToolServer{
			Supervisor:  supervisor,
			Locks:       NewSessionLocks(),
			TurnTimeout: 2 * time.Second,
		}
	})

	ginkgo.It("lists the five tools a Codex client needs", func() {
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
			ToolSpawnSession,
			ToolSendMessage,
			ToolListSessions,
			ToolGetSession,
			ToolKillSession,
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
})
