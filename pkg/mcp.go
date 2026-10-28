// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// PermissionToolName is the MCP tool a child is told to call for a permission
// decision. The name is namespaced by the server name in the generated config.
const PermissionToolName = "permission_prompt"

// ServeMCP runs a minimal MCP server on stdio.
//
// It exists because `--permission-prompts host` never surfaces a request:
// measured 2026-10-28 against claude 2.1.311, a forced `Write` to /etc produced
// no control_request on stdout or stderr and the child blocked until killed.
// A permission-prompt TOOL is the only channel that tells the wrapper a
// question exists.
//
// ⚠️ The tool's payload and return schema are not publicly documented. This
// server is deliberately log-first: it records every request verbatim to
// LogPath and answers allow, so the real shape can be read off disk before any
// parsing logic is written on top of it.
type MCPServer struct {
	In         io.Reader
	Out        io.Writer
	LogPath    string
	Now        libtime.CurrentDateTimeGetter
	WrapperURL string
	SessionID  string
	// Allow is the verdict returned when nobody answers. It is allow on
	// purpose: a child that stalls forever on an unanswered prompt is the
	// failure this whole channel exists to avoid.
	Allow bool
	// Hold is how long a request waits for a REST answer before the default
	// verdict is returned.
	Hold time.Duration
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

// Run serves until stdin closes.
func (s *MCPServer) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	encoder := json.NewEncoder(s.Out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal(line, &request); err != nil {
			continue
		}
		result, respond := s.handle(ctx, &request)
		if !respond {
			continue
		}
		if err := encoder.Encode(rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result:  result,
		}); err != nil {
			return errors.Wrapf(ctx, err, "encode response failed")
		}
	}
	return nil
}

// handle returns the result and whether a response is owed. Notifications
// (no id) are never answered.
func (s *MCPServer) handle(ctx context.Context, request *rpcRequest) (any, bool) {
	switch request.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "claude-wrapper", "version": "0.1.0"},
		}, len(request.ID) > 0
	case "tools/list":
		return map[string]any{"tools": []any{
			map[string]any{
				"name":        PermissionToolName,
				"description": "Decide whether the pending tool call may proceed.",
				"inputSchema": map[string]any{"type": "object"},
			},
		}}, len(request.ID) > 0
	case "tools/call":
		s.record(request.Params)
		return map[string]any{"content": []any{
			map[string]any{"type": "text", "text": s.decide(ctx, request.Params)},
		}}, len(request.ID) > 0
	default:
		return nil, false
	}
}

// record appends the raw request to the log. This is the whole discovery
// mechanism: the payload shape is read from here, not assumed.
func (s *MCPServer) record(params json.RawMessage) {
	if s.LogPath == "" {
		return
	}
	file, err := os.OpenFile(s.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("mcp: open log failed: %v", err)
		return
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(params, '\n')); err != nil {
		log.Printf("mcp: write log failed: %v", err)
	}
}

// decide forwards the request to the wrapper and waits for a REST answer.
//
// The tool is a child of claude, so it cannot touch the wrapper's memory: it
// registers the request over HTTP and polls until answered or the hold expires.
// On expiry it returns the default verdict rather than blocking the child
// forever — an unanswered prompt must never wedge a session.
func (s *MCPServer) decide(ctx context.Context, params json.RawMessage) string {
	if s.WrapperURL == "" || s.SessionID == "" {
		return s.verdictFor(s.defaultBehavior())
	}
	requestID := strconv.FormatInt(s.Now.Now().Time().UnixNano(), 36)
	if err := s.register(ctx, requestID, params); err != nil {
		log.Printf("mcp: register question failed: %v", err)
		return s.verdictFor(s.defaultBehavior())
	}
	deadline := s.Now.Now().Time().Add(s.Hold)
	for s.Now.Now().Time().Before(deadline) {
		// A cancelled host must not keep polling: the caller that owns this
		// context has gone away and the verdict can no longer reach it.
		select {
		case <-ctx.Done():
			return s.verdictFor(s.defaultBehavior())
		default:
		}
		behavior, answered, err := s.poll(ctx, requestID)
		if err == nil && answered {
			return s.verdictFor(behavior)
		}
		time.Sleep(250 * time.Millisecond)
	}
	log.Printf("mcp: request %s unanswered after %s, defaulting", requestID, s.Hold)
	return s.verdictFor(s.defaultBehavior())
}

func (s *MCPServer) register(ctx context.Context, requestID string, params json.RawMessage) error {
	body := map[string]any{
		"request_id": requestID,
		"tool_name":  "permission",
		"input":      json.RawMessage(params),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal question failed")
	}
	url := s.WrapperURL + "/api/1.0/sessions/" + s.SessionID + "/question"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return errors.Wrapf(ctx, err, "build question request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.Wrapf(ctx, err, "post question failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return errors.Errorf(ctx, "question rejected with %d", resp.StatusCode)
	}
	return nil
}

func (s *MCPServer) poll(ctx context.Context, requestID string) (string, bool, error) {
	url := s.WrapperURL + "/api/1.0/sessions/" + s.SessionID + "/question/" + requestID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, errors.Wrapf(ctx, err, "build poll request failed")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, errors.Wrapf(ctx, err, "poll question failed")
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Answered bool   `json:"answered"`
		Behavior string `json:"behavior"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, errors.Wrapf(ctx, err, "decode poll failed")
	}
	return body.Behavior, body.Answered, nil
}

func (s *MCPServer) defaultBehavior() string {
	if s.Allow {
		return "allow"
	}
	return "deny"
}

func (s *MCPServer) verdictFor(behavior string) string {
	if behavior == "deny" {
		return `{"behavior":"deny","message":"denied by claude-wrapper"}`
	}
	return `{"behavior":"allow"}`
}
