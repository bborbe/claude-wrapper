// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// KeepaliveSender is the slice of Supervisor a resume needs: it types a work
// command into a session this process can reach. The production Supervisor
// satisfies it unchanged.
type KeepaliveSender func(ctx context.Context, sessionID string, text string) error

// NewKeepaliveAct builds the act the lapsed-manager and decision-parked-worker
// procedures perform.
//
// ⚠️ **`restart` and `resume` are different commands, not different words for
// one.** A restart re-drives a live session whose loop lapsed; a resume brings
// back one that is gone. Sending the resume command to a live session would open
// a second one, and sending the restart command to a dead session would type into
// nothing — so the verdict picks the command, and an unknown verdict is an error
// rather than a default.
func NewKeepaliveAct(
	send KeepaliveSender,
	resumeCommand func(sessionID string) string,
	restartCommand func(sessionID string) string,
) func(ctx context.Context, verdict string, sessionID string) error {
	return func(ctx context.Context, verdict string, sessionID string) error {
		if send == nil {
			return errors.New(ctx, "keepalive act has no sender")
		}
		if sessionID == "" {
			return errors.New(ctx, "keepalive act needs a session id")
		}
		var text string
		switch verdict {
		case "resume":
			if resumeCommand == nil {
				return errors.New(ctx, "keepalive act has no resume command")
			}
			text = resumeCommand(sessionID)
		case "restart":
			if restartCommand == nil {
				return errors.New(ctx, "keepalive act has no restart command")
			}
			text = restartCommand(sessionID)
		default:
			// ⚠️ Not a default command. A word this act does not know is a
			// procedure/handler drift, and guessing would type the wrong command
			// into a real session.
			return errors.Errorf(ctx, "keepalive act does not know verdict %q", verdict)
		}
		if err := send(ctx, sessionID, text); err != nil {
			return errors.Wrapf(ctx, err, "send %q to %s failed", text, sessionID)
		}
		return nil
	}
}
