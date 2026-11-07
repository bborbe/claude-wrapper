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

	"github.com/bborbe/claude-wrapper/pkg/a2a"
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
	ToolSpawnAgent       ToolName = "spawn_agent"
	ToolSendAgentMessage ToolName = "send_agent_message"
	ToolListAgents       ToolName = "list_agents"
	ToolAgentStatus      ToolName = "agent_status"
	ToolStopAgent        ToolName = "stop_agent"
)

// RosterToolNames is every tool the roster face exposes, in listing order.
//
// Five of the supervisor's nine. The remaining four are the park and the local
// tab/headless spawn surfaces, which have nothing to act on until those slices
// land.
func RosterToolNames() []ToolName {
	return []ToolName{
		ToolSpawnAgent, ToolSendAgentMessage, ToolListAgents, ToolAgentStatus, ToolStopAgent,
	}
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
	// A2A owns the docker / k8s workers: the create and stop calls, the turn
	// plane and the push channel. Nil means the A2A tools refuse, which is the
	// correct answer for a daemon started without a controller configured.
	A2A *a2a.Manager
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
	case ToolSpawnAgent:
		return s.spawnAgent(ctx, args)
	case ToolSendAgentMessage:
		return s.sendAgentMessage(ctx, args)
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
	// The A2A workers this daemon created are appended to the roster: they have
	// no local child and no ledger row of their own, so the only place a caller
	// can see them is here.
	combined := make([]any, 0, len(rows))
	for _, row := range rows {
		combined = append(combined, row)
	}
	if s.A2A != nil {
		for _, agent := range s.A2A.List() {
			combined = append(combined, agent.View(s.A2A.Registry))
		}
	}
	return marshalJSON(ctx, combined)
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
	if agent := s.a2aAgent(ctx, body.AgentID); agent != nil {
		return marshalJSON(ctx, agent.View(s.A2A.Registry))
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
	// An A2A worker is stopped on its controller, keyed on worker id — a task can
	// be respawned after its first worker died, so the task name is not a handle.
	if agent := s.a2aAgent(ctx, body.AgentID); agent != nil {
		if err := s.A2A.Stop(ctx, agent.AgentID); err != nil {
			return "", err
		}
		return marshalJSON(ctx, map[string]string{
			"agent_id": agent.AgentID,
			"state":    "stopped",
		})
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

// spawnAgentArgs is every argument a spawn call may carry.
type spawnAgentArgs struct {
	Target      string            `json:"target"`
	Task        string            `json:"task"`
	Vault       string            `json:"vault"`
	Prompt      string            `json:"prompt"`
	Resume      bool              `json:"resume"`
	Policy      string            `json:"policy"`
	Interactive *bool             `json:"interactive"`
	WindowID    string            `json:"window_id"`
	Role        string            `json:"role"`
	Env         map[string]string `json:"env"`
	Shipping    bool              `json:"shipping"`
}

// spawnAgent creates a worker on an a2a-worker-controller.
//
// ⚠️ ONLY THE A2A TARGETS ARE PORTED IN THIS SLICE. A `local` spawn — a WezTerm
// tab or an in-process headless worker — belongs to a later slice, and refusing
// by name is what keeps a caller from reading a missing feature as a broken one.
func (s *RosterToolServer) spawnAgent(ctx context.Context, args json.RawMessage) (string, error) {
	var body spawnAgentArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if !a2a.IsTarget(ctx, body.Target) {
		return "", errors.Errorf(
			ctx,
			"spawn_agent with target %q is not available in this daemon: only the A2A "+
				"targets `docker`, `k8s` and `k8s:<cluster>` are ported. Local tab and "+
				"headless spawning is a later slice",
			body.Target,
		)
	}
	if s.A2A == nil {
		return "", errors.Errorf(
			ctx,
			"spawn_agent is not wired: no A2A controller configuration was loaded, so no "+
				"worker could be created. Set `a2a.url` and a controller token",
		)
	}
	agent, err := s.A2A.Spawn(ctx, a2a.SpawnArgs{
		Target:      body.Target,
		Task:        body.Task,
		Vault:       body.Vault,
		Prompt:      body.Prompt,
		Resume:      body.Resume,
		Policy:      body.Policy,
		Interactive: body.Interactive,
		WindowID:    body.WindowID,
		Role:        body.Role,
		Env:         body.Env,
		Shipping:    body.Shipping,
	})
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, agent.View(s.A2A.Registry))
}

// sendAgentMessage sends one further turn to an existing A2A worker.
func (s *RosterToolServer) sendAgentMessage(
	ctx context.Context,
	args json.RawMessage,
) (string, error) {
	var body struct {
		AgentID string `json:"agent_id"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" {
		return "", errors.Errorf(ctx, "agent_id is required")
	}
	if s.A2A == nil {
		return "", errors.Errorf(ctx, "send_agent_message is not wired")
	}
	result, err := s.A2A.SendMessage(ctx, body.AgentID, body.Text)
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, map[string]any{
		"agent_id":   body.AgentID,
		"state":      result.State,
		"context_id": result.ContextID,
		"reply":      result.Reply,
	})
}

// a2aAgent resolves an agent id to an A2A record, or nil.
func (s *RosterToolServer) a2aAgent(ctx context.Context, agentID string) *a2a.Agent {
	if s.A2A == nil || agentID == "" {
		return nil
	}
	agent, err := s.A2A.Get(ctx, agentID)
	if err != nil {
		return nil
	}
	return agent
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
		toolDef(ToolSpawnAgent,
			"Create one worker on an a2a-worker-controller and return its agent id "+
				"immediately — the first turn is sent asynchronously, so the record carries "+
				"no result yet. `target` is `docker` (a container on this Mac), `k8s` (a pod "+
				"on the default cluster, nuke dev unless `a2a.defaultCluster` says otherwise) "+
				"or `k8s:<cluster>`. `task` is REQUIRED and names the vault task the worker "+
				"is opened for. The local-only arguments `interactive`, `resume`, `policy`, "+
				"`window_id`, `role`, `env` and `shipping` are REFUSED rather than ignored: "+
				"the controller creates the worker, so there is no local process here to set "+
				"them on.",
			map[string]any{
				"target": stringProp("docker, k8s, or k8s:<cluster>"),
				"task":   stringProp("the vault task the worker is opened for"),
				"vault":  stringProp("the vault the task lives in; required when the name is not unique"),
				"prompt": stringProp("the first turn to send; omitted means create only"),
			},
			"target", "task"),
		toolDef(ToolSendAgentMessage,
			"Send one further turn to an A2A worker, continuing its conversation by "+
				"`contextId`. Returns the turn's terminal state and the assistant text.",
			map[string]any{
				"agent_id": stringProp("id of the agent to write to"),
				"text":     stringProp("the user turn to send"),
			},
			"agent_id", "text"),
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
