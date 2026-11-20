// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// KeepaliveOpener opens one row through the open surface.
//
// It PERFORMS the open — `OpenSurface.Serve` decodes the row, validates it
// against the configured vaults and calls the executor — and returns the open's
// error. It does NOT return a command string for the caller to type.
type KeepaliveOpener func(ctx context.Context, row OpenRow) error

// KeepalivePaneSender types text into a live session's pane.
//
// The pid→pane join (`PanesForPIDs`) and the ready-gated send (`SendWhenReady`)
// live inside the sender the caller wires; the act itself knows nothing of panes.
// A session with no reachable pane is the sender's `no-pane` case, not the act's.
type KeepalivePaneSender func(ctx context.Context, sessionID string, text string) error

// KeepaliveStopper is the no-kill TRIPWIRE: the act must never stop a process, so
// this seam exists only to be proven absent. Production wires a no-op; the fixture
// fakes it and asserts ZERO stop calls after a restart. The inverse proof adds a
// stop call to the restart path and expects the fixture to go red.
type KeepaliveStopper func(ctx context.Context, sessionID string) error

// NewKeepaliveAct builds the act from TWO seams, because `resume` and `restart`
// are different routes, not two words for one command.
//
//   - `resume` targets an ABSENT session → the OPEN surface, which opens it by
//     NAME (kind derived from the branch: topic / goal / task).
//   - `restart` targets a LIVE tab manager → its own PANE, where the restart
//     command is typed and submitted.
//
// ⚠️ **`Supervisor.Send` reaches NEITHER.** It refuses an id it does not hold
// (`unknown session %q`) — true both for an absent session and for a tab
// manager, which is not a wrapper child. That is the defect this split fixes:
// the old act routed both verdicts through `Send`.
//
// An unknown verdict is an error, never a default: a word this act does not know
// is a procedure/handler drift, and guessing would type the wrong command or open
// the wrong row in a real fleet.
func NewKeepaliveAct(
	open KeepaliveOpener,
	restart KeepalivePaneSender,
	stop KeepaliveStopper,
	restartCommand func(sessionID string) string,
) func(ctx context.Context, verdict string, decision LapsedManagerDecision) error {
	// ⚠️ The act NEVER calls stop — a restart types the restart command into the
	// pane and stops nothing. `stop` is a tripwire the fixture asserts is never
	// called; `_ = stop` keeps it wired without invoking it.
	_ = stop
	return func(ctx context.Context, verdict string, decision LapsedManagerDecision) error {
		if decision.SessionID == "" {
			return errors.New(ctx, "keepalive act needs a session id")
		}
		switch verdict {
		case "resume":
			if open == nil {
				return errors.New(ctx, "keepalive act has no opener")
			}
			if decision.Vault == "" || decision.Name == "" {
				return errors.Errorf(
					ctx,
					"resume %s carries no subject (vault %q, name %q) — the open surface opens by name, "+
						"so a decision that drops the subject cannot resume",
					decision.SessionID, decision.Vault, decision.Name,
				)
			}
			return open(ctx, OpenRow{
				Kind:  keepaliveKind(decision.Branch),
				Vault: decision.Vault,
				Name:  decision.Name,
			})
		case "restart":
			if restart == nil {
				return errors.New(ctx, "keepalive act has no pane sender")
			}
			if restartCommand == nil {
				return errors.New(ctx, "keepalive act has no restart command")
			}
			return restart(ctx, decision.SessionID, restartCommand(decision.SessionID))
		default:
			return errors.Errorf(ctx, "keepalive act does not know verdict %q", verdict)
		}
	}
}

// keepaliveKind maps a keepalive branch to the open row's kind.
//
// `topic-keepalive.py:441` derives the opener the same way — `--topic` when the
// branch is `topic`, `--goal` otherwise — so a `--goal` on a topic manager would
// target the wrong tier. Encoding it as the row's Kind is that same rule.
func keepaliveKind(branch string) string {
	switch branch {
	case "topic":
		return OpenKindTopic
	case "goal":
		return OpenKindGoal
	default:
		return OpenKindTask
	}
}
