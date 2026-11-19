// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// startHeadless runs every guard of the in-process headless spawn — the
// terminal-task resume guard, the promptless-resume refusal, the caps and
// start rate — then spawns with the launcher's model and MCP configs, sends
// the opening turn (stopping the child if that fails) and files the ledger
// row. spawn_agent and the open endpoint both call it, so neither carries a
// second copy of a guard.
func (s *RosterToolServer) startHeadless(
	ctx context.Context,
	body spawnAgentArgs,
	sessionID string,
	resolution SpawnModeResolution,
) (*Session, string, string, error) {
	if s.Supervisor == nil {
		return nil, "", "", errors.Errorf(ctx, "spawn_agent is not wired")
	}
	// Role-mode matrix Rule 2: a manager is interactive always. Refused first, before
	// any guard that costs a subprocess and before any side effect — a headless
	// manager is unreachable by the pane wake and does no manager work while reading
	// as alive. This covers both a fresh spawn and a resume, since a resume is a
	// headless-capable spawn_agent call.
	//
	// ⚠️ A resume often carries no `role` (the keep-alive path-A call passes only the
	// session id), so the request's role alone would let a manager resume headless.
	// The role is then read from the session's own spawn-ledger row, which recorded
	// what the original spawn ran under.
	if err := RefuseManagerHeadless(ctx, s.roleForHeadlessSpawn(ctx, body), true); err != nil {
		return nil, "", "", err
	}
	cwd, err := s.headlessCwd(ctx, body)
	if err != nil {
		return nil, "", "", err
	}
	// Terminal first: a resume of a finished task must say so, not send the caller
	// away to add a prompt and then refuse them for the task state on the retry.
	// The vault scan this costs is paid only on a refusal path.
	if err := s.refuseTerminalResume(ctx, body, cwd); err != nil {
		return nil, "", "", err
	}
	if err := refusePromptlessResume(ctx, body); err != nil {
		return nil, "", "", err
	}
	if err := s.enforceCaps(ctx, body, SpawnModeHeadless); err != nil {
		return nil, "", "", err
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
		MCPConfigs:     s.headlessMCPConfigs(ctx, body, cwd),
	})
	if err != nil {
		return nil, "", "", errors.Wrapf(ctx, err, "spawn agent failed")
	}
	if body.Prompt != "" {
		if err := s.Supervisor.Send(ctx, session.ID, body.Prompt); err != nil {
			// The child is live by now and holds stdin. A spawn that throws here
			// leaves an idle process behind that looks exactly like a thinking
			// worker, so it is stopped rather than orphaned.
			_ = s.Supervisor.Stop(context.Background(), session.ID)
			return nil, "", "", errors.Wrapf(ctx, err, "deliver opening turn failed")
		}
	}
	s.recordSpawn(ctx, session, sessionID, body, resolution.Source)
	return session, mode, modeSource, nil
}

// roleForHeadlessSpawn resolves the role a headless spawn runs under: the
// request's own role when it names one, else the role the resumed session's
// spawn-ledger row recorded.
//
// An unreadable ledger or no matching row returns the request's role unchanged
// (possibly empty, which RefuseManagerHeadless reads as agent) — the ledger is
// an extra signal for a role-less resume, never a new refusal path of its own.
func (s *RosterToolServer) roleForHeadlessSpawn(
	ctx context.Context,
	body spawnAgentArgs,
) string {
	role := strings.TrimSpace(body.Role)
	if role != "" || strings.TrimSpace(body.Resume) == "" || s.Ledger == nil {
		return role
	}
	rows, err := s.Ledger.Rows(ctx)
	if err != nil {
		return role
	}
	for _, row := range rows {
		if row.SessionID == body.Resume && strings.TrimSpace(row.Role) != "" {
			return strings.TrimSpace(row.Role)
		}
	}
	return role
}

// OpenHeadlessRequest is one agent row the open executor starts or resumes.
type OpenHeadlessRequest struct {
	Vault  string
	Task   string
	Resume string
	// Prompt is the opening turn: `<first_step>\n\n/vault-cli:work-on-task "<task>"`.
	Prompt          string
	Shipping        bool
	OperatorNamed   bool
	CallerSessionID string
}

// OpenHeadlessSpawner starts or resumes an agent row headless.
type OpenHeadlessSpawner interface {
	SpawnOpenHeadless(ctx context.Context, request OpenHeadlessRequest) (*Session, error)
}

// SpawnOpenHeadless is the open endpoint's agent path: spawn_agent's own
// headless spawn with role agent, the task as label, and mode_source open.
func (s *RosterToolServer) SpawnOpenHeadless(
	ctx context.Context,
	request OpenHeadlessRequest,
) (*Session, error) {
	session, _, _, err := s.startHeadless(ctx, spawnAgentArgs{
		Vault:         request.Vault,
		Task:          request.Task,
		Label:         request.Task,
		Role:          RoleAgent,
		Resume:        request.Resume,
		Prompt:        request.Prompt,
		Shipping:      request.Shipping,
		OperatorNamed: request.OperatorNamed,
	}, request.CallerSessionID, SpawnModeResolution{
		Mode:   SpawnModeHeadless,
		Source: OpenModeSource,
	})
	if err != nil {
		return nil, err
	}
	return session, nil
}
