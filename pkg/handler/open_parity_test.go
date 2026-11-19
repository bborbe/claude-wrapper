// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
)

// serveOpenMCP sends one open_rows tools/call to the fleet-facing MCP face.
func serveOpenMCP(surface *pkg.OpenSurface, arguments string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"open_rows","arguments":%s}}`,
		arguments,
	)
	req := httptest.NewRequest(http.MethodPost, "/supervisor/mcp", strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(pkg.SessionHeader, "caller-1")
	server := &pkg.RosterToolServer{Roster: openFakeRoster{}, Open: surface}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

// openMCPResult is the result half of one tools/call reply.
type openMCPResult struct {
	Result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

var _ = Describe("Open REST/MCP parity", func() {
	It("AC4: returns byte-identical verdicts and makes identical calls", func() {
		// One vault directory, so page paths and every recorded request are
		// comparable between the two runs; the fakes are fresh per surface.
		dir := GinkgoT().TempDir()
		writeOpenTestCallerPages(dir)
		rest := newOpenTestFixture(dir)
		mcp := newOpenTestFixture(dir)

		restRec := serveOpen(rest.surface, openTestRequest(http.MethodPost, openTestCallerRows))
		Expect(restRec.Code).To(Equal(http.StatusOK))

		mcpRec := serveOpenMCP(mcp.surface, openTestCallerRows)
		Expect(mcpRec.Code).To(Equal(http.StatusOK))
		var result openMCPResult
		Expect(json.Unmarshal(mcpRec.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Result.IsError).To(BeFalse())
		Expect(result.Result.Content).To(HaveLen(1))

		Expect(result.Result.Content[0].Text).To(Equal(restRec.Body.String()),
			"the MCP text and the REST body must be the same bytes")
		Expect(mcp.headless.requests).To(Equal(rest.headless.requests))
		Expect(mcp.tabOpener.requests).To(Equal(rest.tabOpener.requests))
		Expect(mcp.scripts.calls).To(Equal(rest.scripts.calls))
	})
})

// openJumpRows is an agent create (one probe) and a live human jump (one pane
// activation).
const openJumpRows = `{"rows":[` +
	`{"kind":"task","vault":"v1","name":"Agent"},` +
	`{"kind":"task","vault":"v1","name":"Jump"}]}`

// openTickRows is the same batch with a `mode` field no row schema carries.
const openTickRows = `{"rows":[` +
	`{"kind":"task","vault":"v1","name":"Agent","mode":"tick"},` +
	`{"kind":"task","vault":"v1","name":"Jump","mode":"tick"}]}`

// newOpenJumpFixture writes the jump batch's pages and a live pane for Jump.
func newOpenJumpFixture(dir string) *openTestFixture {
	writeOpenTestPage(dir, "Agent", "role: agent", "mode: headless")
	liveID := openTestUUID(7)
	writeOpenTestPage(dir, "Jump",
		"role: human", "mode: interactive", "claude_session_id: "+liveID)
	f := newOpenTestFixture(dir)
	f.registry.live = []pkg.RegistryEntry{{PID: 42, SessionID: liveID}}
	f.panes.panes = map[int]string{42: "pane-9"}
	return f
}

var _ = Describe("Open caller mode", func() {
	DescribeTable("AC24: no request field can select tick mode",
		func(surface func(*pkg.OpenSurface, string) *httptest.ResponseRecorder) {
			// Without the extra field.
			plainDir := GinkgoT().TempDir()
			plain := newOpenJumpFixture(plainDir)
			plainRec := surface(plain.surface, openJumpRows)
			Expect(plainRec.Code).To(Equal(http.StatusOK))
			Expect(plain.scripts.probeCalls()).To(Equal(1))
			Expect(plain.tabs.activated).To(Equal([]string{"pane-9"}))

			// With `"mode": "tick"` on every row.
			tickDir := GinkgoT().TempDir()
			tick := newOpenJumpFixture(tickDir)
			tickRec := surface(tick.surface, openTickRows)
			Expect(tickRec.Code).To(Equal(http.StatusOK))
			Expect(tick.scripts.probeCalls()).To(Equal(1),
				"a request-carried mode must not skip the probe")
			Expect(tick.tabs.activated).To(Equal([]string{"pane-9"}),
				"a request-carried mode must not skip pane activation")
		},
		Entry("REST", func(surface *pkg.OpenSurface, body string) *httptest.ResponseRecorder {
			return serveOpen(surface, openTestRequest(http.MethodPost, body))
		}),
		Entry("open_rows", serveOpenMCP),
	)
})
