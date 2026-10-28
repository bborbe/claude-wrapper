// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"

	"github.com/bborbe/errors"
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
	In      io.Reader
	Out     io.Writer
	LogPath string
	// Allow is the default verdict, and the only one this first cut returns.
	Allow bool
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
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
			map[string]any{"type": "text", "text": s.verdict()},
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

func (s *MCPServer) verdict() string {
	if s.Allow {
		return `{"behavior":"allow"}`
	}
	return `{"behavior":"deny","message":"denied by claude-wrapper"}`
}
