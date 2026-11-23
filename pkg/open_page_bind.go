// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// OpenPageBinder records an opened tab's session on the row's own page, the way a
// headless spawn binds its task. A nil binder writes nothing and reports no bind.
//
// ⚠️ This exists because a manager's page stamp cannot be left to its start
// command. The start command is typed into a freshly spawned pane and may never
// take effect: observed 2026-11-23 20:51:49, where SendWhenReadyWithin returned
// "the Enter did not take effect", `/vault-cli:work-on-goal` was never submitted,
// and the goal page kept no `claude_session_id` while its manager ran. Binding at
// open time removes that dependency.
type OpenPageBinder interface {
	BindOpenedPage(
		ctx context.Context,
		request OpenTabRequest,
		sessionID string,
	) (*OpenBind, error)
}

// vaultCLIPageBinder binds a goal page through `vault-cli goal get/set`, so the
// ownership rule stays in one implementation — vault-cli owns it, and a second
// frontmatter writer here would fork it into two that can drift.
type vaultCLIPageBinder struct {
	runner CommandRunner
}

// NewVaultCLIPageBinder returns a binder that writes through `vault-cli`.
func NewVaultCLIPageBinder(runner CommandRunner) OpenPageBinder {
	return &vaultCLIPageBinder{runner: runner}
}

// BindOpenedPage stamps the goal page's `claude_session_id` when it is empty and
// reports the action, mirroring bindSpawnedTask's ownership rule.
//
// It writes nothing — returning a nil bind — for a task row (the headless spawn
// binds that), for a topic row (a topic manager opens through a different start
// command and is a separate defect), for an empty session id (a create whose
// registration timed out must not stamp an empty string), or when the row names
// no page.
//
// ⚠️ There is no `vault-cli goal append-metrics-session` (the subcommands are
// add approve clear complete defer get lint list remove search set show work-on),
// so unlike the task path this records no metrics row. That asymmetry is
// deliberate: reusing the task subcommand on a goal would write to the wrong
// page.
func (b *vaultCLIPageBinder) BindOpenedPage(
	ctx context.Context,
	request OpenTabRequest,
	sessionID string,
) (*OpenBind, error) {
	if b == nil || b.runner == nil {
		return nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	vault := strings.TrimSpace(request.Vault.Name)
	subject := strings.TrimSpace(request.Subject)
	if sessionID == "" || strings.TrimSpace(request.PagePath) == "" ||
		vault == "" || subject == "" {
		return nil, nil
	}
	if request.Kind != OpenKindGoal {
		return nil, nil
	}
	owner, err := b.goalClaudeSessionID(ctx, vault, subject)
	if err != nil {
		return nil, err
	}
	// ⚠️ Accepted last-write-wins race, the same one vault-cli's work-on path and
	// bindSpawnedTask document: two opens onto the same unowned goal can both read
	// empty and both stamp, and the later write wins. vault-cli offers no
	// compare-and-set, so a fix belongs there rather than in a second
	// implementation here.
	action := OpenBindAppended
	if strings.TrimSpace(owner) == "" {
		if err := b.stampGoal(ctx, vault, subject, sessionID); err != nil {
			return nil, err
		}
		action = OpenBindStamped
	}
	return &OpenBind{Task: subject, SessionID: sessionID, Action: action}, nil
}

// goalClaudeSessionID reads a goal page's recorded session id.
func (b *vaultCLIPageBinder) goalClaudeSessionID(
	ctx context.Context,
	vault string,
	goal string,
) (string, error) {
	out, err := b.run(ctx, vault, "goal", "get", goal, "claude_session_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// stampGoal writes the session id onto a goal page that carries none.
func (b *vaultCLIPageBinder) stampGoal(
	ctx context.Context,
	vault string,
	goal string,
	sessionID string,
) error {
	if _, err := b.run(ctx, vault, "goal", "set", goal, "claude_session_id", sessionID); err != nil {
		return errors.Wrapf(ctx, err, "stamp claude_session_id of goal %q", goal)
	}
	return nil
}

// run executes one `vault-cli --vault <vault> …` as an argv, never a shell.
func (b *vaultCLIPageBinder) run(
	ctx context.Context,
	vault string,
	args ...string,
) ([]byte, error) {
	argv := append([]string{"--vault", vault}, args...)
	out, err := b.runner.Run(ctx, "vault-cli", argv...)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "vault-cli %s", strings.Join(argv, " "))
	}
	return out, nil
}
