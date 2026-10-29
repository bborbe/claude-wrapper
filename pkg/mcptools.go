// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/bborbe/errors"
)

// MCPToolServer exposes the Supervisor as MCP tools over streamable HTTP.
//
// It is the client-facing twin of MCPServer, and the two point in opposite
// directions: MCPServer is stdio and child-facing (a child asking the wrapper
// for a permission verdict), this one is HTTP and client-facing (a Codex
// conversation driving a child). Different direction, different process
// boundary, same repo — which is why they live in separate files.
type MCPToolServer struct {
	Supervisor Supervisor
	// Locks is the same lock set the REST message route uses. A turn written
	// from MCP must not interleave with a sequence holding the child.
	Locks *SessionLocks
	// TurnTimeout bounds one send_message turn. A child that never closes its
	// turn must surface as a distinguishable error, never as a silent success.
	TurnTimeout time.Duration
}

const (
	// mcpProtocolVersion is what this server advertises when a client names no
	// preference. A client that names one is echoed, because this server
	// implements the common subset every published revision shares.
	mcpProtocolVersion = "2025-03-26"
	// maxMCPBody bounds one JSON-RPC message.
	maxMCPBody = 1 << 20
	// defaultPermissionMode is the phase-1 crutch.
	//
	// A real Claude session hits a permission gate on its first file write.
	// Phase 1 exists to prove plain turns, so it spawns permissively and the
	// gates never fire. This is a deliberate, temporary choice rather than a
	// safe default: it is removed in phase 2, when Codex learns to answer the
	// gate instead of skipping it.
	defaultPermissionMode = "bypassPermissions"
)

// Tool names as they appear to an MCP client.
const (
	ToolSpawnSession = "spawn_session"
	ToolSendMessage  = "send_message"
	ToolListSessions = "list_sessions"
	ToolGetSession   = "get_session"
	ToolKillSession  = "kill_session"
)

// rpcError is a JSON-RPC protocol-level error.
//
// A tool that fails while running is NOT reported this way: MCP carries that as
// a result with isError set, so the client reads the message as content rather
// than as a transport fault. Only a malformed request or an unknown method
// reaches here.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP serves one streamable-HTTP MCP endpoint.
func (s *MCPToolServer) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodPost:
		s.handlePost(resp, req)
	case http.MethodDelete:
		resp.WriteHeader(http.StatusOK)
	default:
		resp.Header().Set("Allow", "POST, DELETE")
		resp.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *MCPToolServer) handlePost(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	body, err := io.ReadAll(io.LimitReader(req.Body, maxMCPBody))
	if err != nil {
		writeRPC(resp, nil, nil, &rpcError{Code: -32700, Message: "read body failed"})
		return
	}
	var request rpcRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeRPC(resp, nil, nil, &rpcError{Code: -32700, Message: "parse error"})
		return
	}
	result, rpcErr := s.dispatch(ctx, &request)
	// A notification carries no id and is never answered; 202 is the whole
	// reply. Answering one is a protocol violation the client will reject.
	if len(request.ID) == 0 {
		resp.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(resp, request.ID, result, rpcErr)
}

func (s *MCPToolServer) dispatch(ctx context.Context, request *rpcRequest) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		return initializeResult(request.Params), nil
	case "notifications/initialized":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil
	case "tools/call":
		return s.callTool(ctx, request.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + request.Method}
	}
}

// initializeResult negotiates the protocol version.
//
// The client's own version is echoed back rather than this server's constant:
// every published revision shares the subset implemented here, and a client
// handed a version it did not ask for may refuse the session outright.
func initializeResult(params json.RawMessage) map[string]any {
	version := mcpProtocolVersion
	var body struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &body); err == nil && body.ProtocolVersion != "" {
		version = body.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "claude-wrapper", "version": "0.3.0"},
	}
}

func (s *MCPToolServer) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var body struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	text, err := s.invoke(ctx, body.Name, body.Arguments)
	if err != nil {
		// A failed tool is a successful call carrying isError: the client must
		// be able to read WHY, which a transport error would discard.
		return toolResult(err.Error(), true), nil
	}
	return toolResult(text, false), nil
}

func (s *MCPToolServer) invoke(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case ToolSpawnSession:
		return s.spawn(ctx, args)
	case ToolSendMessage:
		return s.send(ctx, args)
	case ToolListSessions:
		return s.list(ctx)
	case ToolGetSession:
		return s.get(ctx, args)
	case ToolKillSession:
		return s.kill(ctx, args)
	default:
		return "", errors.Errorf(ctx, "unknown tool %q", name)
	}
}

type spawnArgs struct {
	Cwd            string `json:"cwd"`
	Model          string `json:"model"`
	PermissionMode string `json:"permission_mode"`
}

func (s *MCPToolServer) spawn(ctx context.Context, args json.RawMessage) (string, error) {
	var body spawnArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	mode := body.PermissionMode
	if mode == "" {
		mode = defaultPermissionMode
	}
	session, err := s.Supervisor.Spawn(ctx, SpawnOptions{
		Cwd:            body.Cwd,
		Model:          body.Model,
		PermissionMode: mode,
	})
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, session)
}

type sendArgs struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// send writes one turn and returns the assistant text that closed it.
//
// It cannot delegate to the REST message route: POST …/message answers 202
// {"status":"sent"} and carries no answer, which is precisely the gap this tool
// exists to fill. So it writes through the same Supervisor.Send path and then
// waits for the child's own `result` event, returning that event's `result`
// field — measured 2026-10-29 against the installed CLI.
func (s *MCPToolServer) send(ctx context.Context, args json.RawMessage) (string, error) {
	var body sendArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.SessionID == "" || body.Text == "" {
		return "", errors.Errorf(ctx, "session_id and text are required")
	}
	// The same lock a sequence holds: a turn written mid-run would have its
	// result read as the current step's close, firing the next step early.
	release, ok := s.Locks.TryAcquire(body.SessionID)
	if !ok {
		return "", errors.Errorf(ctx, "session %q is running a sequence", body.SessionID)
	}
	defer release()

	replay, live, cancel, err := s.Supervisor.Subscribe(ctx, body.SessionID)
	if err != nil {
		return "", err
	}
	defer cancel()

	// Subscribed BEFORE the write, and the expected index taken from the
	// replay: a result arriving between the write and the subscribe would
	// otherwise be missed and the turn would wait out its whole timeout.
	expected := expectedTurn(replay)
	if err := s.Supervisor.Send(ctx, body.SessionID, body.Text); err != nil {
		return "", err
	}
	event, err := s.awaitTurn(ctx, live, expected)
	if err != nil {
		return "", err
	}
	text, _ := event["result"].(string)
	if failed, _ := event["is_error"].(bool); failed {
		return "", errors.Errorf(ctx, "turn %d failed: %s", expected, text)
	}
	if text == "" {
		return "", errors.Errorf(ctx, "turn %d closed with no result text", expected)
	}
	return text, nil
}

// awaitTurn blocks until the child reports turn `expected` closed.
func (s *MCPToolServer) awaitTurn(ctx context.Context, live <-chan Event, expected int) (Event, error) {
	timeout := s.TurnTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		select {
		case <-waitCtx.Done():
			return nil, errors.Wrapf(ctx, waitCtx.Err(),
				"turn %d did not close within %s", expected, timeout)
		case event, open := <-live:
			if !open {
				return nil, errors.Wrapf(ctx, ErrSessionExited,
					"stream closed before turn %d closed", expected)
			}
			if isTurnClosed(event, expected) {
				return event, nil
			}
		}
	}
}

func (s *MCPToolServer) list(ctx context.Context) (string, error) {
	sessions, err := s.Supervisor.List(ctx)
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, sessions)
}

type sessionArgs struct {
	SessionID string `json:"session_id"`
}

func (s *MCPToolServer) get(ctx context.Context, args json.RawMessage) (string, error) {
	var body sessionArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.SessionID == "" {
		return "", errors.Errorf(ctx, "session_id is required")
	}
	session, err := s.Supervisor.Get(ctx, body.SessionID)
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, session)
}

func (s *MCPToolServer) kill(ctx context.Context, args json.RawMessage) (string, error) {
	var body sessionArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.SessionID == "" {
		return "", errors.Errorf(ctx, "session_id is required")
	}
	if err := s.Supervisor.Stop(ctx, body.SessionID); err != nil {
		return "", err
	}
	return marshalJSON(ctx, map[string]string{"status": "killed", "id": body.SessionID})
}

func marshalJSON(ctx context.Context, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "marshal result failed")
	}
	return string(encoded), nil
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": isError,
	}
}

func writeRPC(resp http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	resp.Header().Set("Content-Type", "application/json")
	payload := rpcResponse{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		payload.Error = rpcErr
	} else {
		payload.Result = result
	}
	resp.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(resp).Encode(payload)
}

// toolDefinitions describes the five tools to an MCP client.
func toolDefinitions() []any {
	return []any{
		toolDef(ToolSpawnSession,
			"Spawn one Claude session and return it, with its id.",
			map[string]any{
				"cwd":             stringProp("working directory for the child"),
				"model":           stringProp("model to launch the child with"),
				"permission_mode": stringProp("permission mode; defaults to a permissive one"),
			}),
		toolDef(ToolSendMessage,
			"Send one user turn and return the assistant text that closes it.",
			map[string]any{
				"session_id": stringProp("id of the session to write to"),
				"text":       stringProp("the user turn to send"),
			},
			"session_id", "text"),
		toolDef(ToolListSessions, "List every session this wrapper owns.", map[string]any{}),
		toolDef(ToolGetSession,
			"Read one session's state, model and pending question.",
			map[string]any{"session_id": stringProp("id of the session to read")},
			"session_id"),
		toolDef(ToolKillSession,
			"Kill one session's child process.",
			map[string]any{"session_id": stringProp("id of the session to kill")},
			"session_id"),
	}
}

func toolDef(name string, description string, props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": schema,
	}
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
