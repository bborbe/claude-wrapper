// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// rosterForServer builds a server over one own row and one rehydrated row.
func rosterForServer() *RosterToolServer {
	supervisor := &rosterSupervisor{sessions: []Session{
		{ID: "own-session", Cwd: "/tmp", StartedAt: time.Now()},
	}}
	ledger := &rosterLedger{rows: []LedgerRow{
		{SessionID: "foreignsession01", AgentID: "agent_1", Label: "elsewhere"},
	}}
	liveness := &rosterLiveness{live: map[string]struct{}{"foreignsession01": {}}}
	return &RosterToolServer{
		Roster:     NewRoster(supervisor, ledger, liveness, nil, nil),
		Supervisor: supervisor,
	}
}

// postRPC sends one JSON-RPC body, with the session header when given.
func postRPC(
	server *RosterToolServer,
	body string,
	sessionID string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/supervisor/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "localhost"
	if sessionID != "" {
		request.Header.Set(SessionHeader, sessionID)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

var _ = ginkgo.Describe("RosterToolServer", func() {
	ginkgo.It("refuses a request with no session header, naming the refusal", func() {
		recorder := postRPC(rosterForServer(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")

		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusBadRequest))
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		gomega.Expect(json.Unmarshal(recorder.Body.Bytes(), &body)).To(gomega.Succeed())
		// A named code, never a defaulted session: an unattributed call would be
		// recorded against a session that does not exist.
		gomega.Expect(body.Error.Code).To(gomega.Equal("missing_session_header"))
		gomega.Expect(body.Error.Message).To(gomega.ContainSubstring("X-Claude-Code-Session-Id"))
	})

	ginkgo.It("lists exactly the three roster tools", func() {
		recorder := postRPC(
			rosterForServer(),
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			"caller-session",
		)
		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))

		var body struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		gomega.Expect(json.Unmarshal(recorder.Body.Bytes(), &body)).To(gomega.Succeed())
		names := make([]string, 0, len(body.Result.Tools))
		for _, tool := range body.Result.Tools {
			names = append(names, tool.Name)
		}
		gomega.Expect(names).To(gomega.ConsistOf("list_agents", "agent_status", "stop_agent"))
	})

	ginkgo.It("returns a foreign worker alongside its own", func() {
		recorder := postRPC(
			rosterForServer(),
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_agents","arguments":{}}}`,
			"caller-session",
		)
		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))
		gomega.Expect(recorder.Body.String()).To(gomega.ContainSubstring("foreignsession01"))
		gomega.Expect(recorder.Body.String()).To(gomega.ContainSubstring(`\"rehydrated\":true`))
	})

	ginkgo.It("refuses stop_agent on a session this wrapper did not spawn", func() {
		recorder := postRPC(
			rosterForServer(),
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stop_agent","arguments":{"agent_id":"agent_1~foreigns"}}}`,
			"caller-session",
		)
		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))

		var body struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		gomega.Expect(json.Unmarshal(recorder.Body.Bytes(), &body)).To(gomega.Succeed())
		gomega.Expect(body.Result.IsError).To(gomega.BeTrue())
		gomega.Expect(body.Result.Content[0].Text).
			To(gomega.ContainSubstring("acts only on a session this wrapper spawned"))
	})

	ginkgo.It("reports an unknown agent id as a tool error, not a transport fault", func() {
		recorder := postRPC(
			rosterForServer(),
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_status","arguments":{"agent_id":"nobody"}}}`,
			"caller-session",
		)
		gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))
		// isError is a field of the RESULT, so it is not escaped the way the
		// nested row JSON is.
		gomega.Expect(recorder.Body.String()).To(gomega.ContainSubstring(`"isError":true`))
		gomega.Expect(recorder.Body.String()).To(gomega.ContainSubstring("unknown agent"))
	})
})
