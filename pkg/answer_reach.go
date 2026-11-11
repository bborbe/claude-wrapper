// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// AnswerReachLegMCP is the reach leg written by this file.
//
// The other leg — `wrapper` — is written by the manager's own shell recorder
// (`scripts/answer-reach.py` in claude-supervisor), because a Bash `curl` to the
// wrapper's answer route is the command the permission classifier actually gates.
const AnswerReachLegMCP = "mcp"

// answerReachDirEnv is the override, shared verbatim with the claude-supervisor side
// (`server/config.mjs` `reachDir`, `scripts/answer-reach.py` `REACH_DIR`, and
// `scripts/answer-path-check.py`). A reader honouring a different name than the writer is
// how a manager that reached the route reads as having reached nothing and is refused.
const answerReachDirEnv = "SUPERVISOR_ANSWER_REACH_DIR"

// AnswerReach is one record that a session's answering command reached its route.
type AnswerReach struct {
	SessionID string `json:"session_id"`
	Leg       string `json:"leg"`
	TS        string `json:"ts"`
	Outcome   string `json:"outcome"`
}

// answerReachDir resolves the store: the env override, else the same XDG default every
// other writer uses.
func answerReachDir() string {
	if dir := os.Getenv(answerReachDirEnv); dir != "" {
		return dir
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "claude-supervisor", "answer-reach")
}

// RecordAnswerReach writes one reach record for sessionID.
//
// ⚠️ **Called on ENTRY to the answer tool, before its arguments are validated.** The fact
// being recorded is "this call reached the server", and a call rejected for a missing field
// still proves that. Recording only on success would miss exactly the sentinel shape the
// coverage check uses, which names no agent.
//
// ⚠️ **One file per (session, leg), never one file per session.** The two legs are written by
// two different processes — this server and a shell recorder — so a single file is a
// lost-update race in which whichever wrote second erases the other's leg.
//
// ⚠️ **Threat model: a reach is evidence, not proof.** These are plain files under the user's
// own state directory with no signature and no ownership check, and the session id is a
// client-supplied HTTP header (`SessionHeader`). Any process running as the same user can write
// both legs and certify itself. That is the correct bar for what this answers — "did this
// manager's answer command reach the route?" — and deliberately not a security boundary: the
// thing it gates is a spawn decision that same user could already make by hand. Stated here so
// nobody later reads a green probe as proof that a session could deliver.
//
// A failure is returned for the caller to log and then ignored: a state-write error must never
// turn an answer into an error the manager reads as a denied permission.
func RecordAnswerReach(ctx context.Context, sessionID string, outcome string, now libtime.DateTime) error {
	if sessionID == "" {
		// A call that cannot name its caller cannot be keyed, and the spawn gate refuses an
		// unnamed session anyway — fabricating a reach for it would manufacture evidence.
		return nil
	}
	// The id is joined into a path, so it must be a bare filename. The Python siblings refuse
	// the same shapes; a writer that accepted `../x` while the reader refused it would leave a
	// permanently `absent` leg nobody could explain.
	if !isBareAnswerReachID(sessionID) {
		return errors.Errorf(ctx, "session id %q is not a bare filename — refusing to join it into a path", sessionID)
	}
	dir := answerReachDir()
	if dir == "" {
		return errors.Errorf(ctx, "cannot resolve a state directory for answer reaches")
	}
	// 0o750 / 0o600 rather than 0755 / 0644: the repo lints with gosec, and a reach names a
	// session — a directory the user alone can read is the right default for it.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create reach dir %s failed", dir)
	}
	body, err := json.Marshal(AnswerReach{
		SessionID: sessionID,
		Leg:       AnswerReachLegMCP,
		TS:        now.Time().UTC().Format("2006-01-02T15:04:05.000Z"),
		Outcome:   outcome,
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal reach failed")
	}
	path := filepath.Join(dir, sessionID+"."+AnswerReachLegMCP+".json")
	//nolint:gosec // G703: sessionID passed isBareAnswerReachID above — no separator, no traversal, no control characters.
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write reach %s failed", path)
	}
	return nil
}

// isBareAnswerReachID reports whether the id is safe to join into a path — one filename, no
// traversal. Mirrors `isBareSessionId` in claude-supervisor's `server/answer-reach.mjs`.
//
// Control characters are refused as well: the id is also logged on a failed write, and a
// newline in it would let a caller forge a log line. gosec's taint analysis cannot see
// through this function, so the two call sites carry a `//nolint:gosec` naming it.
func isBareAnswerReachID(sessionID string) bool {
	if sessionID == "" || sessionID == "." || sessionID == ".." {
		return false
	}
	for _, r := range sessionID {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '/', '\\', 0:
			return false
		}
	}
	return true
}
