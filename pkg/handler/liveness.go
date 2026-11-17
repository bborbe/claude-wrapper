// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// Liveness serves the authoritative per-session liveness verdict.
//
// GET only. It reads the child table and the heartbeat store, then hands both
// to pkg.LivenessVerdicts together with the registry, so this process — not a
// Python script and not the LLM fleet round — is the single authority on
// liveness. A read failure is a 500, never a verdict: an empty set would read
// as "nothing is live".
func Liveness(
	registry pkg.SessionRegistry,
	supervisor pkg.Supervisor,
	heartbeats pkg.HeartbeatReader,
) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		if req.Method != http.MethodGet {
			writeError(resp, http.StatusMethodNotAllowed,
				errors.Errorf(ctx, "method %q not allowed", req.Method))
			return
		}
		children, err := supervisor.List(ctx)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		rows, err := heartbeats.Rows(ctx)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		verdicts, err := pkg.LivenessVerdicts(ctx, registry, children, rows)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		writeJSON(resp, http.StatusOK, verdicts)
	})
}
