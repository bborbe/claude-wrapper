// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// ReloadFleetSurface is the fleet-reload operation both the REST route and the
// reload_fleet MCP tool serve, so the two surfaces cannot drift.
type ReloadFleetSurface interface {
	ReloadFleetReport(ctx context.Context, only []string) (pkg.ReloadReport, error)
}

// ReloadFleet serves POST /api/1.0/reload-fleet: the REST half of the
// reload_fleet MCP tool. It is loopback-gated and input-validated like
// handler.Open, and it clears the server write deadline because the serial fleet
// reload can outlast the 60s WriteTimeout.
func ReloadFleet(surface ReloadFleetSurface) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
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
		// The serial fleet reload can outlast the server's 60s WriteTimeout: a
		// 10s settle per unverified session plus ~8-16s SendWhenReady per busy or
		// refusing session. Without clearing the deadline the report is written
		// after it and the CLI reads a broken connection despite the reload having
		// run. Same call as handler.Open and clearRosterWriteDeadline.
		if err := http.NewResponseController(resp).SetWriteDeadline(time.Time{}); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			writeError(resp, http.StatusInternalServerError,
				errors.Wrapf(ctx, err, "clear write deadline failed"))
			return
		}
		var body struct {
			Sessions []string `json:"sessions"`
		}
		raw, err := io.ReadAll(io.LimitReader(req.Body, pkg.MaxOpenBody+1))
		if err != nil {
			writeError(resp, http.StatusBadRequest,
				errors.Wrapf(ctx, err, "read body failed"))
			return
		}
		if len(raw) > pkg.MaxOpenBody {
			writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "body exceeds 1 MiB"))
			return
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeError(resp, http.StatusBadRequest,
					errors.Wrapf(ctx, err, "decode body failed"))
				return
			}
		}
		report, err := surface.ReloadFleetReport(ctx, body.Sessions)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		resp.Header().Set("Content-Type", "application/json")
		resp.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(resp).Encode(report)
	})
}
