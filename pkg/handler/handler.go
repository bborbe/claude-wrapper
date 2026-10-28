// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// Healthz reports process liveness. It touches no child.
func Healthz() http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		writeJSON(resp, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// Sessions serves the collection: GET lists, POST spawns.
func Sessions(supervisor pkg.Supervisor) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		switch req.Method {
		case http.MethodGet:
			sessions, err := supervisor.List(ctx)
			if err != nil {
				writeError(resp, http.StatusInternalServerError, err)
				return
			}
			writeJSON(resp, http.StatusOK, sessions)
		case http.MethodPost:
			var options pkg.SpawnOptions
			if err := json.NewDecoder(req.Body).Decode(&options); err != nil {
				writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
				return
			}
			session, err := supervisor.Spawn(ctx, options)
			if err != nil {
				writeError(resp, http.StatusInternalServerError, err)
				return
			}
			writeJSON(resp, http.StatusCreated, session)
		default:
			writeError(resp, http.StatusMethodNotAllowed,
				errors.Errorf(ctx, "method %q not allowed", req.Method))
		}
	})
}

// Session serves one child: GET, DELETE, and POST …/answer.
func Session(supervisor pkg.Supervisor) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		rest := strings.TrimPrefix(req.URL.Path, "/api/1.0/sessions/")
		id, action := splitPath(rest)
		if id == "" {
			writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "session id missing"))
			return
		}
		if action == "answer" {
			handleAnswer(ctx, resp, req, supervisor, id)
			return
		}
		handleSession(ctx, resp, req, supervisor, id)
	})
}

func splitPath(rest string) (string, string) {
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func handleSession(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	id string,
) {
	switch req.Method {
	case http.MethodGet:
		session, err := supervisor.Get(ctx, id)
		if err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		writeJSON(resp, http.StatusOK, session)
	case http.MethodDelete:
		if err := supervisor.Stop(ctx, id); err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		resp.WriteHeader(http.StatusNoContent)
	default:
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
	}
}

type answerRequest struct {
	RequestID string `json:"request_id"`
	Behavior  string `json:"behavior"`
}

func handleAnswer(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	id string,
) {
	if req.Method != http.MethodPost {
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return
	}
	var body answerRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
		return
	}
	if body.Behavior == "" {
		body.Behavior = "allow"
	}
	if err := supervisor.Answer(ctx, id, body.RequestID, body.Behavior); err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	writeJSON(resp, http.StatusOK, map[string]string{"status": "answered"})
}

// statusFor maps the supervisor's sentinels onto the documented status contract.
func statusFor(err error) int {
	switch {
	case errors.Is(err, pkg.ErrSessionNotFound):
		return http.StatusNotFound
	case errors.Is(err, pkg.ErrSessionExited):
		return http.StatusGone
	case errors.Is(err, pkg.ErrAnswerUnsupported):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func writeError(resp http.ResponseWriter, status int, err error) {
	writeJSON(resp, status, map[string]any{
		"error": map[string]string{
			"code":    http.StatusText(status),
			"message": err.Error(),
		},
	})
}

func writeJSON(resp http.ResponseWriter, status int, payload any) {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(status)
	_ = json.NewEncoder(resp).Encode(payload)
}
