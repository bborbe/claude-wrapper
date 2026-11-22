// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// skipReason says why a live session was not delivered to. The empty value means
// the session is eligible for delivery.
type skipReason string

const (
	// skipNone means the session may be delivered to.
	skipNone skipReason = ""
	// skipBusy means the session is mid-turn or parked on a gate. It is skipped,
	// never interrupted: a delivery would cut the turn or answer the gate.
	skipBusy skipReason = "busy"
	// skipNoPane means the session is a shell, not a Claude session, so it has no
	// command surface.
	skipNoPane skipReason = "no-pane"
	// skipUnreadable means the session's status could not be read, so nothing is
	// typed into it.
	skipUnreadable skipReason = "status-unreadable"
)

// fleetSession is one live session plus the decision taken about it before any
// delivery.
type fleetSession struct {
	Entry  RegistryEntry
	Skip   skipReason
	Detail string
}

// liveSessions enumerates every live session, scoped to `only` when non-empty,
// and decides per session whether a fleet-wide slash command may be typed into
// it. It is the single enumeration both fleet verbs share, so a session the
// reload skips is skipped by the fleet command for the same reason.
func liveSessions(
	ctx context.Context,
	deps ReloadFleetDeps,
	only []string,
) ([]fleetSession, error) {
	live, err := deps.Registry.Live(ctx)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "list live sessions failed")
	}
	onlySet := toSet(only)
	sessions := make([]fleetSession, 0, len(live))
	for _, entry := range live {
		// A fleet run types into panes, so it can outlast the caller's interest:
		// stop enumerating rather than delivering to a fleet nobody is waiting on.
		if err := ctx.Err(); err != nil {
			return nil, errors.Wrapf(ctx, err, "fleet enumeration cancelled")
		}
		if len(onlySet) > 0 {
			if _, ok := onlySet[entry.SessionID]; !ok {
				continue
			}
		}
		sessions = append(sessions, decideSession(ctx, deps, entry))
	}
	return sessions, nil
}

// decideSession reads one session's status and records why it must be skipped.
// A nil status means the session is unlisted; delivery decides that one.
func decideSession(
	ctx context.Context,
	deps ReloadFleetDeps,
	entry RegistryEntry,
) fleetSession {
	session := fleetSession{Entry: entry}
	status, err := deps.Registry.Status(ctx, entry.SessionID)
	if err != nil {
		session.Skip = skipUnreadable
		session.Detail = "status read failed: " + err.Error()
		return session
	}
	if status == nil {
		return session
	}
	switch *status {
	case "busy", "waiting":
		session.Skip = skipBusy
		session.Detail = "status " + *status
	case "shell":
		session.Skip = skipNoPane
		session.Detail = "shell session"
	}
	return session
}
