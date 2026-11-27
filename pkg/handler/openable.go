// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// OpenableSource answers the flagged tick's per-row open decision.
type OpenableSource interface {
	Openable(ctx context.Context) ([]pkg.OpenableEntry, error)
}

// Openable serves GET /api/1.0/openable, the read half of the open decision.
//
// It reports, for every candidate row, whether the tick would open it this cycle
// and — when held — the tick's own reason token, so a manager reads one
// deterministic verdict instead of hunting the vault and the wrapper log. The
// tick and this handler share one decision function (FlaggedTick.Openable →
// classify), so they cannot route the same row two ways.
func Openable(source OpenableSource) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		// Checked FIRST, matching Open: a non-loopback caller learns nothing.
		if !pkg.LoopbackRequest(req) {
			resp.WriteHeader(http.StatusForbidden)
			return
		}
		if req.Method != http.MethodGet {
			resp.Header().Set("Allow", "GET")
			writeError(resp, http.StatusMethodNotAllowed,
				errors.Errorf(ctx, "method %q not allowed", req.Method))
			return
		}
		entries, err := source.Openable(ctx)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		writeJSON(resp, http.StatusOK, map[string]any{"rows": entries})
	})
}
