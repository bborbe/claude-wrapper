// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"io"
	"net/http"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// Open serves POST /api/1.0/open, the REST half of the one open operation.
//
// The contract is docs/open-endpoint.md. It delegates decode, validation,
// execution and marshalling to pkg.OpenSurface, which the open_rows MCP tool
// calls too, so the two surfaces cannot drift (AC4).
func Open(surface *pkg.OpenSurface) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		// Checked FIRST, before the method, matching RosterToolServer.ServeHTTP:
		// a non-loopback caller learns nothing about the route.
		if !pkg.LoopbackRequest(req) {
			resp.WriteHeader(http.StatusForbidden)
			return
		}
		if req.Method != http.MethodPost {
			resp.Header().Set("Allow", "POST")
			writeError(resp, http.StatusMethodNotAllowed,
				errors.Errorf(ctx, "method %q not allowed", req.Method))
			return
		}
		if !pkg.IsJSONRequest(req) {
			resp.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		// A goal manager's start commands may wait up to six minutes, past the
		// server's 60 s WriteTimeout. Same idiom, same reason, as
		// clearRosterWriteDeadline.
		if err := http.NewResponseController(resp).SetWriteDeadline(time.Time{}); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			writeError(resp, http.StatusInternalServerError,
				errors.Wrapf(ctx, err, "clear write deadline failed"))
			return
		}
		// One byte past the cap, so an oversized body is diagnosed rather than
		// silently truncated into a parse error.
		body, err := io.ReadAll(io.LimitReader(req.Body, pkg.MaxOpenBody+1))
		if err != nil {
			writeError(resp, http.StatusBadRequest,
				errors.Wrapf(ctx, err, "read body failed"))
			return
		}
		if len(body) > pkg.MaxOpenBody {
			writeError(resp, http.StatusBadRequest,
				errors.Errorf(ctx, "body exceeds 1 MiB"))
			return
		}
		out, err := surface.Serve(ctx, body, req.Header.Get(pkg.SessionHeader))
		if err != nil {
			if errors.Is(err, pkg.ErrInvalidOpenRequest) {
				writeError(resp, http.StatusBadRequest, err)
				return
			}
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		// Written verbatim, not through writeJSON (which appends a newline): the
		// bytes must equal the open_rows tool's text byte for byte (AC4).
		resp.Header().Set("Content-Type", "application/json")
		resp.WriteHeader(http.StatusOK)
		//nolint:gosec // G705: out is JSON from OpenSurface and this route declares application/json, never markup
		_, _ = resp.Write(out)
	})
}
