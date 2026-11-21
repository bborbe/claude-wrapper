// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// reloadDaemon serves a fixed report and records the request body.
func reloadDaemon(report pkg.ReloadReport, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(report)
	}))
}

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
})
