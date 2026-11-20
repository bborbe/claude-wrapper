// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// answerPathRefusal runs claude-supervisor's answer-path-check.py for the
// session issuing a headless spawn and returns its refusal text, or "" when the
// session has an answer path.
//
// ⚠️ THIS IS THE LIVE SPAWN PATH, and it carried no answer-path guard until this
// function existed. The probe was enforced only by the drill server
// (`server/answer-path.mjs`) and by the open endpoint's request mode
// (`openExecutor.answerPathProbe`). Measured 2026-11-20: a session whose probe
// read LAPSED spawned four headless workers through `spawn_agent`, each one's
// first permission park left to die on the 15-minute deny.
//
// ⚠️ It CALLS the probe; it does not re-derive the verdict. The state word
// (OK / WORKING / IDLE / LAPSED / STALE / UNARMED / INVALID), the registration
// lookup and the reach window are `scripts/answer-path-check.py`'s facts. A
// second implementation here would be the two-counters defect this repo refuses
// everywhere else: the day the probe changes, the two disagree with no error on
// either.
//
// Every branch refuses — an unwired probe, an unconfigured path, a probe that
// cannot run, and any non-zero exit. A guard that failed open would open exactly
// the worker whose every gate dies unanswered, which is the 2026-09-20 stranding
// the probe exists to prevent.
func (s *RosterToolServer) answerPathRefusal(
	ctx context.Context,
	sessionID string,
) string {
	if strings.TrimSpace(sessionID) == "" {
		return "no calling session id, so the answer-path probe has no session to check"
	}
	if s.Scripts == nil {
		return "the answer-path probe is not wired"
	}
	if strings.TrimSpace(s.AnswerPathScript) == "" {
		return "the answer-path probe path is not configured"
	}
	result, err := s.Scripts.RunScript(
		ctx, openPython, s.AnswerPathScript, "--session-id", sessionID,
	)
	if err != nil {
		return err.Error()
	}
	if result.ExitCode == 0 {
		return ""
	}
	// The probe prints its reason on stdout and exits 1. Stderr is the fallback
	// because a script that dies before printing still has to hand back a quotable
	// line, and an empty reason would refuse without naming a condition.
	message := strings.TrimSpace(result.Stdout)
	if message == "" {
		message = strings.TrimSpace(result.Stderr)
	}
	if message == "" {
		message = "answer-path-check.py exited without a reason"
	}
	return message
}

// refuseUnansweredSpawn turns a probe refusal into the error `spawn_agent`
// returns, naming the condition the probe reported and the remedy for an agent
// row.
//
// The remedy is the `agent` one (`docs/role-mode-matrix.md` § Answer-path
// remedy): arm this session's manager loop and re-run the spawn. It is never
// "open interactive" — a headless worker is an agent row, and Rule 2 forbids the
// tab that would be the cheaper-looking remedy.
func refuseUnansweredSpawn(ctx context.Context, refusal string) error {
	return errors.Errorf(
		ctx,
		"spawn_agent refused: this session has no answer path for a headless worker's "+
			"parked prompt — %s. Arm this session's manager loop and re-run the spawn; "+
			"a headless worker's prompts park with the session that spawned it, and no "+
			"other session can release them",
		refusal,
	)
}
