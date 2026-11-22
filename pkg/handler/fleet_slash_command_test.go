// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

type fakeFleetSlashSurface struct {
	report  pkg.FleetCommandReport
	err     error
	command string
	only    []string
}

func (f *fakeFleetSlashSurface) FleetSlashCommandReport(
	_ context.Context,
	command string,
	only []string,
) (pkg.FleetCommandReport, error) {
	f.command, f.only = command, only
	return f.report, f.err
}

func fleetRequest(method, host, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/1.0/fleet-slash-command", strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	return req
}

var _ = Describe("FleetSlashCommand handler", func() {
	It("returns 200 with the JSON report for a loopback POST", func() {
		surface := &fakeFleetSlashSurface{report: pkg.FleetCommandReport{
			Command: "/mcp reconnect tts",
			Results: []pkg.FleetCommandResult{{SessionID: "s1", Outcome: pkg.FleetDelivered}},
		}}
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(surface).ServeHTTP(
			rec,
			fleetRequest(http.MethodPost, "localhost",
				`{"command":"/mcp reconnect tts","sessions":["s1"]}`),
		)

		Expect(rec.Code).To(Equal(http.StatusOK))
		var got pkg.FleetCommandReport
		Expect(json.Unmarshal(rec.Body.Bytes(), &got)).To(Succeed())
		Expect(got.Results).To(HaveLen(1))
		Expect(got.Results[0].Outcome).To(Equal(pkg.FleetDelivered))
		Expect(surface.command).To(Equal("/mcp reconnect tts"))
		Expect(surface.only).To(ConsistOf("s1"))
	})

	It("returns 403 for a non-loopback caller", func() {
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(&fakeFleetSlashSurface{}).ServeHTTP(
			rec,
			fleetRequest(http.MethodPost, "example.com", `{"command":"/compact"}`),
		)
		Expect(rec.Code).To(Equal(http.StatusForbidden))
	})

	It("returns 405 for a non-POST method", func() {
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(&fakeFleetSlashSurface{}).ServeHTTP(
			rec, fleetRequest(http.MethodGet, "localhost", ""))
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
	})

	It("returns 415 for a non-JSON content-type", func() {
		req := httptest.NewRequest(
			http.MethodPost, "/api/1.0/fleet-slash-command", strings.NewReader(`{}`))
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(&fakeFleetSlashSurface{}).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusUnsupportedMediaType))
	})

	It("returns 400 for a malformed body", func() {
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(&fakeFleetSlashSurface{}).ServeHTTP(
			rec, fleetRequest(http.MethodPost, "localhost", `{not json`))
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("returns 400 when the command is missing", func() {
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(&fakeFleetSlashSurface{}).ServeHTTP(
			rec, fleetRequest(http.MethodPost, "localhost", `{}`))
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("returns 400 when the command is not allowlisted", func() {
		surface := &fakeFleetSlashSurface{err: pkg.ErrSlashCommandNotAllowed}
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(surface).ServeHTTP(
			rec,
			fleetRequest(http.MethodPost, "localhost", `{"command":"/mcp reconnect other"}`),
		)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("returns 500 when the surface errors", func() {
		surface := &fakeFleetSlashSurface{err: errors.New("boom")}
		rec := httptest.NewRecorder()
		handler.FleetSlashCommand(surface).ServeHTTP(
			rec,
			fleetRequest(http.MethodPost, "localhost", `{"command":"/compact"}`),
		)
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})
})
