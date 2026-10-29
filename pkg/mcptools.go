// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
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
	// Sequencer runs the model change's two steps. It is the same sequencer the
	// REST route uses, so the two faces cannot drift in how a change is
	// delivered or when it is refused.
	Sequencer Sequencer
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
	defaultPermissionMode PermissionMode = PermissionModeBypassPermissions
)

// ToolName is the name of one MCP tool. The set is closed: AvailableToolNames
// is the single list, and the dispatch switch is checked against it.
type ToolName string

// Tool names as they appear to an MCP client.
const (
	ToolSpawnSession ToolName = "spawn_session"
	ToolSendMessage  ToolName = "send_message"
	ToolListSessions ToolName = "list_sessions"
	ToolGetSession   ToolName = "get_session"
	ToolKillSession  ToolName = "kill_session"
	// ToolGetPending reads what a session is parked on, with the options the
	// question carries, so a caller can choose without a second round-trip.
	ToolGetPending ToolName = "get_pending"
	// ToolAnswerPending answers the request get_pending returned.
	ToolAnswerPending ToolName = "answer_pending"
	// ToolSetModel changes the model of a running session and reports what
	// actually served the next turn.
	ToolSetModel ToolName = "set_model"
	// ToolSetPermissionMode changes the permission mode of a running session and
	// reports the mode the child itself answers with.
	ToolSetPermissionMode ToolName = "set_permission_mode"
)

// AvailableToolNames is every tool this server exposes, in listing order.
func AvailableToolNames() []ToolName {
	return []ToolName{
		ToolSpawnSession,
		ToolSendMessage,
		ToolListSessions,
		ToolGetSession,
		ToolKillSession,
		ToolGetPending,
		ToolAnswerPending,
		ToolSetModel,
		ToolSetPermissionMode,
	}
}

// PermissionMode is a --permission-mode value a child accepts. The set is
// closed: AvailablePermissionModes is the single list.
type PermissionMode string

// The permission modes a child accepts.
const (
	PermissionModeDefault           PermissionMode = "default"
	PermissionModeAcceptEdits       PermissionMode = "acceptEdits"
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
	PermissionModePlan              PermissionMode = "plan"
)

// AvailablePermissionModes is every mode spawn_session will pass to a child.
func AvailablePermissionModes() []PermissionMode {
	return []PermissionMode{
		PermissionModeDefault,
		PermissionModeAcceptEdits,
		PermissionModeBypassPermissions,
		PermissionModePlan,
	}
}

// isPermissionMode reports whether a client-supplied mode is one this server
// will hand to Spawn. An unrecognised value would start a child that behaves
// differently from what the caller asked for, with nothing reporting the typo.
func isPermissionMode(mode string) bool {
	for _, known := range AvailablePermissionModes() {
		if string(known) == mode {
			return true
		}
	}
	return false
}

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
	// Fail fast and visibly. Without this a server constructed without its
	// collaborators answers tools/list happily and then panics on the first
	// send_message, which reads as a client bug rather than a wiring bug.
	if s.Supervisor == nil || s.Locks == nil {
		resp.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// Rejected BEFORE any parsing. A localhost HTTP server is reachable by any
	// page in the operator's browser through DNS rebinding: the attacker's
	// hostname is re-resolved to 127.0.0.1, the request is then same-origin, and
	// a text/plain POST is a CORS "simple request" that skips preflight
	// entirely. Without this check such a page could call spawn_session — a
	// child with every permission gate disabled and an arbitrary cwd — which is
	// remote code execution as the daemon user.
	if !originAllowed(req) {
		resp.WriteHeader(http.StatusForbidden)
		return
	}
	switch req.Method {
	case http.MethodPost:
		if !isJSONRequest(req) {
			resp.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		s.handlePost(resp, req)
	case http.MethodDelete:
		resp.WriteHeader(http.StatusOK)
	default:
		resp.Header().Set("Allow", "POST, DELETE")
		resp.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// originAllowed rejects a request whose Host is not loopback, and one whose
// Origin names a non-loopback host.
//
// An absent Origin is allowed on purpose: a non-browser client — Codex, curl —
// sends none, and the Host check already bounds the request to this machine.
// A present Origin must be loopback, which is what stops a rebinding page: its
// Origin is the attacker's hostname even though the socket reached 127.0.0.1.
func originAllowed(req *http.Request) bool {
	// The socket's own local address is what the listener ACTUALLY bound. The
	// Host header is client-supplied, so on a non-loopback bind any remote
	// client could send "Host: localhost" and pass the check below; this makes
	// the guard follow the real bind. Absent in a recorder under test, which is
	// why the check is conditional.
	if addr, ok := req.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if host, _, err := net.SplitHostPort(addr.String()); err == nil &&
			!isLoopbackHost(host) {
			return false
		}
	}
	if !isLoopbackHost(req.Host) {
		return false
	}
	origin := req.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return isLoopbackHost(parsed.Host)
}

// isLoopbackHost reports whether host is a loopback name, with or without a port.
func isLoopbackHost(host string) bool {
	name := host
	if withoutPort, _, err := net.SplitHostPort(host); err == nil {
		name = withoutPort
	}
	switch strings.ToLower(strings.Trim(name, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// isJSONRequest reports whether the body is declared as JSON.
//
// An absent Content-Type is REJECTED rather than tolerated: a POST with no
// Content-Type is also a CORS simple request, so accepting it would reopen the
// preflight bypass this check exists to close.
func isJSONRequest(req *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func (s *MCPToolServer) handlePost(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	// The turn deadline is cleared for the duration: send_message may legitimately
	// wait out its whole TurnTimeout, which outlasts the server's 60-second
	// WriteTimeout, and a response written after that deadline never reaches the
	// client — the documented timeout error would be undeliverable. Same reason,
	// same idiom, as the /commands route.
	//
	// Best effort: a recorder under test does not implement deadlines.
	if err := http.NewResponseController(resp).SetWriteDeadline(time.Time{}); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		writeRPC(resp, nil, nil,
			&rpcError{Code: -32603, Message: "clear write deadline failed"})
		return
	}
	// One byte past the cap, so an oversized body is diagnosed rather than
	// silently truncated into a parse error the client cannot act on.
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
	switch ToolName(name) {
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
	case ToolGetPending:
		return s.getPending(ctx, args)
	case ToolAnswerPending:
		return s.answerPending(ctx, args)
	case ToolSetModel:
		return s.setModel(ctx, args)
	case ToolSetPermissionMode:
		return s.setPermissionMode(ctx, args)
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
		mode = string(defaultPermissionMode)
	}
	if !isPermissionMode(mode) {
		return "", errors.Errorf(ctx, "unknown permission_mode %q", mode)
	}
	session, err := s.Supervisor.Spawn(ctx, SpawnOptions{
		Cwd:            body.Cwd,
		Model:          body.Model,
		PermissionMode: mode,
	})
	if err != nil {
		return "", errors.Wrapf(ctx, err, "spawn session failed")
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

	// The lock excludes other WRITERS while they hold it; it does not cover a
	// turn already open on the child. The timeout path proves the difference:
	// awaitTurn gives up and the deferred release runs, but the child's turn is
	// still generating. Writing then would open a second turn on top of the
	// first, and the two results become indistinguishable — the new call could
	// return the old turn's text. Same guard, same reason, as Sequencer.Run.
	session, err := s.Supervisor.Get(ctx, body.SessionID)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "get session %q failed", body.SessionID)
	}
	if session.PendingTurns > 0 ||
		(session.State != StateIdle && session.State != StateStarting) {
		return "", errors.Errorf(ctx, "session %q is %s with %d turn(s) pending",
			body.SessionID, session.State, session.PendingTurns)
	}

	replay, live, cancel, err := s.Supervisor.Subscribe(ctx, body.SessionID)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "subscribe to session %q failed", body.SessionID)
	}
	defer cancel()

	// Subscribed BEFORE the write, and the expected index taken from the
	// replay: a result arriving between the write and the subscribe would
	// otherwise be missed and the turn would wait out its whole timeout.
	expected := expectedTurn(replay)
	if err := s.Supervisor.Send(ctx, body.SessionID, body.Text); err != nil {
		return "", errors.Wrapf(ctx, err, "send turn to session %q failed", body.SessionID)
	}
	event, question, err := s.awaitTurnOrQuestion(ctx, live, expected, body.SessionID)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "wait for turn %d failed", expected)
	}
	// A turn that parks must return NOW, not when the turn closes. The child's
	// permission hold is 90s; a send that waited for the turn would hand the
	// caller a question it can no longer answer, which is the recorded-but-never-
	// delivered failure this phase removes. Returning the question early is what
	// makes the answer land inside the hold.
	if question != nil {
		pending, marshalErr := marshalJSON(ctx, question)
		if marshalErr != nil {
			return "", errors.Wrapf(ctx, marshalErr, "encode pending question failed")
		}
		return "parked: " + pending, nil
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

type pendingArgs struct {
	SessionID string `json:"session_id"`
}

// getPending returns the request a session is parked on, or null.
//
// Its own tool rather than a field on get_session: get_session answers "what is
// this session doing" and is polled often, while this payload is large and only
// relevant while the session is actually waiting.
func (s *MCPToolServer) getPending(ctx context.Context, args json.RawMessage) (string, error) {
	var body pendingArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.SessionID == "" {
		return "", errors.Errorf(ctx, "session_id is required")
	}
	session, err := s.Supervisor.Get(ctx, body.SessionID)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "get session %q failed", body.SessionID)
	}
	if session.Question == nil {
		return "null", nil
	}
	return marshalJSON(ctx, session.Question)
}

type answerPendingArgs struct {
	SessionID string            `json:"session_id"`
	RequestID string            `json:"request_id"`
	Behavior  string            `json:"behavior"`
	Answers   map[string]string `json:"answers"`
}

// answerPending posts the caller's decision through the same Supervisor.Answer
// path the REST route uses, so the two faces cannot drift apart.
func (s *MCPToolServer) answerPending(ctx context.Context, args json.RawMessage) (string, error) {
	var body answerPendingArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.SessionID == "" || body.RequestID == "" {
		return "", errors.Errorf(ctx, "session_id and request_id are required")
	}
	answer := Answer{Kind: AnswerKindPermission, Behavior: body.Behavior}
	if len(body.Answers) > 0 {
		answer = Answer{Kind: AnswerKindQuestion, Answers: body.Answers}
	}
	if answer.Kind == AnswerKindPermission && answer.Behavior == "" {
		answer.Behavior = "allow"
	}
	if err := s.Supervisor.Answer(ctx, body.SessionID, body.RequestID, answer); err != nil {
		return "", errors.Wrapf(ctx, err, "answer request %q failed", body.RequestID)
	}
	return marshalJSON(ctx, map[string]string{"status": "answered"})
}

// awaitTurnOrQuestion blocks until the child closes turn `expected` OR registers
// a request it is parked on — whichever happens first.
//
// The question branch is what makes the phase work at all. The child's
// permission hold is 90s; waiting for the turn to close would hand the caller a
// question whose hold has already expired, so every answer would arrive after
// the child had already defaulted past it.
func (s *MCPToolServer) awaitTurnOrQuestion(
	ctx context.Context,
	live <-chan Event,
	expected int,
	sessionID string,
) (Event, *Question, error) {
	timeout := s.TurnTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return nil, nil, errors.Wrapf(ctx, waitCtx.Err(),
				"turn %d did not close within %s", expected, timeout)
		case <-poll.C:
			current, err := s.Supervisor.Get(ctx, sessionID)
			if err == nil && current.Question != nil {
				return nil, current.Question, nil
			}
		case event, open := <-live:
			if !open {
				return nil, nil, errors.Wrapf(ctx, ErrSessionExited,
					"stream closed before turn %d closed", expected)
			}
			if isTurnClosed(event, expected) {
				return event, nil, nil
			}
		}
	}
}

func (s *MCPToolServer) list(ctx context.Context) (string, error) {
	sessions, err := s.Supervisor.List(ctx)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "list sessions failed")
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
		return "", errors.Wrapf(ctx, err, "get session %q failed", body.SessionID)
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
		return "", errors.Wrapf(ctx, err, "stop session %q failed", body.SessionID)
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
type setModelArgs struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
}

// setModel changes a running session's model through the same sequencer the
// REST route uses, and reports what actually served the next turn.
func (s *MCPToolServer) setModel(ctx context.Context, args json.RawMessage) (string, error) {
	var body setModelArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if err := ValidateModelName(ctx, body.Model); err != nil {
		return "", err
	}
	if s.Sequencer == nil {
		return "", errors.Errorf(ctx, "model change is not wired")
	}
	before, err := s.Supervisor.Get(ctx, body.SessionID)
	if err != nil {
		return "", err
	}
	if _, err := s.Sequencer.Run(ctx, body.SessionID, ModelChangeMacro(body.Model)); err != nil {
		return "", err
	}
	after, err := s.Supervisor.Get(ctx, body.SessionID)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(ModelChange{
		Requested:     body.Model,
		PreviousModel: before.Model,
		ServedModel:   after.Model,
		Changed:       after.Model != before.Model,
	})
	if err != nil {
		return "", errors.Wrapf(ctx, err, "marshal model change failed")
	}
	return string(encoded), nil
}

type setPermissionModeArgs struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
}

// setPermissionMode changes a running session's permission mode and reports the
// mode the child itself answers with.
func (s *MCPToolServer) setPermissionMode(
	ctx context.Context,
	args json.RawMessage,
) (string, error) {
	var body setPermissionModeArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.Mode == "" {
		return "", errors.Errorf(ctx, "mode is required")
	}
	if s.Locks == nil {
		return "", errors.Errorf(ctx, "permission mode change is not wired")
	}
	release, err := s.Locks.Acquire(ctx, body.SessionID)
	if err != nil {
		return "", err
	}
	defer release()
	reported, err := s.Supervisor.SetPermissionMode(ctx, body.SessionID, body.Mode)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(map[string]string{
		"status":          "changed",
		"permission_mode": reported,
	})
	if err != nil {
		return "", errors.Wrapf(ctx, err, "marshal permission mode result failed")
	}
	return string(encoded), nil
}

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
		toolDef(ToolGetPending,
			"Read what a session is parked on, with the options its question carries. "+
				"Returns null when the session is not waiting on anything.",
			map[string]any{"session_id": stringProp("id of the session to read")},
			"session_id"),
		toolDef(ToolAnswerPending,
			"Answer the request get_pending returned. Supply behavior (allow|deny) for a "+
				"permission request, or answers (question text -> chosen label) for an AskUserQuestion.",
			map[string]any{
				"session_id": stringProp("id of the session to answer"),
				"request_id": stringProp("the pending request's id, from get_pending"),
				"behavior":   stringProp("allow or deny, for a permission request"),
				"answers":    map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			},
			"session_id", "request_id"),
		toolDef(ToolSetModel,
			"Change a running session's model and return what actually served the next turn. "+
				"The served model is read from the child's own assistant event, so a name that "+
				"resolves to a different backend reports the served name rather than echoing it.",
			map[string]any{
				"session_id": stringProp("id of the session to change"),
				"model":      stringProp("model to switch the child to"),
			},
			"session_id", "model"),
		toolDef(ToolSetPermissionMode,
			"Change a running session's permission mode and return the mode the child itself "+
				"reports. Modes: default, acceptEdits, bypassPermissions, plan.",
			map[string]any{
				"session_id": stringProp("id of the session to change"),
				"mode":       stringProp("permission mode to switch the child to"),
			},
			"session_id", "mode"),
	}
}

func toolDef(name ToolName, description string, props map[string]any, required ...string) map[string]any {
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
