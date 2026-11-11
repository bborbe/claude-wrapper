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
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

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

// maxAwaitPermission bounds how long await_permission holds a request open.
const maxAwaitPermission = 5 * time.Minute

// Roster tool names, as they appear to an MCP client.
const (
	ToolSpawnAgent             ToolName = "spawn_agent"
	ToolSendAgentMessage       ToolName = "send_agent_message"
	ToolListAgents             ToolName = "list_agents"
	ToolAgentStatus            ToolName = "agent_status"
	ToolStopAgent              ToolName = "stop_agent"
	ToolSetAgentPermissionMode ToolName = "set_agent_permission_mode"
	ToolAnswerPermission       ToolName = "answer_permission"
	ToolAwaitPermission        ToolName = "await_permission"
	ToolPendingPermissions     ToolName = "pending_permissions"
	ToolReadPushConfig         ToolName = "read_push_config"
)

// RosterToolNames is every tool the roster face exposes, in listing order.
//
// Nine of the supervisor's nine — the last one, `read_push_config`, is the read
// half of the A2A push round trip — plus `set_agent_permission_mode`, the live
// mode switch a manager needs because a headless worker has no pane to press
// Shift+Tab in.
func RosterToolNames() []ToolName {
	return []ToolName{
		ToolListAgents,
		ToolAgentStatus,
		ToolStopAgent,
		ToolSpawnAgent,
		ToolSendAgentMessage,
		ToolSetAgentPermissionMode,
		ToolAnswerPermission,
		ToolAwaitPermission,
		ToolPendingPermissions,
		ToolReadPushConfig,
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
	// Decider records a manager answer. Nil is legal — the answer still lands,
	// and only its audit row is skipped.
	Decider *PermissionDecider
	// A2A owns the docker / k8s workers: the create and stop calls, the turn
	// plane and the push channel. Nil means the A2A tools refuse, which is the
	// correct answer for a daemon started without a controller configured.
	A2A *a2a.Manager
	// Spawner opens the WezTerm tab for a `local` spawn_agent. Nil means the local
	// target refuses, which is the correct answer for a daemon with no terminal.
	Spawner TabSpawner
	// SpawnMode reads SUPERVISOR_SPAWN_MODE and `spawn.mode`, the two operator
	// sources a `local` spawn without an explicit `interactive` resolves through.
	// Nil means neither is configured, so the chain falls to the argument and the
	// built-in default only.
	SpawnMode SpawnModeSourcesReader
	// Vaults resolves a headless spawn's `vault` to its directory. Nil means a
	// headless spawn must pass `cwd`.
	Vaults VaultLister
	// Tabs types into a tab worker's pane for send_agent_message. Nil means a tab
	// worker cannot be messaged.
	Tabs TabClient
	// Now bounds the pane-send readiness and confirmation polls. Nil falls back to
	// the system clock, which is what a test wants to override.
	Now libtime.CurrentDateTimeGetter
	// Caps enforces the fleet-wide worker cap, manager cap and start rate before
	// a local spawn — tab or headless — creates anything. Nil disables
	// enforcement, which is correct only for a unit test; the daemon always wires
	// it.
	Caps SpawnCaps
	// Registry resolves a headless worker's session id to the name it registered,
	// which is the address the caller's `SendMessage` takes. Nil means a headless
	// worker's name cannot be resolved, and send_agent_message says so rather than
	// naming a channel the caller cannot address.
	Registry SessionRegistry
	// Locks is the same per-session sequence lock the REST and /mcp faces take.
	// set_agent_permission_mode fails fast on it rather than queuing, so the three
	// faces cannot disagree about whether a change waits or refuses. Nil means the
	// switch refuses, which is correct only for a daemon started without one.
	Locks *SessionLocks
	// PermissionModeLog records every switch. Nil is legal — the switch still
	// lands in the child, and only its audit line is skipped, which the reply
	// reports as `recorded: false`.
	PermissionModeLog PermissionModeLog
}

// currentDateTime returns the clock the pane polls run on.
func (s *RosterToolServer) currentDateTime() libtime.CurrentDateTimeGetter {
	if s.Now == nil {
		return libtime.NewCurrentDateTime()
	}
	return s.Now
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
	// The spawn caps may PACE a start for up to MaxStartDelay (5 minutes), which
	// outlasts the server's 60-second WriteTimeout. A response written after that
	// deadline never reaches the caller, who then retries — producing the very
	// burst the start rate exists to bound. Same idiom, same reason, as the
	// /commands route and the Codex MCP face.
	if err := clearRosterWriteDeadline(ctx, resp); err != nil {
		writeRPC(resp, nil, nil, &rpcError{Code: -32603, Message: err.Error()})
		return
	}
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

// clearRosterWriteDeadline removes the server's WriteTimeout for this response.
//
// Best effort: a recorder under test does not implement deadlines, and refusing
// the call over an unsupported deadline would make the face untestable for no
// gain.
func clearRosterWriteDeadline(ctx context.Context, resp http.ResponseWriter) error {
	if err := http.NewResponseController(resp).SetWriteDeadline(time.Time{}); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return errors.Wrapf(ctx, err, "clear write deadline failed")
	}
	return nil
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
		return s.spawnAgent(ctx, args, sessionID)
	case ToolSendAgentMessage:
		return s.sendAgentMessage(ctx, args)
	case ToolListAgents:
		return s.listAgents(ctx)
	case ToolAgentStatus:
		return s.agentStatus(ctx, args)
	case ToolStopAgent:
		return s.stopAgent(ctx, args, sessionID)
	case ToolSetAgentPermissionMode:
		return s.setAgentPermissionMode(ctx, args, sessionID)
	case ToolAnswerPermission:
		return s.answerPermission(ctx, args, sessionID)
	case ToolAwaitPermission:
		return s.awaitPermission(ctx, args)
	case ToolPendingPermissions:
		return s.pendingPermissions(ctx)
	case ToolReadPushConfig:
		return s.readPushConfig(ctx, args)
	default:
		return "", errors.Errorf(ctx, "unknown tool %q", name)
	}
}

// spawnLocalAgent creates a wrapper child and delivers its opening turn.
//
// ⚠️ It is in-process on purpose: the daemon that answers the call is the daemon
// that holds the child, so there is no HTTP hop to itself and no second process
// to keep alive. The child's opening turn is a SEPARATE call because the wrapper
// has no `prompt` field on spawn — a spawn that only creates the child leaves it
// idle, which looks exactly like one that is thinking.
// ShippingPermissionMode is the mode a headless worker runs in when the caller marks
// it `shipping`.
//
// ⚠️ `acceptEdits` and not `bypassPermissions`: it auto-accepts edits and stops the
// auto-mode classifier from gating a routine commit or push, while `permissions.allow`
// and every PreToolUse hook still apply — so `deny-prod-deploy.sh` keeps refusing a prod
// deploy in a shipping worker.
const ShippingPermissionMode = "acceptEdits"

// The two values `permission_mode_source` reports, so a caller can tell a mode the flag
// decided from the daemon's default. A dropped flag is then visible in the reply.
const (
	PermissionModeSourceShipping = "shipping"
	PermissionModeSourceDefault  = "default"
)

// headlessPermissionMode maps `shipping` to the mode a headless child starts in.
//
// ⚠️ This is the ONLY source of the mode on the headless path: no argument, env var or
// config key can name another, so `bypassPermissions` is unreachable through `spawn_agent`.
func headlessPermissionMode(shipping bool) (mode, source string) {
	if shipping {
		return ShippingPermissionMode, PermissionModeSourceShipping
	}
	return DefaultPermissionMode, PermissionModeSourceDefault
}

func (s *RosterToolServer) spawnLocalAgent(
	ctx context.Context,
	body spawnAgentArgs,
	sessionID string,
	resolution SpawnModeResolution,
) (string, error) {
	if body.Interactive != nil && *body.Interactive {
		return "", errors.Errorf(
			ctx,
			"interactive spawn is not this slice's surface — a tab worker is spawned and steered by the tab-spawn slice",
		)
	}
	if s.Supervisor == nil {
		return "", errors.Errorf(ctx, "spawn_agent is not wired")
	}
	cwd, err := s.headlessCwd(ctx, body)
	if err != nil {
		return "", err
	}
	if err := s.refuseTerminalResume(ctx, body, cwd); err != nil {
		return "", err
	}
	if err := s.enforceCaps(ctx, body, SpawnModeHeadless); err != nil {
		return "", err
	}
	mode, modeSource := headlessPermissionMode(body.Shipping)
	session, err := s.Supervisor.Spawn(ctx, SpawnOptions{
		Cwd:            cwd,
		Model:          s.headlessModel(ctx, body, cwd),
		PermissionMode: mode,
		Label:          body.Label,
		Role:           body.Role,
		Policy:         body.Policy,
		Resume:         body.Resume,
	})
	if err != nil {
		return "", errors.Wrapf(ctx, err, "spawn agent failed")
	}
	if body.Prompt != "" {
		if err := s.Supervisor.Send(ctx, session.ID, body.Prompt); err != nil {
			// The child is live by now and holds stdin. A spawn that throws here
			// leaves an idle process behind that looks exactly like a thinking
			// worker, so it is stopped rather than orphaned.
			_ = s.Supervisor.Stop(context.Background(), session.ID)
			return "", errors.Wrapf(ctx, err, "deliver opening turn failed")
		}
	}
	s.recordSpawn(ctx, session, sessionID, body, resolution.Source)
	return marshalJSON(ctx, map[string]string{
		"agent_id":    session.ID,
		"session_id":  session.ID,
		"state":       string(session.State),
		"cwd":         session.Cwd,
		"mode":        SpawnModeHeadless,
		"mode_source": resolution.Source,
		// ⚠️ Reported because the flag is otherwise unobservable: before this key
		// existed a caller passing `shipping: true` could not tell it had been
		// dropped, and every child started in `auto` regardless. Measured
		// 2026-11-11: 119 of 120 live children in `auto`, none in `acceptEdits`.
		"permission_mode":        mode,
		"permission_mode_source": modeSource,
	})
}

// headlessCwd resolves the directory a headless worker runs in.
//
// An explicit `cwd` alone is used as given — the v0.9.0 headless contract. A
// `vault` is resolved to its directory, because once `spawn.mode: headless`
// routes argument-less spawns here, a vault-only call that the tab path would
// have resolved must not land in this daemon's own working directory: that
// default started 27 workers in the wrong repo on 2026-10-03. Neither given is
// refused for the same reason.
func (s *RosterToolServer) headlessCwd(
	ctx context.Context,
	body spawnAgentArgs,
) (string, error) {
	if strings.TrimSpace(body.Vault) == "" {
		if strings.TrimSpace(body.Cwd) == "" {
			return "", errors.Wrapf(
				ctx,
				ErrUnknownVault,
				"a headless spawn requires `cwd` or `vault` — omitting both would run the "+
					"worker in this daemon's own working directory",
			)
		}
		return body.Cwd, nil
	}
	if s.Vaults == nil {
		return "", errors.Errorf(
			ctx,
			"a headless spawn named vault %q but this daemon has no vault lister wired, "+
				"so the vault cannot be resolved to a directory — pass `cwd`",
			body.Vault,
		)
	}
	vaults, err := s.Vaults.List(ctx)
	if err != nil {
		return "", err
	}
	serverCwd, _ := os.Getwd()
	target, err := ResolveWorkerTarget(ctx, vaults, body.Cwd, body.Vault, serverCwd)
	if err != nil {
		return "", err
	}
	return target.Cwd, nil
}

// terminalTaskStatus reports whether a task status means the work is over.
//
// `hold` and a deferred task are deliberately NOT terminal: a parked or deferred
// worker is exactly what the park-watch resume exists to bring back, so refusing
// those would break the leg this guard protects.
func terminalTaskStatus(status string) bool {
	return status == "completed" || status == "aborted"
}

// refuseTerminalResume refuses a resume whose task is already finished.
//
// The resume that fires in production is `spawn_agent(resume=<id>)` with no
// `task` and no `vault` — the keepalive actor's path-A call — so the session id
// is the only identity available, and the task is resolved by the
// `claude_session_id:` it carries. A caller that DOES name a task is checked on
// that too: a task file may predate the id being written, and a completed task
// named by the caller is the same defect whether or not the id is recorded yet.
//
// An unresolvable id is ALLOWED with one log line. No task claims the session,
// so there is nothing to prove terminal, and refusing would break every existing
// path-A resume; the log line is what keeps that fail-open observable.
func (s *RosterToolServer) refuseTerminalResume(
	ctx context.Context,
	body spawnAgentArgs,
	cwd string,
) error {
	session := strings.TrimSpace(body.Resume)
	if session == "" {
		return nil
	}
	ref := s.taskForResume(ctx, body, cwd)
	if ref == nil {
		// One line, and it asserts no CAUSE: the vault list may have been
		// unreadable or simply held no match, and a message naming the wrong one
		// would send a reader looking in the wrong place.
		log.Printf(
			"resume guard: no task could be resolved for session %s — allowing the "+
				"resume unresolved",
			session,
		)
		return nil
	}
	if !terminalTaskStatus(ref.Status) {
		return nil
	}
	return errors.Wrapf(
		ctx,
		ErrTaskTerminal,
		"resume of session %s refused: its task %q in vault %q is %s, and a finished "+
			"task must not be handed a live worker again — closing is the fleet's only "+
			"way to reclaim a spawn slot",
		session,
		ref.Title,
		ref.Vault,
		ref.Status,
	)
}

// taskForResume resolves the task a resumed session belongs to, or nil.
//
// The session id is asked first and is authoritative: it names the conversation
// being continued. The named task is a SECOND signal for a caller that supplies
// one — a task file may predate its `claude_session_id:` being written — so both
// are consulted rather than one.
func (s *RosterToolServer) taskForResume(
	ctx context.Context,
	body spawnAgentArgs,
	cwd string,
) *TaskRef {
	var vaults []Vault
	if s.Vaults != nil {
		listed, err := s.Vaults.List(ctx)
		if err != nil {
			log.Printf(
				"resume guard: the vault list is unreadable (%v) — allowing session %s "+
					"unresolved rather than guessing a status",
				err,
				body.Resume,
			)
			return nil
		}
		vaults = listed
	}
	if ref := TaskBySessionID(ctx, vaults, body.Resume); ref != nil {
		return ref
	}
	if strings.TrimSpace(body.Task) == "" {
		return nil
	}
	vault, err := s.vaultFor(ctx, body, cwd)
	if err != nil || vault == nil {
		return nil
	}
	status := TaskStatus(vault.Path, body.Task)
	if status == "" {
		return nil
	}
	return &TaskRef{Title: body.Task, Status: status, Vault: vault.Name}
}

// headlessModel returns the model a headless spawn must pass to the child.
//
// The caller's explicit `model` always wins. Otherwise the task's own
// `launcher:` is read, then the vault's worker launcher — the same chain a tab
// spawn follows, which a headless worker otherwise bypasses completely because
// its child is a bare `claude --print` and the launcher script is never
// executed. Measured 2026-11-09: every headless worker ran on Claude Code's
// default model while every interactive tab read deepseek, because nothing ever
// passed `--model`.
//
// "" means no model could be resolved, and the child keeps its own default —
// never a guess, for the same reason ParseLauncherModel refuses one.
func (s *RosterToolServer) headlessModel(
	ctx context.Context,
	body spawnAgentArgs,
	cwd string,
) string {
	if model := strings.TrimSpace(body.Model); model != "" {
		return model
	}
	vault, err := s.vaultFor(ctx, body, cwd)
	if err != nil || vault == nil {
		return ""
	}
	if model := TaskLauncherModel(vault.Path, body.Task); model != "" {
		return model
	}
	launcher, _, err := launcherForVault(ctx, vault)
	if err != nil {
		return ""
	}
	return LauncherModel(launcher)
}

// vaultFor resolves the vault a spawn belongs to: the one it named, else the most
// specific configured vault containing the cwd. nil when neither resolves — a
// spawn with only a cwd outside every vault keeps its default model rather than
// borrowing another vault's launcher.
func (s *RosterToolServer) vaultFor(
	ctx context.Context,
	body spawnAgentArgs,
	cwd string,
) (*Vault, error) {
	if s.Vaults == nil {
		return nil, nil
	}
	vaults, err := s.Vaults.List(ctx)
	if err != nil {
		return nil, err
	}
	named, err := findVault(ctx, vaults, body.Vault)
	if err != nil || named != nil {
		return named, err
	}
	target, err := resolveByCwd(ctx, vaults, cwd, body.Vault)
	if err != nil {
		return nil, err
	}
	return findVault(ctx, vaults, target.Vault)
}

// recordSpawn files the ledger row a spawned child would otherwise lack.
//
// Role and ResumedFrom are recorded because the caps count BY them: a row with
// no role reads as a worker (so a headless manager would be charged to the
// worker cap and never to the manager cap), and a row with no `resumed_from`
// cannot be excluded as an auto-resume.
func (s *RosterToolServer) recordSpawn(
	ctx context.Context,
	session *Session,
	callerSessionID string,
	body spawnAgentArgs,
	modeSource string,
) {
	if s.Ledger == nil {
		return
	}
	if err := s.Ledger.Post(ctx, callerSessionID, LedgerRow{
		SessionID:  session.ID,
		AgentID:    session.ID,
		Label:      body.Label,
		Cwd:        session.Cwd,
		Mode:       SpawnModeHeadless,
		ModeSource: modeSource,
		// The REQUEST's role, not the session snapshot's: the row describes what was
		// asked for, and a session type that does not carry a role would otherwise
		// file a manager as a worker.
		Role:        body.Role,
		ResumedFrom: body.Resume,
		Location:    "local",
	}); err != nil {
		log.Printf("WARNING: ledger post for spawned session %s failed: %v", session.ID, err)
	}
}

// answerPermission settles a parked permission request.
func (s *RosterToolServer) answerPermission(
	ctx context.Context,
	args json.RawMessage,
	sessionID string,
) (string, error) {
	var body struct {
		AgentID   string `json:"agent_id"`
		RequestID string `json:"request_id"`
		Behavior  string `json:"behavior"`
	}
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" || body.RequestID == "" {
		return "", errors.Errorf(ctx, "agent_id and request_id are required")
	}
	if body.Behavior != "allow" && body.Behavior != "deny" {
		return "", errors.Errorf(ctx, "behavior must be allow or deny, got %q", body.Behavior)
	}
	if s.Supervisor == nil {
		return "", errors.Errorf(ctx, "answer_permission is not wired")
	}
	session, err := s.Supervisor.Get(ctx, body.AgentID)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "read agent %s", body.AgentID)
	}
	kind := AnswerKindPermission
	if session.Question != nil && session.Question.Kind != "" {
		kind = session.Question.Kind
	}
	if err := s.Supervisor.Answer(ctx, body.AgentID, body.RequestID, Answer{
		Kind:     kind,
		Behavior: body.Behavior,
	}); err != nil {
		return "", errors.Wrapf(ctx, err, "answer permission failed")
	}
	if s.Decider != nil && session.Question != nil {
		s.Decider.RecordSettled(
			ctx, body.AgentID, PolicyContext{Cwd: session.Cwd}, *session.Question,
			body.Behavior, DecidedByManager, "", nil,
		)
	}
	// Deliberately no per-argument log line: the caller-supplied strings are
	// attacker-controlled and the decided_by record already carries the verdict.
	_ = sessionID
	return marshalJSON(ctx, map[string]string{
		"agent_id":   body.AgentID,
		"request_id": body.RequestID,
		"behavior":   body.Behavior,
	})
}

// awaitPermission waits for a park to be settled, or returns a null answer.
//
// A null answer is a quiet window, not an error: the park may still be open, and
// a caller that treats a timeout as failure would re-ask a question already
// pending.
func (s *RosterToolServer) awaitPermission(
	ctx context.Context,
	args json.RawMessage,
) (string, error) {
	var body struct {
		AgentID        string `json:"agent_id"`
		RequestID      string `json:"request_id"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" {
		return "", errors.Errorf(ctx, "agent_id is required")
	}
	if s.Supervisor == nil {
		return "", errors.Errorf(ctx, "await_permission is not wired")
	}
	requestID := body.RequestID
	if requestID == "" {
		session, err := s.Supervisor.Get(ctx, body.AgentID)
		if err != nil {
			return "", errors.Wrapf(ctx, err, "read agent %s", body.AgentID)
		}
		if session.Question == nil {
			return marshalJSON(ctx, map[string]any{"answered": false, "answer": nil})
		}
		requestID = session.Question.RequestID
	}
	timeout := time.Duration(body.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	// Bounded: the wait holds a request goroutine, and the caller names the
	// number. A park's own window is the policy timeout, so waiting past it only
	// holds a connection open for a gate that has already been decided.
	if timeout > maxAwaitPermission {
		timeout = maxAwaitPermission
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pending, err := s.Supervisor.PollAnswer(ctx, requestID)
		if err != nil {
			return "", errors.Wrapf(ctx, err, "poll answer failed")
		}
		if pending.Answered {
			return marshalJSON(ctx, map[string]any{
				"answered":   true,
				"request_id": requestID,
				"behavior":   pending.Answer.Behavior,
				"answers":    pending.Answer.Answers,
				"message":    pending.Answer.Message,
			})
		}
		select {
		case <-ctx.Done():
			return marshalJSON(ctx, map[string]any{"answered": false, "answer": nil})
		case <-time.After(250 * time.Millisecond):
		}
	}
	return marshalJSON(ctx, map[string]any{"answered": false, "request_id": requestID, "answer": nil})
}

// pendingPermissions lists every park this process holds.
func (s *RosterToolServer) pendingPermissions(ctx context.Context) (string, error) {
	if s.Supervisor == nil {
		return "", errors.Errorf(ctx, "pending_permissions is not wired")
	}
	sessions, err := s.Supervisor.List(ctx)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "list sessions failed")
	}
	rows := make([]map[string]any, 0, len(sessions))
	for index := range sessions {
		if sessions[index].Question == nil {
			continue
		}
		rows = append(rows, map[string]any{
			"agent_id":   sessions[index].ID,
			"request_id": sessions[index].Question.RequestID,
			"tool":       sessions[index].Question.ToolName,
			"kind":       string(sessions[index].Question.Kind),
			"cwd":        sessions[index].Cwd,
		})
	}
	return marshalJSON(ctx, rows)
}

// readPushConfig reads a worker's push config back from the worker itself.
//
// The A2A manager owns the gate and the call: this method only decodes the
// argument and refuses a daemon that was started without a controller, so a
// caller gets a named reason rather than an empty list that would read as "the
// worker holds nothing".
func (s *RosterToolServer) readPushConfig(
	ctx context.Context,
	args json.RawMessage,
) (string, error) {
	var body agentArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" {
		return "", errors.Errorf(ctx, "agent_id is required")
	}
	if s.A2A == nil {
		return "", errors.Errorf(
			ctx, "read_push_config is not wired — this daemon was started without an A2A controller",
		)
	}
	result, err := s.A2A.ReadPushConfig(ctx, body.AgentID)
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, result)
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

// switchablePermissionModes is the closed set set_agent_permission_mode accepts.
//
// ⚠️ Exactly two values, matched case-sensitively, and deliberately not more:
//   - `acceptEdits` stops the auto-mode classifier from gating an edit, a commit or
//     a push, while `permissions.allow` and every PreToolUse hook still apply — so
//     `deny-prod-deploy.sh` keeps refusing a prod deploy in a switched worker.
//   - `auto` is what every headless child starts in, so a worker switched away from
//     it must be switchable back.
//
// `bypassPermissions` stays out because it lifts the permission-prompt path
// entirely — no park, no policy consult, nothing for the operator to see — and a
// manager is itself a model session. `plan` stalls a worker that has to ship,
// `dontAsk` silently denies everything outside the allow-list, and `default`
// parks every tool for the manager. The cap is the operator's decision of
// 2026-11-11.
var switchablePermissionModes = []string{ShippingPermissionMode, "auto"}

// allowedSwitchablePermissionMode reports whether a caller-supplied mode is one
// this tool may hand to a child.
func allowedSwitchablePermissionMode(mode string) bool {
	return slices.Contains(switchablePermissionModes, mode)
}

// setAgentPermissionMode switches a RUNNING local headless worker's permission
// mode.
//
// ⚠️ Why this exists: the documented remedy for an auto-mode classifier denial is
// "switch to accept edits (Shift+Tab)", and a headless worker has no pane, so the
// remedy is unperformable. Between 2026-11-05 and 2026-11-11 the attention store
// logged 131 Shift+Tab closer lines, each an operator card nobody could act on.
// The daemon already switches a running child's mode through the SDK control
// request on its `/mcp` face; managers bind `/supervisor/mcp`, so this is the same
// capability on the face the people who need it actually hold.
//
// The mode is checked BEFORE any lookup, so a crafted value never reaches the
// child whatever the target turns out to be.
func (s *RosterToolServer) setAgentPermissionMode(
	ctx context.Context,
	args json.RawMessage,
	callerSessionID string,
) (string, error) {
	var body struct {
		AgentID string `json:"agent_id"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if body.AgentID == "" {
		return "", errors.Errorf(ctx, "agent_id is required")
	}
	if !allowedSwitchablePermissionMode(body.Mode) {
		return "", errors.Errorf(
			ctx,
			"permission mode %q is not switchable through this tool — the only modes it sets are "+
				"%s. `bypassPermissions` is unreachable on purpose: it lifts every approval gate on "+
				"another model session, and `acceptEdits` already stops the auto-mode classifier",
			body.Mode,
			strings.Join(switchablePermissionModes, ", "),
		)
	}
	session, err := s.switchableSession(ctx, body.AgentID)
	if err != nil {
		return "", err
	}
	if s.Locks == nil {
		return "", errors.Errorf(ctx, "permission mode change is not wired")
	}
	// TryAcquire, not Acquire: a compaction can hold the lock for minutes, and the
	// REST route fails fast with 409 for exactly that reason. The three faces must
	// not disagree about whether a change queues or refuses.
	release, ok := s.Locks.TryAcquire(session.ID)
	if !ok {
		return "", errors.Errorf(ctx, "session %q is running a sequence", session.ID)
	}
	defer release()
	// Read BEFORE the call: the reply reports what the session held, and after a
	// switch the daemon's own listing already holds the new value.
	previous := session.PermissionMode
	reported, err := s.Supervisor.SetPermissionMode(ctx, session.ID, body.Mode)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "switch permission mode failed")
	}
	recorded := s.recordPermissionModeChange(
		ctx, session.ID, callerSessionID, previous, body.Mode, reported,
	)
	return marshalJSON(ctx, map[string]any{
		"agent_id": session.ID,
		// ⚠️ The child's own answer, never the request echoed back: a child that
		// refused the mode must not read as a success, and a child that answered a
		// different mode must be visible.
		"permission_mode": reported,
		"previous_mode":   previous,
		"requested":       body.Mode,
		"recorded":        recorded,
	})
}

// switchableSession returns the held child a mode switch may target, or the
// refusal naming the route that applies to the target it actually is.
//
// The ownership boundary is the point rather than a limitation: this daemon holds
// a headless child's stdin, which is what makes a mode change reach it. A tab
// worker's mode belongs to its pane, and a cluster worker's is fixed at spawn.
func (s *RosterToolServer) switchableSession(
	ctx context.Context,
	agentID string,
) (*Session, error) {
	if agent := s.a2aAgent(ctx, agentID); agent != nil {
		return nil, errors.Errorf(
			ctx,
			"set_agent_permission_mode acts on a local headless worker only — %s is a cluster "+
				"(A2A) worker, whose permission mode is fixed at spawn; respawn it with "+
				"`shipping: true` to start it in %s",
			agentID,
			ShippingPermissionMode,
		)
	}
	if s.Supervisor == nil {
		return nil, errors.Errorf(ctx, "set_agent_permission_mode is not wired")
	}
	session, err := s.Supervisor.Get(ctx, agentID)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			return nil, errors.Wrapf(ctx, err, "read agent %s", agentID)
		}
		return nil, s.refuseUnheldSwitchTarget(ctx, agentID)
	}
	if session.State == StateExited {
		return nil, errors.Errorf(
			ctx,
			"set_agent_permission_mode acts on a local headless worker only — %s has exited, so "+
				"there is no child to switch; respawn it with `shipping: true`",
			agentID,
		)
	}
	// Defensive: the supervisor only spawns headless children, so an interactive
	// session here would mean an adopted tab. Its mode is its pane's, not ours.
	if session.Mode == SpawnModeInteractive {
		return nil, errors.Errorf(
			ctx,
			"set_agent_permission_mode acts on a local headless worker only — %s is a tab worker, "+
				"whose permission mode belongs to its pane; switch it there with Shift+Tab",
			agentID,
		)
	}
	return session, nil
}

// refuseUnheldSwitchTarget names the route that applies to a target this daemon
// holds no child for.
//
// The ledger is what separates the three cases: an `interactive` row is a tab
// worker, any other row is a worker spawned elsewhere (cluster, or a headless
// child of a previous daemon process), and no row at all means the id names
// nothing here.
func (s *RosterToolServer) refuseUnheldSwitchTarget(
	ctx context.Context,
	agentID string,
) error {
	if s.Ledger != nil {
		rows, err := s.Ledger.Rows(ctx)
		if err != nil {
			log.Printf(
				"WARNING: the ledger could not be read (%v) — refusing %q as an unknown target",
				err,
				agentID,
			)
		}
		for _, row := range rows {
			if row.SessionID != agentID && row.AgentID != agentID {
				continue
			}
			if row.Mode == SpawnModeInteractive {
				return errors.Errorf(
					ctx,
					"set_agent_permission_mode acts on a local headless worker only — %s is a tab "+
						"worker, whose permission mode belongs to its pane; switch it there with "+
						"Shift+Tab",
					agentID,
				)
			}
			return errors.Errorf(
				ctx,
				"set_agent_permission_mode acts on a local headless worker only — %s is a %s "+
					"worker this daemon holds no child for, so there is no stdin to switch; "+
					"respawn it headless with `shipping: true` to start it in %s",
				agentID,
				row.Mode,
				ShippingPermissionMode,
			)
		}
	}
	return errors.Errorf(
		ctx,
		"set_agent_permission_mode acts on a local headless worker only — %s names no worker on "+
			"this daemon",
		agentID,
	)
}

// recordPermissionModeChange appends the audit line and reports whether it landed.
//
// A failed write does not undo the switch: it has already reached the child, and
// a mode nobody recorded is still the mode the worker holds. The reply carries
// `recorded: false` and the daemon log gets one line, so the gap is visible
// rather than implied to be absent.
func (s *RosterToolServer) recordPermissionModeChange(
	ctx context.Context,
	agentID string,
	callerSessionID string,
	from string,
	requested string,
	to string,
) bool {
	if s.PermissionModeLog == nil {
		return false
	}
	if err := s.PermissionModeLog.Write(ctx, PermissionModeChange{
		AgentID:         agentID,
		CallerSessionID: callerSessionID,
		From:            from,
		Requested:       requested,
		To:              to,
	}); err != nil {
		log.Printf(
			"WARNING: permission mode change record for %s could not be written: %v",
			agentID,
			err,
		)
		return false
	}
	return true
}

// spawnAgentArgs is every argument a spawn call may carry.
type spawnAgentArgs struct {
	Target string `json:"target"`
	Task   string `json:"task"`
	Vault  string `json:"vault"`
	// Cwd and Label are the local-tab arguments. Cwd is deliberately separate from
	// Vault: a tab may run in a subdirectory of its vault, and the launcher still
	// comes from the vault.
	Cwd    string `json:"cwd"`
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	// Resume is the full session id of the conversation to continue, or empty
	// for a fresh one. A boolean `"resume": true` fails to decode, so a caller
	// that cannot name the conversation is refused instead of silently handed
	// a new one.
	Resume      string            `json:"resume"`
	Policy      string            `json:"policy"`
	Interactive *bool             `json:"interactive"`
	WindowID    string            `json:"window_id"`
	Role        string            `json:"role"`
	Env         map[string]string `json:"env"`
	Shipping    bool              `json:"shipping"`
	// OperatorNamed is the CALLER's assertion that the operator named this task
	// by hand — the exemption that lets one task open between the soft and hard
	// caps. Never inferred here; absent means an ordinary spawn.
	OperatorNamed bool `json:"operator_named"`
}

// spawnAgent creates a worker on an a2a-worker-controller, in a WezTerm tab, or
// headless in this daemon.
//
// Dispatch is by TARGET, then by MODE. An A2A target (`docker`, `k8s`,
// `k8s:<cluster>`) creates a worker on a controller. An absent or `local` target
// resolves its mode through ResolveSpawnMode — explicit `interactive`, then
// SUPERVISOR_SPAWN_MODE, then `spawn.mode` in the config file, then the
// `interactive` default — and opens a tab under the vault's cc-* launcher or an
// in-process headless worker accordingly.
func (s *RosterToolServer) spawnAgent(
	ctx context.Context,
	args json.RawMessage,
	sessionID string,
) (string, error) {
	var body spawnAgentArgs
	if err := json.Unmarshal(args, &body); err != nil {
		return "", errors.Wrapf(ctx, err, "decode arguments failed")
	}
	if !a2a.IsTarget(ctx, body.Target) {
		return s.spawnLocalTarget(ctx, body, sessionID)
	}
	if s.A2A == nil {
		return "", errors.Errorf(
			ctx,
			"spawn_agent is not wired: no A2A controller configuration was loaded, so no "+
				"worker could be created. Set `a2a.url` and a controller token",
		)
	}
	// The node's `spawnA2AWorker` checks the worker cap and the start rate too: a
	// cluster worker is a different SHAPE of worker, not a different population.
	// The manager cap stays local-only, as it is there.
	if err := s.enforceWorkerCaps(ctx, body, ""); err != nil {
		return "", err
	}
	agent, err := s.A2A.Spawn(ctx, a2a.SpawnArgs{
		Target:      body.Target,
		Task:        body.Task,
		Vault:       body.Vault,
		Prompt:      body.Prompt,
		Resume:      body.Resume != "",
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
	// A tab worker has a pane and no A2A record, so it is resolved first: falling
	// through to A2A would report it as unknown and never type into its tab.
	if handled, response, err := s.sendToTabPane(ctx, body.AgentID, body.Text); handled {
		return response, err
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

// spawnLocalTarget serves the `local` spawn: a WezTerm tab under a vault's cc-*
// launcher, or the in-process headless worker, as ResolveSpawnMode decides.
//
// Any other target is refused by name rather than guessed at: a worker created
// somewhere the caller did not ask for is discovered only by noticing it.
func (s *RosterToolServer) spawnLocalTarget(
	ctx context.Context,
	body spawnAgentArgs,
	sessionID string,
) (string, error) {
	switch body.Target {
	case "", "local":
	default:
		return "", errors.Errorf(
			ctx,
			"spawn_agent with target %q is not available in this daemon: the targets "+
				"are `local` (a WezTerm tab or a headless worker), `docker`, `k8s` and "+
				"`k8s:<cluster>`",
			body.Target,
		)
	}
	// The mode goes through the node's FULL chain (`resolveSpawnMode`): the
	// explicit `interactive` argument, then SUPERVISOR_SPAWN_MODE, then
	// `spawn.mode` in the config file, then the `interactive` default. An earlier
	// port honoured only the argument and the default, so an operator's
	// `spawn.mode: headless` opened every argument-less spawn as a tab.
	resolution, err := s.resolveSpawnMode(ctx, body.Interactive)
	if err != nil {
		return "", err
	}
	if resolution.Mode == SpawnModeHeadless {
		return s.spawnLocalAgent(ctx, body, sessionID, resolution)
	}
	return s.spawnTabAgent(ctx, body, sessionID, resolution)
}

// spawnTabAgent opens a WezTerm tab worker under the vault's cc-* launcher.
func (s *RosterToolServer) spawnTabAgent(
	ctx context.Context,
	body spawnAgentArgs,
	sessionID string,
	resolution SpawnModeResolution,
) (string, error) {
	if s.Spawner == nil {
		return "", errors.Errorf(
			ctx,
			"spawn_agent is not wired: this daemon has no tab spawner, so no local "+
				"worker could be opened",
		)
	}
	// Arguments the tab path cannot honour are REFUSED, never dropped: the tab
	// launches a `cc-*` script, which is never handed `--resume` or a policy, so
	// accepting either would do real work and then discard what was asked for.
	if body.Resume != "" {
		return "", errors.Errorf(
			ctx,
			"`resume` reaches a headless worker only — the tab path launches the cc-* "+
				"launcher, which is not handed the flag, so the call would open a fresh "+
				"conversation while you believed it continued one",
		)
	}
	if body.Policy != "" {
		return "", errors.Errorf(
			ctx,
			"`policy` reaches a headless worker only — a tab worker answers its own "+
				"prompts in its own tab, so the server never sees them and a policy "+
				"handed to one would never be consulted",
		)
	}
	if body.Shipping {
		return "", errors.Errorf(
			ctx,
			"`shipping` needs the server to set the worker's permission mode, which "+
				"this slice does not do for a tab — omit it",
		)
	}
	if err := ValidateTabEnv(ctx, body.Env); err != nil {
		return "", err
	}
	// The schema cannot say "required for a tab, optional for headless", so it says
	// optional and this refusal carries the tab's requirement — clearer than the
	// spawner's generic "invalid spawn options", and it names the headless escape.
	if strings.TrimSpace(body.Prompt) == "" {
		return "", errors.Errorf(
			ctx,
			"a local tab spawn requires `prompt` — the tab launches with it as the "+
				"worker's first turn. Pass `interactive: false` for a headless worker "+
				"that may start idle",
		)
	}
	windowID, err := parseWindowID(ctx, body.WindowID)
	if err != nil {
		return "", err
	}
	if err := s.enforceCaps(ctx, body, SpawnModeInteractive); err != nil {
		return "", err
	}
	result, err := s.Spawner.Spawn(ctx, TabSpawnRequest{
		Prompt:           body.Prompt,
		Label:            body.Label,
		Cwd:              body.Cwd,
		Vault:            body.Vault,
		Role:             body.Role,
		Env:              body.Env,
		WindowID:         windowID,
		SpawnerSessionID: sessionID,
		ModeSource:       resolution.Source,
	})
	if err != nil {
		return "", err
	}
	return marshalJSON(ctx, result)
}

// enforceCaps runs the fleet-wide caps, then reserves a start slot.
//
// It sits after every cheap argument refusal and immediately before the spawn's
// first side effect, on both local paths: a refusal after a tab opened would
// leave an orphan pane the caller never learns about, and a start slot consumed
// by a call that was never going to spawn would charge the next legitimate start
// for an argument typo.
//
// The worker cap applies to every spawn, managers included — the node's order —
// and the manager cap is consulted only for an explicit `role: manager`.
//
// ⚠️ `mode` is the RESOLVED mode, not the argument: `spawn.mode: headless` in the
// config routes an argument-less spawn here as headless, and charging that spawn
// to the interactive sub-cap would leave the headless cap unbindable on exactly
// the path the 2026-11-10 storm grew.
func (s *RosterToolServer) enforceCaps(
	ctx context.Context,
	body spawnAgentArgs,
	mode string,
) error {
	if s.Caps == nil {
		return nil
	}
	// A manager answers to the manager axes, never to a WORKER mode sub-cap: the
	// headless/interactive pair bounds the worker population, and a manager counted
	// against it would be refused by a cap over a population it is not in. The
	// worker total and the session backstop still apply to it.
	workerMode := mode
	if strings.TrimSpace(body.Role) == RoleManager {
		workerMode = ""
	}
	if err := s.Caps.CheckWorker(ctx, workerMode, body.OperatorNamed); err != nil {
		return err
	}
	if strings.TrimSpace(body.Role) == RoleManager {
		// One vault read for both halves: `vaultRoots` shells `vault-cli config list`,
		// and asking it twice for one spawn would pay that subprocess twice.
		roots := s.vaultRoots(ctx)
		if err := s.Caps.CheckManager(
			ctx, ManagerKindForLabel(body.Label, roots, FileExists), roots, body.OperatorNamed,
		); err != nil {
			return err
		}
	}
	// Last, as the node has it: a spawn refused by a cap above must not spend a
	// start slot the next legitimate spawn then waits for.
	return s.Caps.ReserveStart(ctx)
}

// enforceWorkerCaps runs the worker cap and reserves a start slot.
//
// Shared with the A2A path, which the node holds to these two and not to the
// manager cap. The mode is empty there on purpose: a container worker is neither
// a local tab nor a local headless process, so only the worker total and the
// session backstop apply.
func (s *RosterToolServer) enforceWorkerCaps(
	ctx context.Context,
	body spawnAgentArgs,
	mode string,
) error {
	if s.Caps == nil {
		return nil
	}
	if err := s.Caps.CheckWorker(ctx, mode, body.OperatorNamed); err != nil {
		return err
	}
	return s.Caps.ReserveStart(ctx)
}

// vaultRoots returns the configured vault directories, or nil when the list
// cannot be read.
//
// ⚠️ The kind is classified from the SAME roots (`ManagerKindForLabel`), by the
// rule the live count uses, so a manager cannot be counted under one cap and
// opened under another. An unreadable vault list degrades to no roots, which
// classifies the manager `unresolved`: it still answers to the manager cap and
// the session cap, and refusing to spawn over a vault-list failure would be a
// worse trade than skipping one sub-cap.
func (s *RosterToolServer) vaultRoots(ctx context.Context) []string {
	if s.Vaults == nil {
		return nil
	}
	vaults, err := s.Vaults.List(ctx)
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(vaults))
	for _, vault := range vaults {
		if vault.Path != "" {
			roots = append(roots, vault.Path)
		}
	}
	return roots
}

// resolveSpawnMode reads the operator's sources and resolves the spawn's mode.
//
// Read on every call, so a `spawn.mode` edit applies to the next spawn. A nil
// reader means neither operator source is configured.
func (s *RosterToolServer) resolveSpawnMode(
	ctx context.Context,
	interactive *bool,
) (SpawnModeResolution, error) {
	var sources SpawnModeSources
	if s.SpawnMode != nil {
		read, err := s.SpawnMode.Read(ctx)
		if err != nil {
			return SpawnModeResolution{}, err
		}
		sources = read
	}
	return ResolveSpawnMode(ctx, interactive, sources)
}

// noPaneResponse answers a LOCAL worker whose ledger row carries no pane.
//
// Split out of sendToTabPane because the decision is a table, not a branch: a
// headless worker is handed back as an address, an interactive worker whose pane is
// gone is refused. Keeping it here is what lets the caller's loop stay one
// condition deep.
func (s *RosterToolServer) noPaneResponse(
	ctx context.Context,
	agentID string,
	ledgerRow LedgerRow,
) (string, error) {
	if ledgerRow.Mode == "headless" {
		return s.headlessPeerResponse(ctx, agentID, ledgerRow.SessionID)
	}
	// ⚠️ UNCHANGED from before this change, deliberately: an INTERACTIVE worker is a
	// real claude session registered under a name, so `SendMessage` genuinely can
	// reach it — the advice is performable for this mode and unperformable only for
	// headless, which is the branch above.
	return "", errors.Errorf(
		ctx,
		"agent %s is a %s worker with no pane — there is nothing to type into. "+
			"Answer its parked prompts with answer_permission, or reach it mid-task "+
			"with SendMessage",
		agentID,
		ledgerRow.Mode,
	)
}

// headlessPeerResponse resolves a headless worker's registry name and hands it
// back for the caller to send, or returns the refusal naming why it cannot.
//
// ⚠️ THIS IS NOT A SEND, and the response must not say otherwise. This daemon is
// not a Claude session, so it cannot call `SendMessage` — that tool lives in the
// CALLER's harness. What the daemon owns is the join the caller cannot perform: it
// holds the worker's session id, `SendMessage` takes a registry name, and the
// registry is the only place that maps one to the other. So `sent` stays false and
// the name is the whole payload; claiming `sent: true` would repeat the defect the
// change removes — a channel reported as working while delivering nothing.
//
// The three failure paths mirror the Node server's contract exactly (see
// claude-supervisor `server/message-channel.mjs`): more than one name refuses,
// because either would reach the wrong conversation; an unreadable registry is
// retryable and must not read as "the worker is gone"; and no name at all refuses,
// naming the parked-prompt channel that still works.
func (s *RosterToolServer) headlessPeerResponse(
	ctx context.Context,
	agentID string,
	sessionID string,
) (string, error) {
	if s.Registry == nil {
		return "", errors.Errorf(
			ctx,
			"agent %s is a headless worker and this daemon was started without a session registry, "+
				"so its SendMessage name is unknown — answer its parked prompts with answer_permission",
			agentID,
		)
	}
	names, err := s.Registry.NamesForSession(ctx, sessionID)
	if err != nil {
		// Wrapped, not formatted into the message: the cause stays reachable through
		// `errors.Is`/`As`, which a `%v` in the text would destroy.
		return "", errors.Wrapf(
			ctx,
			err,
			"agent %s is a headless worker whose session registry could not be read, so its "+
				"SendMessage name is unknown — retry, and answer its parked prompts with "+
				"answer_permission meanwhile",
			agentID,
		)
	}
	if len(names) > 1 {
		return "", errors.Errorf(
			ctx,
			"agent %s is a headless worker registered under %d names (%s), so a bare name would "+
				"reach the wrong conversation — pick the worker from list_agents, or answer its "+
				"parked prompts with answer_permission",
			agentID,
			len(names),
			strings.Join(names, ", "),
		)
	}
	if len(names) == 0 {
		return "", errors.Errorf(
			ctx,
			"agent %s is a headless worker with no session-registry entry — the session exited, its "+
				"first turn has not reported an id yet, or it runs in-process without registering a "+
				"name — so no SendMessage can address it. Check it with agent_status, or answer its "+
				"parked prompts with answer_permission",
			agentID,
		)
	}
	log.Printf(
		"resolved headless worker %s to registry name %q — the caller delivers",
		agentID,
		names[0],
	)
	return marshalJSON(ctx, map[string]any{
		"agent_id":   agentID,
		"sent":       false,
		"channel":    "peer",
		"name":       names[0],
		"session_id": sessionID,
		"note": "this daemon cannot reach a headless worker's conversation itself — send it with " +
			"SendMessage(to: \"" + names[0] + "\"), which is the registry name this worker answers to",
	})
}

// parseWindowID converts the wire's string window id to the pointer the spawner
// takes, keeping empty and absent distinct from window 0.
func parseWindowID(ctx context.Context, raw string) (*int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "window_id %q is not a number", raw)
	}
	return &parsed, nil
}

// sendToTabPane types a message into a tab worker's pane.
//
// `handled` is false when the id names no tab worker, so the caller falls through
// to the A2A arm. A tab worker is recognised by its LEDGER row, not its roster
// row: the roster deliberately carries no pane handle, and the pane is the only
// thing a message can be typed into.
//
// The id may be the roster's own agent id or the session id the spawn returned —
// both name the same worker, and the spawn response hands the caller the session
// id because that is the only identity a tab worker has.
//
// `sent: true` means the message was typed AND its submit was confirmed in the
// pane's own output. The send's exit code alone would report keystrokes reaching
// the pty, which is true whether or not anything ran.
func (s *RosterToolServer) sendToTabPane(
	ctx context.Context,
	agentID string,
	text string,
) (bool, string, error) {
	if s.Tabs == nil || s.Roster == nil || s.Ledger == nil {
		return false, "", nil
	}
	sessionID := ""
	if rows, err := s.Roster.List(ctx); err != nil {
		// A roster that cannot be read must not read as "not a tab worker": the
		// caller would then be told the agent is unknown, which is a different
		// and false fact. Degrade loudly, as the roster itself does.
		log.Printf(
			"WARNING: the roster could not be read (%v) — resolving agent %q against the ledger only",
			err,
			agentID,
		)
	} else {
		for _, row := range rows {
			if row.AgentID == agentID {
				sessionID = row.SessionID
				break
			}
		}
	}
	ledgerRows, err := s.Ledger.Rows(ctx)
	if err != nil {
		return false, "", nil
	}
	for _, ledgerRow := range ledgerRows {
		if ledgerRow.SessionID != sessionID && ledgerRow.SessionID != agentID {
			continue
		}
		if ledgerRow.PaneID == "" {
			// Only a LOCAL row is ours to answer. `interactive` and `headless` are
			// both this daemon's own spawns, so a missing pane is a real answer for
			// them; `cluster` and `a2a` rows belong to a remote controller and carry
			// no pane either, so claiming them here would answer an A2A worker as
			// unreachable before the A2A arm ever ran.
			if ledgerRow.Mode != "interactive" && ledgerRow.Mode != "headless" {
				continue
			}
			response, err := s.noPaneResponse(ctx, agentID, ledgerRow)
			return true, response, err
		}
		if err := SendWhenReady(ctx, s.Tabs, ledgerRow.PaneID, text, s.currentDateTime()); err != nil {
			return true, "", err
		}
		response, err := marshalJSON(ctx, map[string]any{
			"agent_id":   agentID,
			"sent":       true,
			"pane_id":    ledgerRow.PaneID,
			"session_id": ledgerRow.SessionID,
		})
		return true, response, err
	}
	return false, "", nil
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

// rosterToolDefinitions describes the roster face's tools to an MCP client.
func rosterToolDefinitions() []any {
	definitions := append(spawnToolDefinitions(), parkToolDefinitions()...)
	return append(definitions, modeToolDefinitions()...)
}

// modeToolDefinitions describes the live permission-mode switch.
func modeToolDefinitions() []any {
	return []any{
		toolDef(ToolSetAgentPermissionMode,
			"Switch a RUNNING local headless worker's permission mode. Only `acceptEdits` and "+
				"`auto` are accepted: `acceptEdits` stops the auto-mode classifier from gating edits, "+
				"commits and pushes while `permissions.allow` and every PreToolUse hook still apply, "+
				"and `auto` hands each call back to the classifier. `bypassPermissions` is "+
				"unreachable on purpose. Only a headless worker THIS daemon holds can be switched — "+
				"a tab worker's mode belongs to its pane and a cluster (A2A) worker's is fixed at "+
				"spawn. The reply carries `permission_mode`, the mode the CHILD reports (never the "+
				"request echoed back), plus `previous_mode` and `recorded`.",
			map[string]any{
				"agent_id": stringProp("id of the local headless worker to switch"),
				"mode": map[string]any{
					"type":        "string",
					"enum":        switchablePermissionModes,
					"description": "acceptEdits or auto; every other mode is refused",
				},
			},
			"agent_id", "mode"),
	}
}

// spawnToolDefinitions describes the spawn and steering tools.
// shippingProp is the `shipping` argument's schema.
//
// ⚠️ A function, not a literal inside spawnToolDefinitions: that function sits on the
// repo's funlen budget, and the description is the only part of this change that grew it.
func shippingProp() map[string]any {
	return boolProp("headless only: run the worker under `acceptEdits` so its edits and " +
		"commits do not stop on the auto-mode classifier; `permissions.allow` and every " +
		"hook still apply. A tab or A2A target refuses it")
}

func spawnToolDefinitions() []any {
	return []any{
		toolDef(ToolSpawnAgent,
			"Create one worker and return its agent id. `target` selects the shape: "+
				"`docker` (a container on this Mac), `k8s` (a pod on the default cluster, "+
				"nuke dev unless `a2a.defaultCluster` says otherwise) or `k8s:<cluster>` "+
				"creates a worker on an a2a-worker-controller and returns immediately — the "+
				"first turn is sent asynchronously, so the record carries no result yet, and "+
				"`task` is REQUIRED naming the vault task the worker is opened for. An absent "+
				"or `local` target opens EITHER a WezTerm tab under the vault's cc-* launcher "+
				"OR an in-process headless worker in THIS daemon (where `cwd`, `vault`, `label`, "+
				"`model`, `policy` and `resume` (a full session id) apply and the daemon holds "+
				"its parks and stdin). The mode resolves in this order: an explicit "+
				"`interactive`, then SUPERVISOR_SPAWN_MODE, then `spawn.mode` in "+
				"~/.config/claude-supervisor/config.json, then the `interactive` default; the "+
				"response's `mode_source` names the step that decided. A `local` spawn requires "+
				"`cwd` or `vault` — omitting both is refused, "+
				"never defaulted. A tab accepts `label`, `role`, `window_id` and `env`, and "+
				"REFUSES `resume`, `policy` and `shipping`, which reach a headless worker only. "+
				"`env` may not set a worker's permission or spawn mode.",
			map[string]any{
				"target":    stringProp("local, docker, k8s, or k8s:<cluster>"),
				"task":      stringProp("the vault task the worker is opened for (A2A targets)"),
				"vault":     stringProp("the vault the worker runs in; resolves its launcher"),
				"cwd":       stringProp("a directory inside a configured vault, instead of vault"),
				"label":     stringProp("the tab label; defaults to the vault name"),
				"role":      stringProp("manager, agent or human; defaults to agent"),
				"window_id": stringProp("the WezTerm window to open the tab in"),
				"env":       mapProp("environment exported into the pane before the launcher runs"),
				"prompt":    stringProp("the first turn to send; required for a tab, optional for headless"),
				"model":     stringProp("headless only: model to run, or empty for the default"),
				"resume":    stringProp("headless only: the full session id (UUID) of the conversation to continue"),
				"policy":    stringProp("headless only: path to a per-spawn policy overlay"),
				"shipping":  shippingProp(),
				"interactive": boolProp("per-call override: true opens a tab, false spawns headless in-process. " +
					"OMIT it to use the fleet's configured mode (SUPERVISOR_SPAWN_MODE, then " +
					"spawn.mode, then interactive)"),
				"operator_named": boolProp("whether the OPERATOR named THIS task by hand — the exemption " +
					"that lets one task open while the fleet sits between its soft and hard worker caps " +
					"(spawn.maxWorkerSoft / spawn.maxWorkerHard; the manager pair likewise for" +
					"role=manager). The caller resolves it from the task's own operator-set flag and passes " +
					"it; the server never infers it. Omit for an ordinary spawn, which is refused at the " +
					"soft cap. An operator-named spawn is still refused at the hard cap."),
			}),
		toolDef(ToolSendAgentMessage,
			"Send one further turn to a worker. An A2A worker receives it by "+
				"`contextId` and the turn's terminal state plus assistant text come back. "+
				"A tab worker has it typed into its pane. A HEADLESS worker has no pane, "+
				"so the response carries `channel: \"peer\"` and the registry `name` it "+
				"answers to, with `sent: false` — ⚠️ `sent: false` there is the contract, "+
				"not a failure report: the CALLER must deliver the text itself with "+
				"`SendMessage(to: <name>)`, because this daemon is not a Claude session "+
				"and cannot call it. When no single name can be given (none registered, "+
				"several registered, or the registry unreadable) the call refuses and "+
				"names which.",
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

// parkToolDefinitions describes the permission-park tools.
func parkToolDefinitions() []any {
	return []any{
		toolDef(ToolAnswerPermission,
			"Settle a parked permission request. Writes a `decided_by: manager` row to "+
				"permissions.jsonl and returns the answer that was recorded.",
			map[string]any{
				"agent_id":   stringProp("id of the parked agent"),
				"request_id": stringProp("the parked request id"),
				"behavior":   map[string]any{"type": "string", "enum": []string{"allow", "deny"}},
			},
			"agent_id", "request_id", "behavior"),
		toolDef(ToolAwaitPermission,
			"Wait for a park to be settled. Returns `answered: true` with the behavior and "+
				"answers, or `answered: false` when the window closed with the park still open — "+
				"a quiet window, not an error.",
			map[string]any{
				"agent_id":        stringProp("id of the parked agent"),
				"request_id":      stringProp("the request to wait on; defaults to the agent's current park"),
				"timeout_seconds": map[string]any{"type": "integer", "description": "how long to wait; defaults to 60"},
			},
			"agent_id"),
		toolDef(ToolPendingPermissions,
			"Every park this daemon holds right now: agent id, request id, tool and kind.",
			map[string]any{}),
		toolDef(ToolReadPushConfig,
			"Read a worker's registered A2A push config back FROM THE WORKER — "+
				"`{ configs }` (a list, empty when the worker holds none) or an error. It "+
				"lists rather than gets: the SDK's get is keyed by a config id the "+
				"registration never sends, so a get can never match. This is the read half "+
				"of the push round trip — `push_registration: attempted-ok` proves only that "+
				"the worker ACCEPTED a registration, not that it holds one, and this call is "+
				"the only thing that separates those two. It requires an accepted "+
				"registration to exist for that agent and refuses otherwise, rather than "+
				"reading an unregistered task. Read-only: nothing on the worker changes.",
			map[string]any{
				"agent_id": stringProp("the A2A worker whose push config to read"),
			},
			"agent_id"),
	}
}
