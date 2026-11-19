// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/handler"
	"github.com/bborbe/claude-wrapper/pkg/sweepgate"
)

// serveSweep runs one request through the handler and returns the recorder.
//
// The 200 path is not asserted here: it needs a real vault, and the rows it
// serves are covered directly by pkg/sweepgate's ClassifiedRows specs. What is
// asserted below is this handler's own logic — method, the required subject, and
// the error path.
func serveSweep(method, target string, cfg sweepgate.Config) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	handler.Sweep(cfg).ServeHTTP(rec, req)
	return rec
}

var _ = Describe("Sweep", func() {
	It("answers 405 for a method other than GET", func() {
		rec := serveSweep(http.MethodPost, "/api/1.0/sweep?subject=S", sweepgate.Config{})

		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
	})

	// A missing subject is refused rather than defaulted: the gate is configured
	// per subject, and guessing one would silently answer about the wrong set.
	It("answers 400 when the subject is missing", func() {
		rec := serveSweep(http.MethodGet, "/api/1.0/sweep", sweepgate.Config{})

		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	// A traversal attempt is a bad request, refused before it reaches a path
	// join. Regression guard for the defect this route shipped with: the subject
	// went straight into filepath.Join(vault, "24 Goals", subject+".md"), which
	// reads files outside the vault.
	DescribeTable("answers 400 for a subject that would traverse a path",
		func(subject string) {
			rec := serveSweep(http.MethodGet, "/api/1.0/sweep?subject="+subject, sweepgate.Config{})

			Expect(rec.Code).To(Equal(http.StatusBadRequest))
		},
		Entry("parent reference", "../../../etc/passwd"),
		Entry("embedded parent reference", "foo/../../bar"),
		Entry("forward slash", "a/b"),
		Entry("backslash", `a\b`),
	)

	// The handler no longer runs Prepare — it serves the tick's cached rows. No
	// cache means the tick has not run for this subject yet, which is a
	// service-not-ready answer. NOT an empty 200: an empty list reads as
	// "nothing is tracked".
	It("answers 503 when the tick has not written the rows yet", func() {
		rec := serveSweep(http.MethodGet, "/api/1.0/sweep?subject=S", sweepgate.Config{
			StateDir: GinkgoT().TempDir(),
			Interval: time.Minute,
		})

		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
	})

	// A stale verdict presented as fresh is the mislabelling this task exists to
	// remove, so past the bound the answer carries the age instead of the rows.
	It("answers 503 with the age when the cached rows are past the bound", func() {
		dir := GinkgoT().TempDir()
		cfg := sweepgate.Config{StateDir: dir, Interval: time.Minute}
		cfg.Subject = "S"
		rowsPath, err := sweepgate.RowsPath(context.Background(), cfg, "S")
		Expect(err).To(BeNil())
		Expect(os.WriteFile(rowsPath, []byte("[]"), 0o600)).To(Succeed())
		// Age the cache well past 3 x 1m.
		old := time.Now().Add(-10 * time.Minute)
		Expect(os.Chtimes(rowsPath, old, old)).To(Succeed())

		rec := serveSweep(http.MethodGet, "/api/1.0/sweep?subject=S", cfg)

		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(rec.Body.String()).To(ContainSubstring("past the"))
	})

	It("serves the cached rows verbatim when they are fresh", func() {
		dir := GinkgoT().TempDir()
		cfg := sweepgate.Config{StateDir: dir, Interval: time.Minute}
		cfg.Subject = "S"
		rowsPath, err := sweepgate.RowsPath(context.Background(), cfg, "S")
		Expect(err).To(BeNil())
		const body = `[{"name":"A","bucket":"done"}]`
		Expect(os.WriteFile(rowsPath, []byte(body), 0o600)).To(Succeed())

		rec := serveSweep(http.MethodGet, "/api/1.0/sweep?subject=S", cfg)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal(body))
	})
})
