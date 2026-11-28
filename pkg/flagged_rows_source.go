// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

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
	Mode      string `json:"mode"`
	SessionID string `json:"session_id"`
	Liveness  string `json:"liveness"`
	// FlagSetBy is the page's raw `flag_set_by` value, carried so the read
	// endpoint GET /api/1.0/openable can report who set the flag. Empty when the
	// source did not compute it (the exec source, a fixture).
	FlagSetBy string `json:"flag_set_by"`
	// Cwd is the page's `cwd`, carried so the liveness classifier can locate the
	// session transcript at ~/.claude/projects/<slug(cwd)>/<session-id>.jsonl.
	// Empty when the source did not compute it.
	Cwd       string   `json:"cwd"`
	Branch    string   `json:"branch"`
	BlockedBy []string `json:"blocked_by"`
	// Refused carries the predicate's OWN provenance verdicts — `agent-set-flag`
	// for a flag an agent set, `flag-provenance-unrecorded` for a flag nobody
	// attributed, `manager-row` for the branch partition. The tick reads this
	// rather than re-deriving the rule: the predicate is the single home of "who
	// may set a flag", and a second copy here would drift from it.
	Refused []string `json:"refused"`
	// Provenance is the row's provenance class — `operator`, `agent` or
	// `unrecorded` — as the source computed it. It is carried on the row so the
	// tick line can name all three states in one run: a refusal token marks only
	// the refused rows, and an operator-set row is opened with no marker of its
	// own. Empty when the source did not compute it (the exec source, a fixture).
	Provenance Provenance `json:"-"`
	// Stopped is the operator's stand-down marker (`stopped`), written by
	// /supervisor:stop. Populated by VaultAPIFlaggedRowsSource from the task
	// record's frontmatter; the exec source never sets it.
	Stopped bool `json:"-"`
	// StopDate is the instant the stand-down was written (`stop_date`), parsed
	// from the page's frontmatter. nil when the key is absent or unparseable —
	// both mean "no readable stop instant", which the decision treats as
	// fail-closed.
	StopDate *time.Time `json:"-"`
	// FlagSetAt is the raw `flag_set_at` instant, parsed from the page's
	// frontmatter. nil when absent or unparseable. It is the raw flag instant,
	// never an `approved_at` fallback.
	FlagSetAt *time.Time `json:"-"`
	// ProgressHash is the sha256 of the page's `# Progress` body, first 16 chars.
	// It is the stall signal: a revival appends `metrics_sessions` to the
	// frontmatter and leaves `# Progress` alone, so the file's mtime moves every
	// tick while this hash does not. Populated by VaultAPIFlaggedRowsSource from
	// the task page's body; the exec source never sets it.
	ProgressHash string `json:"-"`
	// ProgressTail is the last non-empty line of the page's `# Progress` section —
	// what the row last said it was doing. The stall escalation quotes it so the
	// card names the wait rather than only its hash. Populated by
	// VaultAPIFlaggedRowsSource from the task page's body; the exec source never
	// sets it.
	ProgressTail string `json:"-"`
	// Met is the page's checkbox reading (`n/m`) across the whole body. The stall
	// predicate needs "at least one open box": a row with none is finished, not
	// stalled, and must not be skipped.
	Met string `json:"-"`
	// Phase is the page's `phase` frontmatter. The stall predicate applies only to
	// `execution`, matching sweepgate.IdleStuck.
	Phase string `json:"-"`
	// DeferDate is the page's raw `defer_date` frontmatter. Carried so a writer can
	// refuse to overwrite a date someone else just set — the tick's own `due=now`
	// query excludes a future-dated row, so a row reaching the gate normally has
	// none, and this field is what makes that a checked invariant rather than an
	// assumption about the query.
	DeferDate string `json:"-"`
}

// Key is the row's identity in the log line and in the ledger join.
func (r FlaggedRow) Key() string {
	if r.SessionID != "" {
		return r.SessionID
	}
	return r.Vault + "/" + r.Kind + "/" + r.Name
}

// OperatorNamed reports whether the operator's own flag put this row here.
//
// The predicate refuses every row it cannot attribute to an opening actor, and
// `decision` refuses those rows before any cap check, so a row that reaches the
// cap check carries no provenance refusal at all — it is operator-flagged, or it
// is a legacy flag the operator opened. Operator ruling 2026-11-17: such a row
// counts as operator-named and may open up to the HARD headless limit.
//
// ⚠️ The predicate stays the single home of "who may set a flag" — this method
// reads its verdict and never re-derives OPENING_ACTORS, for the same reason
// `Refused` documents.
func (r FlaggedRow) OperatorNamed() bool {
	return len(r.Refused) == 0
}

// CapMode is the worker mode the row will actually open in, for the cap axes.
//
// It follows the role, not the stored `mode:`: an agent row opens headless
// whatever the record says (role × mode matrix Rule 2), so charging its stored
// `interactive` to the interactive sub-cap refused a row that never uses that
// population. A non-agent row keeps its stored mode, including an empty one —
// the caps never default it (AC8).
func (r FlaggedRow) CapMode() string {
	if strings.TrimSpace(r.Role) == RoleAgent {
		return SpawnModeHeadless
	}
	return strings.TrimSpace(r.Mode)
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
	//
	// `--no-pane` because this tick never reads a pane: `decision` skips every
	// non-ABSENT row as not-absent before it looks at `jump`, and FlaggedRow
	// carries no pane field. Without it the script spawned one interpreter per
	// LIVE row to resolve a pane this process discards, and one of those helpers
	// held a read 11m49s until predicateTimeout killed it (2026-11-16). Measured:
	// a full run drops from 88 s to 19 s.
	//
	// ⚠️ Requires a predicate that knows the flag (bborbe/claude-supervisor#929).
	// An older script rejects it as an unrecognised argument and EVERY tick then
	// fails, so that PR must be deployed before this one.
	out, err := runner.Run(ctx, python, s.Script, "--json", "--no-pane")
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
