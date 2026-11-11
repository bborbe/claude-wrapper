// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/bborbe/errors"
)

// FlaggedRow is one flagged row as `flagged-rows.py --json` reports it.
//
// Only the fields the reopen decision reads are decoded. The predicate emits
// more (window, chip, pane, launcher_source) and those belong to the tab
// spawner, which resolves them itself.
//
// ⚠️ This struct is the Go half of a cross-repo contract: the Python half lives
// in claude-supervisor's `scripts/flagged-rows.py`, which this repo does not
// own and must not modify. `docs/eligibility-predicate.md` is the contract's
// home; a field renamed there and not here decodes to its zero value silently.
type FlaggedRow struct {
	Kind  string `json:"kind"`
	Vault string `json:"vault"`
	Name  string `json:"name"`
	// Role is `agent`, `manager` or `human`. A human row is a Direct row — the
	// role's window is named `Direct` in the predicate's ROLE_WINDOW map — and
	// it must be opened before the fleet's own rows.
	Role string `json:"role"`
	// Mode is the row's own spawn mode (`headless` or `interactive`). It is
	// passed to SpawnCaps verbatim: an empty mode takes no mode sub-cap at all,
	// so defaulting it here would silently disable a cap.
	Mode      string   `json:"mode"`
	SessionID string   `json:"session_id"`
	Liveness  string   `json:"liveness"`
	Branch    string   `json:"branch"`
	BlockedBy []string `json:"blocked_by"`
	// Refused carries the predicate's OWN provenance verdicts — `agent-set-flag`
	// for a task whose `flag_set_by` is outside OPENING_ACTORS, `manager-row` for
	// the branch partition. The tick reads this rather than re-deriving the rule:
	// the predicate is the single home of "who may set a flag", and a second copy
	// here would drift from it.
	Refused []string `json:"refused"`
}

// Key is the row's identity in the log line and in the ledger join.
func (r FlaggedRow) Key() string {
	if r.SessionID != "" {
		return r.SessionID
	}
	return r.Vault + "/" + r.Kind + "/" + r.Name
}

// FlaggedRowSource answers the eligible flagged rows.
//
// An interface rather than a direct exec so the decision rule can be tested
// without a vault, a script or a two-minute scan.
type FlaggedRowSource interface {
	Rows(ctx context.Context) ([]FlaggedRow, error)
}

// CommandRunner runs one command and returns its stdout.
//
// It exists so the exec source's argv and decode can be asserted without
// spawning python: the argv IS the contract with the predicate, and a test that
// ran the real script would assert nothing about it.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecCommandRunner runs commands with os/exec. It is the production runner.
type ExecCommandRunner struct{}

// Run runs the command and returns its stdout.
func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // argv is operator-configured, never user input
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "run %s failed", name)
	}
	return out, nil
}

// ExecFlaggedRowsSource reads rows by running the fleet's own predicate.
//
// First iteration calls the script rather than porting it: the predicate is ~900
// lines of vault parsing with its own tested edge cases. Calling it keeps ONE
// implementation of eligibility, which is what stops this tick and
// `/supervisor:open --flagged` from disagreeing about which rows are eligible.
type ExecFlaggedRowsSource struct {
	// Script is the absolute path to `flagged-rows.py`.
	Script string
	// Python is the interpreter, defaulting to `python3`.
	Python string
	// Runner defaults to ExecCommandRunner.
	Runner CommandRunner
}

// Rows runs the predicate and decodes its rows.
func (s ExecFlaggedRowsSource) Rows(ctx context.Context) ([]FlaggedRow, error) {
	python := s.Python
	if python == "" {
		python = "python3"
	}
	runner := s.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	// ⚠️ The argv is fixed and has no shell: no interpolation, no
	// user-controlled path. The script path comes from the daemon's own flag.
	out, err := runner.Run(ctx, python, s.Script, "--json")
	if err != nil {
		return nil, errors.Wrap(ctx, err, "run flagged-rows")
	}
	var payload struct {
		Rows []FlaggedRow `json:"rows"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, errors.Wrap(ctx, err, "decode flagged rows")
	}
	return payload.Rows, nil
}
