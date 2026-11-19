// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"os"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg/sweepgate"
)

// staleRowsFactor bounds how old the cached rows may be, as a multiple of the
// tick interval.
//
// Three intervals tolerates two missed ticks before the endpoint refuses: one
// missed tick is ordinary, two is a hiccup, three means the gate has stopped and
// the rows no longer describe the fleet. Past the bound the answer is 503 WITH
// THE AGE, never the stale rows — a stale verdict presented as fresh is the
// mislabelling this whole task exists to remove.
const staleRowsFactor = 3

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
		// Serve the TICK'S CACHED ROWS rather than re-running Prepare. That scan
		// is minutes (measured 3m12s across three roots), so a per-request scan
		// timed out before answering — and a round that times out falls back to
		// re-deriving the classification, the defect this endpoint exists to
		// remove. The tick writes these rows on every tick.
		rowsPath, err := sweepgate.RowsPath(ctx, cfg, subject)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		// gosec's taint analysis cannot see through RowsPath -> Slug, which
		// refuses `..` and separators before any join; the subject was also
		// checked at the boundary above. Two independent refusals, one path.
		info, err := os.Stat(rowsPath) //nolint:gosec // subject validated by RefuseUnsafeSubject + Slug
		if err != nil {
			// No cache yet: the tick has not run for this subject. That is a
			// service-not-ready answer, not a client error and not an empty 200 —
			// an empty list would read as "nothing is tracked".
			writeError(resp, http.StatusServiceUnavailable,
				errors.Errorf(ctx, "no cached rows for subject %q yet — the tick has not written them", subject))
			return
		}
		limit := time.Duration(staleRowsFactor) * cfg.Interval
		if age := cfg.Now.Now().Time().Sub(info.ModTime()); age > limit {
			// A stale verdict is the mislabelling this whole task removes, so it is
			// refused WITH its age rather than served as though it were fresh.
			writeError(resp, http.StatusServiceUnavailable,
				errors.Errorf(ctx, "cached rows for %q are %s old, past the %s bound",
					subject, age.Round(time.Second), limit))
			return
		}
		data, err := os.ReadFile(rowsPath) //nolint:gosec // path is derived from a validated subject
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		resp.Header().Set("Content-Type", "application/json")
		resp.WriteHeader(http.StatusOK)
		if _, err := resp.Write(data); err != nil { //nolint:gosec // served as application/json, not HTML
			_ = err // the client went away; nothing useful to do with it
		}
	})
}
