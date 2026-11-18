// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"log"
	"net/http"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// Liveness serves the authoritative per-session liveness verdict.
//
// GET only. It reads the child table and the heartbeat store, then hands both
// to pkg.LivenessVerdicts together with the registry, so this process — not a
// Python script and not the LLM fleet round — is the single authority on
// liveness.
//
// A failure of a PRIMARY source is a 500, never a verdict: an empty set would
// read as "nothing is live". The heartbeat store is different — it is
// corroboration only, so its failure is logged and the answer is served from the
// primary sources alone. Returning 500 there would hide every live worker on a
// store outage, which is the exact failure this endpoint exists to remove; the
// same degrade is the established pattern at pkg/roster.go:157-161.
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
		if err != nil || rows == nil {
			// Corroboration absent, not fatal: the registry and the child table
			// already answered, and a store outage must never hide a live worker.
			log.Printf("WARNING: liveness — the heartbeat store could not be read, "+
				"answering from the primary sources alone: %v", err)
			rows = nil
		}
		verdicts, err := pkg.LivenessVerdicts(ctx, registry, children, rows)
		if err != nil {
			writeError(resp, http.StatusInternalServerError, err)
			return
		}
		writeJSON(resp, http.StatusOK, verdicts)
	})
}
