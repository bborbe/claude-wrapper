// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/bborbe/errors"
)

// SessionHeader is the header a calling session identifies itself with.
//
// It is supplied by a `headersHelper` on the client's `type: http` entry, which
// walks to the calling `claude` process and reads its session id from the
// registry. Without it a roster call cannot be attributed, and an unattributed
// call is refused rather than defaulted — a default would record the call
// against a session that does not exist.
const SessionHeader = "X-Claude-Code-Session-Id"

// Roster tool names, as they appear to an MCP client.
const (
	ToolListAgents  ToolName = "list_agents"
	ToolAgentStatus ToolName = "agent_status"
	ToolStopAgent   ToolName = "stop_agent"
)

// RosterToolNames is every tool the roster face exposes, in listing order.
//
// Three of the supervisor's nine. The other six are the spawn, message, park and
// A2A surfaces, which have nothing to act on until the spawn slices land.
func RosterToolNames() []ToolName {
	return []ToolName{ToolListAgents, ToolAgentStatus, ToolStopAgent}
}

// RosterToolServer serves the supervisor's roster tools over streamable HTTP.
//
// It is a THIRD MCP face, and the three point in different directions:
// MCPToolServer is HTTP and client-facing (Codex driving a child), MCPServer is
// stdio and child-facing (a child asking for a permission verdict), and this one
// is HTTP and fleet-facing (a Claude session reading and steering the fleet).
type RosterToolServer struct {
	Roster Roster
	// Supervisor is used by stop_agent, and only for a session this process
	// spawned: it holds the child's stdin, which is what makes a stop a stop
	// rather than a signal sent at a process this daemon does not own.
	Supervisor Supervisor
	// Ledger records the stop attribution. Nil is legal — the tools still serve,
	// and only the ledger write is skipped.
	Ledger LedgerReader
}

// ServeHTTP serves one roster MCP request.
func (s *RosterToolServer) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	if s.Roster == nil {
		resp.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// The same rebinding guard the Codex face uses: a localhost HTTP server is
	// reachable by any page in the operator's browser through DNS rebinding, and
	// this face reports the fleet.
	if !originAllowed(req) {
		resp.WriteHeader(http.StatusForbidden)
		return
	}
	sessionID := req.Header.Get(SessionHeader)
	if sessionID == "" {
		writeRosterError(
			resp,
			http.StatusBadRequest,
			"missing_session_header",
			ErrMissingSessionHeader.Error()+
				" — a roster call is attributed to the calling session, so it is refused rather than defaulted",
		)
		return
	}
	switch req.Method {
	case http.MethodPost:
		if !isJSONRequest(req) {
			resp.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		s.handlePost(resp, req, sessionID)
	case http.MethodDelete:
		resp.WriteHeader(http.StatusOK)
	default:
		resp.Header().Set("Allow", "POST, DELETE")
		resp.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handlePost reads one JSON-RPC message and answers it.
func (s *RosterToolServer) handlePost(
	resp http.ResponseWriter,
	req *http.Request,
	sessionID string,
) {
	ctx := req.Context()
	body, err := io.ReadAll(io.LimitReader(req.Body, maxMCPBody+1))
	if err != nil {
		writeRPC(resp, nil, nil, &rpcError{Code: -32700, Message: "read body failed"})
		return
	}
	if len(body) > maxMCPBody {
		resp.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	var request rpcRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeRPC(resp, nil, nil, &rpcError{Code: -32700, Message: "parse error"})
		return
	}
	result, rpcErr := s.dispatch(ctx, &request, sessionID)
	// A notification carries no id and is never answered.
	if len(request.ID) == 0 {
		resp.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(resp, request.ID, result, rpcErr)
}

func (s *RosterToolServer) dispatch(
	ctx context.Context,
	request *rpcRequest,
	sessionID string,
) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		return initializeResult(request.Params), nil
	case "notifications/initialized":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": rosterToolDefinitions()}, nil
	case "tools/call":
		return s.callTool(ctx, request.Params, sessionID)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + request.Method}
	}
}

func (s *RosterToolServer) callTool(
	ctx context.Context,
	params json.RawMessage,
	sessionID string,
) (any, *rpcError) {
	var body struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &body); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	text, err := s.invoke(ctx, body.Name, body.Arguments, sessionID)
	if err != nil {
		// A failed tool is a successful call carrying isError, so the client can
		// read WHY — a transport error would discard it.
		return toolResult(err.Error(), true), nil
	}
	return toolResult(text, false), nil
}

func (s *RosterToolServer) invoke(
	ctx context.Context,
	name string,
	args json.RawMessage,
	sessionID string,
) (string, error) {
	switch ToolName(name) {
	case ToolListAgents:
		return s.listAgents(ctx)
	case ToolAgentStatus:
		return s.agentStatus(ctx, args)
	case ToolStopAgent:
		return s.stopAgent(ctx, args, sessionID)
	default:
		return "", errors.Errorf(ctx, "unknown tool %q", name)
	}
}

func (s *RosterToolServer) listAgents(ctx context.Context) (string, error) {
	rows, err := s.Roster.List(ctx)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "list agents failed")
	}
	return marshalJSON(ctx, rows)
}

type agentArgs struct {
	AgentID string `json:"agent_id"`
}

func (s *RosterToolServer) agentStatus(ctx context.Context, args json.RawMessage) (string, error) {
	var body agentArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" {
		return "", errors.Errorf(ctx, "agent_id is required")
	}
	row, err := s.Roster.Get(ctx, body.AgentID)
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, row)
}

// stopAgent stops a session this wrapper spawned.
//
// The ownership boundary is the point rather than a limitation: the wrapper holds
// its own children's stdin, which is what makes stopping them meaningful. A
// session spawned elsewhere has no such handle here, so the call is refused with
// the reason instead of signalling a process this daemon does not own.
func (s *RosterToolServer) stopAgent(
	ctx context.Context,
	args json.RawMessage,
	sessionID string,
) (string, error) {
	var body agentArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" {
		return "", errors.Errorf(ctx, "agent_id is required")
	}
	row, err := s.Roster.Get(ctx, body.AgentID)
	if err != nil {
		return "", err
	}
	if row.Rehydrated {
		return "", errors.Wrapf(
			ctx, ErrAgentNotOwned,
			"stop_agent acts only on a session this wrapper spawned; %s is not one "+
				"(it was adopted from the ledger). Tab and cluster workers are out of this slice's reach",
			body.AgentID,
		)
	}
	if s.Supervisor == nil {
		return "", errors.Errorf(ctx, "stop_agent is not wired")
	}
	if err := s.Supervisor.Stop(ctx, row.SessionID); err != nil {
		return "", errors.Wrapf(ctx, err, "stop agent %q failed", body.AgentID)
	}
	s.recordStop(ctx, row, sessionID)
	return marshalJSON(ctx, map[string]string{
		"agent_id": body.AgentID,
		"state":    "stopped",
	})
}

// recordStop writes the ledger row a stop would otherwise leave silent.
//
// It writes ONLY when no row exists for the session. A POST replaces a row
// outright, so re-posting one this process did not write would blank the fields
// its owner had recorded — the reason the node client grew a PATCH. Until this
// client has one, an existing row is left alone.
func (s *RosterToolServer) recordStop(ctx context.Context, row *AgentRow, callerSessionID string) {
	if s.Ledger == nil {
		return
	}
	rows, err := s.Ledger.Rows(ctx)
	if err != nil || rows == nil {
		log.Printf("WARNING: ledger read failed while recording a stop: %v", err)
		return
	}
	for _, existing := range rows {
		if existing.SessionID == row.SessionID {
			return
		}
	}
	if err := s.Ledger.Post(ctx, callerSessionID, LedgerRow{
		SessionID: row.SessionID,
		AgentID:   row.AgentID,
		Label:     row.Label,
		Cwd:       row.Cwd,
		Mode:      "interactive",
		Location:  "local",
	}); err != nil {
		log.Printf("WARNING: ledger post for stopped session %s failed: %v", row.SessionID, err)
	}
}

// writeRosterError writes the repo's {error:{code,message}} shape with a named
// code, so a caller can tell a refusal apart from a transport fault.
func writeRosterError(resp http.ResponseWriter, status int, code string, message string) {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(status)
	_ = json.NewEncoder(resp).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

// rosterToolDefinitions describes the three tools to an MCP client.
func rosterToolDefinitions() []any {
	return []any{
		toolDef(ToolListAgents,
			"All sessions with status, the call each is inside right now, and how many "+
				"are waiting on input. Each row carries `current_tool_call` and "+
				"`last_message`, so one sweep says what every worker is doing without a pane. "+
				"Rows adopted from the ledger carry `rehydrated: true` — their status is a "+
				"read-time value, not a live measurement.",
			map[string]any{}),
		toolDef(ToolAgentStatus,
			"One session: status, last assistant message, the current tool call, and its "+
				"registry status. `current_tool_call` is the call the worker is inside right "+
				"now and how long it has been held, or null when nothing is in flight. "+
				"`awaiting_input` is null — not false — when the registry does not list the session.",
			map[string]any{"agent_id": stringProp("id of the agent to read")},
			"agent_id"),
		toolDef(ToolStopAgent,
			"Stop a session this wrapper spawned. Returns the agent id and the resulting state. "+
				"A session spawned elsewhere is refused with a named error: this daemon holds "+
				"only its own children.",
			map[string]any{"agent_id": stringProp("id of the agent to stop")},
			"agent_id"),
	}
}
