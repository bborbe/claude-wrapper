// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// ReloadFleetSurface is the fleet-reload operation both the REST route and the
// reload_fleet MCP tool serve, so the two surfaces cannot drift.
type ReloadFleetSurface interface {
	ReloadFleetReport(ctx context.Context, only []string) (pkg.ReloadReport, error)
}

// ReloadFleet serves POST /api/1.0/reload-fleet: the REST half of the
// reload_fleet MCP tool. Loopback-gated like the other pane-touching routes.
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
		var body struct {
			Sessions []string `json:"sessions"`
		}
		if req.Body != nil {
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
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
