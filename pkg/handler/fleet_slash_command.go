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

// FleetSlashCommandSurface is the fleet-wide slash delivery both the REST route
// and the fleet_slash_command MCP tool serve, so the two surfaces cannot drift.
type FleetSlashCommandSurface interface {
	FleetSlashCommandReport(
		ctx context.Context,
		command string,
		only []string,
	) (pkg.FleetCommandReport, error)
}

// FleetSlashCommand serves POST /api/1.0/fleet-slash-command: the REST half of
// the fleet_slash_command MCP tool. Same gate and same write-deadline handling as
// the reload route, because a serial fleet run has the same shape.
//
// A command outside the allowlist is refused here with 400 rather than reported
// once per session: it can never be delivered, so the run would be pure noise.
func FleetSlashCommand(surface FleetSlashCommandSurface) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		raw, ok := prepareFleetRequest(ctx, resp, req)
		if !ok {
			return
		}
		var body struct {
			Command  string   `json:"command"`
			Sessions []string `json:"sessions"`
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeError(resp, http.StatusBadRequest,
					errors.Wrapf(ctx, err, "decode body failed"))
				return
			}
		}
		if body.Command == "" {
			writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "command is required"))
			return
		}
		report, err := surface.FleetSlashCommandReport(ctx, body.Command, body.Sessions)
		if err != nil {
			if errors.Is(err, pkg.ErrSlashCommandNotAllowed) {
				writeError(resp, http.StatusBadRequest, err)
				return
			}
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		resp.Header().Set("Content-Type", "application/json")
		resp.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(resp).Encode(report)
	})
}
