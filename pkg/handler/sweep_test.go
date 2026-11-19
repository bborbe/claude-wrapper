// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

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

	It("answers 500 when the rows cannot be prepared", func() {
		// An empty vault cannot resolve a tracked set, so Prepare fails.
		rec := serveSweep(http.MethodGet, "/api/1.0/sweep?subject=S", sweepgate.Config{})

		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})
})
