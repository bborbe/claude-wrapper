// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
)

func TestMain(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Main Suite")
}

// reloadDaemon serves a fixed report at the given status.
func reloadDaemon(report pkg.ReloadReport, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(report)
	}))
}

// reloadDaemonCapturing serves a fixed report and records the request body, so a
// test can assert the scoped sessions reach the daemon.
func reloadDaemonCapturing(report pkg.ReloadReport, captured *[]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	}))
}

// fleetSlashDaemon serves a fixed fleet-command report at the given status and
// records the request body, so a test can assert the command reaches the daemon.
func fleetSlashDaemon(
	report pkg.FleetCommandReport,
	status int,
	captured *[]byte,
) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			*captured, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(report)
	}))
}

var _ = ginkgo.Describe("fleetSlashCommandCLI", func() {
	ginkgo.It("prints JSON-lines to stdout, the summary to stderr, and exits 0 when all delivered", func() {
		srv := fleetSlashDaemon(pkg.FleetCommandReport{
			Command: "/mcp reconnect tts",
			Results: []pkg.FleetCommandResult{
				{SessionID: "s1", Outcome: pkg.FleetDelivered},
			},
		}, http.StatusOK, nil)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := fleetSlashCommandCLI(
			context.Background(), srv.URL, "/mcp reconnect tts", "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(0))
		gomega.Expect(stdout.String()).To(gomega.ContainSubstring(`"session_id":"s1"`))
		gomega.Expect(stdout.String()).To(gomega.ContainSubstring(`"outcome":"delivered"`))
		gomega.Expect(stderr.String()).To(gomega.ContainSubstring("delivered=1"))
	})

	ginkgo.It("exits 1 when a session was not delivered to", func() {
		srv := fleetSlashDaemon(pkg.FleetCommandReport{Results: []pkg.FleetCommandResult{
			{SessionID: "s1", Outcome: pkg.FleetBusy},
		}}, http.StatusOK, nil)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := fleetSlashCommandCLI(
			context.Background(), srv.URL, "/mcp reconnect tts", "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(1))
	})

	ginkgo.It("exits 1 for an empty population", func() {
		srv := fleetSlashDaemon(pkg.FleetCommandReport{}, http.StatusOK, nil)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := fleetSlashCommandCLI(
			context.Background(), srv.URL, "/mcp reconnect tts", "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(1))
	})

	ginkgo.It("exits 2 when the daemon returns non-200", func() {
		srv := fleetSlashDaemon(pkg.FleetCommandReport{}, http.StatusBadRequest, nil)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := fleetSlashCommandCLI(
			context.Background(), srv.URL, "/mcp reconnect tts", "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(2))
	})

	ginkgo.It("exits 2 when the daemon is unreachable", func() {
		var stdout, stderr bytes.Buffer
		code := fleetSlashCommandCLI(
			context.Background(), "http://127.0.0.1:1", "/mcp reconnect tts", "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(2))
	})

	ginkgo.It("sends the command and the scoped sessions to the daemon", func() {
		var captured []byte
		srv := fleetSlashDaemon(pkg.FleetCommandReport{}, http.StatusOK, &captured)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		_ = fleetSlashCommandCLI(
			context.Background(), srv.URL, "/mcp reconnect tts", "a,b", &stdout, &stderr)
		gomega.Expect(string(captured)).To(gomega.ContainSubstring(`"/mcp reconnect tts"`))
		gomega.Expect(string(captured)).To(gomega.ContainSubstring(`"a"`))
		gomega.Expect(string(captured)).To(gomega.ContainSubstring(`"b"`))
	})
})

var _ = ginkgo.Describe("reloadFleetCLI", func() {
	ginkgo.It("prints JSON-lines to stdout, the summary to stderr, and exits 0 when all reloaded", func() {
		srv := reloadDaemon(pkg.ReloadReport{Results: []pkg.ReloadResult{
			{SessionID: "s1", Outcome: pkg.OutcomeReloaded},
		}}, http.StatusOK)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := reloadFleetCLI(context.Background(), srv.URL, "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(0))
		gomega.Expect(stdout.String()).To(gomega.ContainSubstring(`"session_id":"s1"`))
		gomega.Expect(stdout.String()).To(gomega.ContainSubstring(`"outcome":"reloaded"`))
		gomega.Expect(stderr.String()).To(gomega.ContainSubstring("reloaded=1"))
	})

	ginkgo.It("exits 1 when a session is not reloaded", func() {
		srv := reloadDaemon(pkg.ReloadReport{Results: []pkg.ReloadResult{
			{SessionID: "s1", Outcome: pkg.OutcomeBusy},
		}}, http.StatusOK)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := reloadFleetCLI(context.Background(), srv.URL, "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(1))
	})

	ginkgo.It("exits 1 for an empty population", func() {
		srv := reloadDaemon(pkg.ReloadReport{}, http.StatusOK)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := reloadFleetCLI(context.Background(), srv.URL, "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(1))
	})

	ginkgo.It("exits 2 when the daemon returns non-200", func() {
		srv := reloadDaemon(pkg.ReloadReport{}, http.StatusInternalServerError)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		code := reloadFleetCLI(context.Background(), srv.URL, "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(2))
	})

	ginkgo.It("exits 2 when the daemon is unreachable", func() {
		var stdout, stderr bytes.Buffer
		code := reloadFleetCLI(context.Background(), "http://127.0.0.1:1", "", &stdout, &stderr)
		gomega.Expect(code).To(gomega.Equal(2))
	})

	ginkgo.It("sends the scoped sessions to the daemon", func() {
		var captured []byte
		srv := reloadDaemonCapturing(pkg.ReloadReport{}, &captured)
		defer srv.Close()
		var stdout, stderr bytes.Buffer
		_ = reloadFleetCLI(context.Background(), srv.URL, "a,b", &stdout, &stderr)
		gomega.Expect(string(captured)).To(gomega.ContainSubstring(`"a"`))
		gomega.Expect(string(captured)).To(gomega.ContainSubstring(`"b"`))
	})
})
