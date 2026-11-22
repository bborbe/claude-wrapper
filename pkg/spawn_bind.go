// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"strings"

	"github.com/bborbe/errors"
)

// bindSpawnedTask binds a headless-spawned session to the task the spawn request
// named, honouring the ownership rule vault-cli already implements in
// pkg/ops/workon.go persistSessionAndMetrics: stamp `claude_session_id` ONLY when
// it is empty, and ALWAYS append the incoming session to `metrics_sessions`.
//
// ⚠️ The ownership branch is the point of this method. An unconditional write of
// `claude_session_id` would bind the new session by stealing a task another live
// session already owns: that passes "the field names the spawned id" and fails the
// rule that the original owner is left intact. So the current id is read first:
//
//   - empty → set it (`vault-cli task set … claude_session_id …`), action "stamped"
//   - held  → leave it intact, record this session in metrics_sessions only,
//     action "appended"
//
// Both the read and the two writes shell out to `vault-cli` rather than writing
// task frontmatter directly: vault-cli owns the ownership rule, and a second
// writer would fork it into two implementations that can drift. The metrics append
// runs in BOTH cases — that is the same "always append" the work-on path performs,
// and the spec accepts the duplicate-id append it can produce.
//
// An empty task returns ("", nil) and does nothing, so a spawn that names no task
// writes nothing and reports no `bind` key. A bind failure is returned as an
// error; the caller reports it in the spawn reply without failing the spawn,
// because by the time this runs the child is already live.
func (s *RosterToolServer) bindSpawnedTask(
	ctx context.Context,
	body spawnAgentArgs,
	sessionID string,
) (string, error) {
	task := strings.TrimSpace(body.Task)
	if task == "" {
		return "", nil
	}
	vault := strings.TrimSpace(body.Vault)
	if vault == "" {
		return "", errors.Errorf(
			ctx,
			"cannot bind task %q to session %s: the spawn named no vault, so there is no "+
				"vault-cli to bind through — a task-bearing headless spawn must name its vault",
			task,
			sessionID,
		)
	}
	if s.Runner == nil {
		return "", errors.Errorf(
			ctx,
			"cannot bind task %q to session %s: no command runner is wired",
			task,
			sessionID,
		)
	}
	owner, err := s.taskClaudeSessionID(ctx, vault, task)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "read claude_session_id of task %q", task)
	}
	// ⚠️ Accepted last-write-wins race, the same one vault-cli's own work-on path
	// documents (pkg/ops/workon.go persistSessionAndMetrics): two spawns onto the
	// same unowned task can both read empty and both stamp, and the later write
	// wins. vault-cli offers no compare-and-set, so a fix belongs there, not in a
	// second implementation here — mirroring its rule is the whole point.
	action := OpenBindAppended
	if owner == "" {
		if _, err := s.runVaultCLI(
			ctx, vault, "task", "set", task, "claude_session_id", sessionID,
		); err != nil {
			return "", errors.Wrapf(ctx, err, "stamp claude_session_id of task %q", task)
		}
		action = OpenBindStamped
	}
	if _, err := s.runVaultCLI(
		ctx, vault, "task", "append-metrics-session", task, sessionID,
	); err != nil {
		return "", errors.Wrapf(ctx, err, "append metrics session to task %q", task)
	}
	return action, nil
}

// bindForReply binds the spawned session to the task the spawn named and returns
// the reply's `bind` object, or nil when the spawn named no task (so the reply
// carries no `bind` key at all and nothing is written).
//
// A bind FAILURE is logged and reported as action "failed" rather than returned:
// the child is already live by the time this runs, so a bind error must not read
// to the caller as a failed spawn.
func (s *RosterToolServer) bindForReply(
	ctx context.Context,
	body spawnAgentArgs,
	sessionID string,
) map[string]any {
	if strings.TrimSpace(body.Task) == "" {
		return nil
	}
	action, err := s.bindSpawnedTask(ctx, body, sessionID)
	if err != nil {
		log.Printf(
			"WARNING: bind task %q to spawned session %s failed: %v",
			body.Task,
			sessionID,
			err,
		)
		return map[string]any{
			"action":     OpenBindFailed,
			"task":       body.Task,
			"session_id": sessionID,
			"error":      err.Error(),
		}
	}
	return map[string]any{
		"action":     action,
		"task":       body.Task,
		"session_id": sessionID,
	}
}

// taskClaudeSessionID reads the task's current `claude_session_id`, empty when the
// field is unset. It shells `vault-cli task get <task> claude_session_id`, which
// prints the bare value (or an empty line) and needs no parsing beyond a trim.
func (s *RosterToolServer) taskClaudeSessionID(
	ctx context.Context,
	vault string,
	task string,
) (string, error) {
	out, err := s.runVaultCLI(ctx, vault, "task", "get", task, "claude_session_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runVaultCLI runs one `vault-cli --vault <vault> …` as an argv, never a shell.
func (s *RosterToolServer) runVaultCLI(
	ctx context.Context,
	vault string,
	args ...string,
) ([]byte, error) {
	argv := append([]string{"--vault", vault}, args...)
	out, err := s.Runner.Run(ctx, "vault-cli", argv...)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "vault-cli %s", strings.Join(argv, " "))
	}
	return out, nil
}
