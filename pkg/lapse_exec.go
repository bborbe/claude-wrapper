// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"

	"github.com/bborbe/errors"
)

// This file binds the two jobs whose predicate stays in Python to the scripts
// that already implement it.
//
// ⚠️ **Exec'd, not re-derived.** `manager-liveness.py` carries the liveness
// rule and `fleet-keeper.py` carries the 18/38/51-minute settle measurement the
// operator's 2026-11-01 ruling was made against. Porting either to Go now would
// put two definitions of one judgement in the fleet, and they would disagree —
// the same reason the flagged tick execs `flagged-rows.py`.
const (
	// PythonBin is the interpreter the shell ticks already used.
	PythonBin = "/opt/homebrew/bin/python3"
	// SupervisorScriptsDir is the marketplace clone holding the two checkers.
	SupervisorScriptsDir = "~/.claude/plugins/marketplaces/claude-supervisor/scripts"
	// ManagerLivenessCheckScript is the liveness predicate.
	ManagerLivenessCheckScript = SupervisorScriptsDir + "/manager-liveness.py"
	// FleetKeeperCheckScript is the fleet-round predicate and card poster.
	FleetKeeperCheckScript = SupervisorScriptsDir + "/fleet-keeper.py"
	// NotificationPublishScript is the vault's authenticated write path.
	NotificationPublishScript = "~/Documents/Obsidian/private-personal/.claude/scripts/trading-api-write.sh"
	// NotificationPublishEndpoint is the escalation route the shell tick used.
	NotificationPublishEndpoint = "/api/1.0/command/core-notification-v1/notification-publish?sync=true"
)

// ExecLapseCheck runs the liveness checker.
func ExecLapseCheck(script string) LapseCheck {
	return func(ctx context.Context) (string, int, error) {
		return runScript(ctx, PythonBin, expandedDir(script), "--check")
	}
}

// ExecLapseDelivered marks one slug delivered, which is the only thing that
// stops the checker re-emitting it.
func ExecLapseDelivered(script string) LapseDelivered {
	return func(ctx context.Context, slug string) error {
		_, code, err := runScript(ctx, PythonBin, expandedDir(script), "--delivered", slug)
		if err != nil {
			return err
		}
		if code != 0 {
			return errors.Errorf(ctx, "delivered %s exited %d", slug, code)
		}
		return nil
	}
}

// ExecLapsePublisher publishes one escalation through the vault's write script.
//
// ⚠️ The script is the credential holder; this process never sees the token. A
// non-zero exit is a FAILED send and the lapse stays owed.
func ExecLapsePublisher(script string, endpoint string) LapsePublisher {
	return func(ctx context.Context, message string) error {
		body, err := json.Marshal(map[string]string{
			"type":    "agent-escalation",
			"message": message,
		})
		if err != nil {
			return errors.Wrap(ctx, err, "marshal escalation failed")
		}
		_, code, runErr := runScript(ctx, expandedDir(script), "prod", endpoint, string(body))
		if runErr != nil {
			return runErr
		}
		if code != 0 {
			return errors.Errorf(ctx, "publish exited %d", code)
		}
		return nil
	}
}

// ExecFleetCheck runs the fleet keeper, which posts its own card.
func ExecFleetCheck(script string) FleetCheck {
	return func(ctx context.Context) (string, int, error) {
		return runScript(ctx, PythonBin, expandedDir(script))
	}
}

// runScript runs one command and returns its combined output and exit code.
//
// A non-zero exit is returned as a code, not an error: both checkers use the
// code as their verdict (10 = owed), and folding it into an error would erase
// the distinction between "a lapse is owed" and "the checker could not run".
func runScript(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.String(), exitErr.ExitCode(), nil
	}
	return out.String(), -1, errors.Wrapf(ctx, err, "run %s failed", name)
}
