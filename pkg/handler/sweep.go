// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg/sweepgate"
)

// Sweep serves the sweep gate's classified rows for one subject.
//
// GET only, with the subject as `?subject=<name>`. It answers DATA, not the
// rendered frame: before this the only way to read a bucket out of the gate was
// to render the table and parse it, so no consumer did — and the fleet round kept
// re-deriving the classification from the same inputs, which is the defect this
// route removes.
//
// The rows come from `sweepgate.ClassifiedRows`, which classifies through the
// same `Bucket` function the frame uses, so a consumer here and an operator
// reading the rendered table cannot disagree about a row.
//
// A missing subject is a 400 rather than a default: the gate is configured per
// subject, and guessing one would silently answer about the wrong tracked set.
func Sweep(base sweepgate.Config) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		if req.Method != http.MethodGet {
			writeError(resp, http.StatusMethodNotAllowed,
				errors.Errorf(ctx, "method %q not allowed", req.Method))
			return
		}
		subject := req.URL.Query().Get("subject")
		if subject == "" {
			writeError(resp, http.StatusBadRequest,
				errors.Errorf(ctx, "query parameter %q is required", "subject"))
			return
		}
		// Refuse a traversal at the BOUNDARY, before it reaches a path join. The
		// gate also refuses it internally, but a client sending `../../x` is
		// making a bad request, not triggering a server fault — and answering 400
		// here keeps the reason legible instead of surfacing as a 500.
		if err := sweepgate.RefuseUnsafeSubject(ctx, subject); err != nil {
			writeError(resp, http.StatusBadRequest, err)
			return
		}
		// Resolve defaults HERE rather than trusting the caller's config: the
		// clock is one of them, and Prepare dereferences it. A config that skipped
		// WithDefaults (the flag builder returns its unresolved value when that
		// step fails) would otherwise panic this handler instead of answering.
		cfg, err := base.WithDefaults(ctx)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		cfg.Subject = subject
		rows, err := sweepgate.ClassifiedRows(ctx, cfg)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		writeJSON(resp, http.StatusOK, rows)
	})
}
