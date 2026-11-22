// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
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
// reload_fleet MCP tool. It is loopback-gated and input-validated like
// handler.Open, and it clears the server write deadline because the serial fleet
// reload can outlast the 60s WriteTimeout.
func ReloadFleet(surface ReloadFleetSurface) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		raw, ok := prepareFleetRequest(ctx, resp, req)
		if !ok {
			return
		}
		var body struct {
			Sessions []string `json:"sessions"`
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
