// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// prepareFleetRequest applies the gate every fleet route shares: loopback-only,
// POST, JSON, bounded body. It also clears the server write deadline, because a
// serial fleet run can outlast the server's 60s WriteTimeout — a 10s settle per
// unverified session plus ~8-16s SendWhenReady per busy or refusing session — and
// the report would then be written after it, leaving the CLI reading a broken
// connection despite the run having happened.
//
// ok is false when a response has already been written; the caller returns.
func prepareFleetRequest(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
) ([]byte, bool) {
	if !pkg.LoopbackRequest(req) {
		resp.WriteHeader(http.StatusForbidden)
		return nil, false
	}
	if req.Method != http.MethodPost {
		resp.Header().Set("Allow", "POST")
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return nil, false
	}
	if !pkg.IsJSONRequest(req) {
		resp.WriteHeader(http.StatusUnsupportedMediaType)
		return nil, false
	}
	if err := http.NewResponseController(resp).SetWriteDeadline(time.Time{}); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		writeError(resp, http.StatusInternalServerError,
			errors.Wrapf(ctx, err, "clear write deadline failed"))
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, pkg.MaxOpenBody+1))
	if err != nil {
		writeError(resp, http.StatusBadRequest,
			errors.Wrapf(ctx, err, "read body failed"))
		return nil, false
	}
	if len(raw) > pkg.MaxOpenBody {
		writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "body exceeds 1 MiB"))
		return nil, false
	}
	return raw, true
}
