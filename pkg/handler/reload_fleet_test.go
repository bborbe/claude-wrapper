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

type fakeReloadSurface struct {
	report pkg.ReloadReport
	err    error
	only   []string
}

func (f *fakeReloadSurface) ReloadFleetReport(_ context.Context, only []string) (pkg.ReloadReport, error) {
	f.only = only
	return f.report, f.err
}

func reloadRequest(method, host, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/1.0/reload-fleet", strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	return req
}

var _ = Describe("ReloadFleet handler", func() {
	It("returns 200 with the JSON report for a loopback POST", func() {
		surface := &fakeReloadSurface{report: pkg.ReloadReport{
			Results: []pkg.ReloadResult{{SessionID: "s1", Outcome: pkg.OutcomeReloaded}},
		}}
		rec := httptest.NewRecorder()
		handler.ReloadFleet(surface).ServeHTTP(rec, reloadRequest(http.MethodPost, "localhost", `{"sessions":["s1"]}`))

		Expect(rec.Code).To(Equal(http.StatusOK))
		var got pkg.ReloadReport
		Expect(json.Unmarshal(rec.Body.Bytes(), &got)).To(Succeed())
		Expect(got.Results).To(HaveLen(1))
		Expect(got.Results[0].Outcome).To(Equal(pkg.OutcomeReloaded))
		Expect(surface.only).To(ConsistOf("s1"))
	})

	It("returns 403 for a non-loopback caller", func() {
		rec := httptest.NewRecorder()
		handler.ReloadFleet(&fakeReloadSurface{}).ServeHTTP(
			rec, reloadRequest(http.MethodPost, "example.com", `{}`))
		Expect(rec.Code).To(Equal(http.StatusForbidden))
	})

	It("returns 405 for a non-POST method", func() {
		rec := httptest.NewRecorder()
		handler.ReloadFleet(&fakeReloadSurface{}).ServeHTTP(
			rec, reloadRequest(http.MethodGet, "localhost", ""))
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
	})

	It("returns 400 for a malformed body", func() {
		rec := httptest.NewRecorder()
		handler.ReloadFleet(&fakeReloadSurface{}).ServeHTTP(
			rec, reloadRequest(http.MethodPost, "localhost", `{not json`))
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("returns 415 for a non-JSON content-type", func() {
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/reload-fleet", strings.NewReader(`{}`))
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.ReloadFleet(&fakeReloadSurface{}).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusUnsupportedMediaType))
	})

	It("returns 200 for an empty body (every live session)", func() {
		surface := &fakeReloadSurface{report: pkg.ReloadReport{
			Results: []pkg.ReloadResult{{SessionID: "s1", Outcome: pkg.OutcomeReloaded}},
		}}
		rec := httptest.NewRecorder()
		handler.ReloadFleet(surface).ServeHTTP(rec, reloadRequest(http.MethodPost, "localhost", ""))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(surface.only).To(BeEmpty())
	})

	It("returns 500 when the surface errors", func() {
		surface := &fakeReloadSurface{err: errors.New("boom")}
		rec := httptest.NewRecorder()
		handler.ReloadFleet(surface).ServeHTTP(rec, reloadRequest(http.MethodPost, "localhost", `{}`))
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})
})
