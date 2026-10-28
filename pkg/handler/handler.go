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

// Session serves one child: GET, DELETE, and the POST …/answer, …/message and
// …/question sub-resources.
func Session(supervisor pkg.Supervisor) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		rest := strings.TrimPrefix(req.URL.Path, "/api/1.0/sessions/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		id := parts[0]
		if id == "" {
			writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "session id missing"))
			return
		}
		action := ""
		requestID := ""
		if len(parts) > 1 {
			action = parts[1]
		}
		if len(parts) > 2 {
			requestID = parts[2]
		}
		switch action {
		case "answer":
			handleAnswer(ctx, resp, req, supervisor, id)
		case "message":
			handleMessage(ctx, resp, req, supervisor, id)
		case "question":
			handleQuestion(ctx, resp, req, supervisor, id, requestID)
		default:
			handleSession(ctx, resp, req, supervisor, id)
		}
	})
}

type messageRequest struct {
	Text string `json:"text"`
}

func handleMessage(
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
	var body messageRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
		return
	}
	if body.Text == "" {
		writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "text is required"))
		return
	}
	if err := supervisor.Send(ctx, id, body.Text); err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	writeJSON(resp, http.StatusAccepted, map[string]string{"status": "sent"})
}

type questionRequest struct {
	RequestID string         `json:"request_id"`
	ToolName  string         `json:"tool_name"`
	Input     map[string]any `json:"input"`
}

// handleQuestion is the two halves the MCP tool needs: POST registers a
// permission request, GET polls for its answer. Polling rather than long-poll
// keeps one pending prompt from holding a server goroutine.
func handleQuestion(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	id string,
	requestID string,
) {
	switch req.Method {
	case http.MethodPost:
		var body questionRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
			return
		}
		question := pkg.Question{
			RequestID: body.RequestID,
			ToolName:  body.ToolName,
			Input:     body.Input,
			Options:   []string{"allow", "deny"},
		}
		if err := supervisor.RegisterQuestion(ctx, id, question); err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		writeJSON(resp, http.StatusAccepted, question)
	case http.MethodGet:
		behavior, answered, err := supervisor.PollAnswer(ctx, requestID)
		if err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		writeJSON(resp, http.StatusOK, map[string]any{
			"answered": answered,
			"behavior": behavior,
		})
	default:
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
	}
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
