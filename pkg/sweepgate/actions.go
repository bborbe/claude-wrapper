// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
)

// supervisorScriptsRel is the plugin clone's scripts directory, relative to the
// home directory. The clone is NOT installed in the YOLO container, so every
// test that would spawn one of these scripts injects a fake ExitRunner instead.
const supervisorScriptsRel = ".claude/plugins/marketplaces/claude-supervisor/scripts"

// ExitRunner runs one command and reports its stdout and its exit code.
//
// The exit code is part of these scripts' contract, so it is a return value, not
// folded into the error: `close-line.py` exits 1 to decline, which is normal.
type ExitRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout []byte, exitCode int, err error)
}

// ExecRunner is the production runner.
type ExecRunner struct{}

// Run runs the command. A non-zero exit is NOT an error; only a spawn failure is.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	var stdout bytes.Buffer
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // argv is vault-authored data, never a shell string
	command.Stdout = &stdout
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		return stdout.Bytes(), exitErr.ExitCode(), nil
	}
	return stdout.Bytes(), -1, err
}

// scriptsDir resolves the supervisor scripts directory once per call.
func scriptsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, supervisorScriptsRel)
}

// pythonBin resolves the interpreter both plugin scripts are run through.
//
// ⚠️ Two failure modes meet here, and both are fatal to a whole tick.
//
// First, the scripts are run as `python3 <script>` and never exec'd directly:
// `close-line.py` is tracked **100644** in the supervisor repo, so a direct exec
// fails with EACCES. Second, the interpreter must not be a bare `python3` looked
// up in PATH: the daemon runs under launchd, whose PATH is not the shell's —
// `which python3` here is a pyenv shim launchd does not have.
//
// Either failure surfaces as a spawn error that propagates CloseLine →
// ActionLines → Frame → Tick, so the tick returns ExitFailed before writing its
// tick file or its state, and every subject carrying a done row with a live
// owner writes nothing, every tick. The reference has neither failure mode
// because `sys.executable` is absolute by construction (`sweep-gate.py:2885`,
// `:2918`); `reap-closer.py` happens to be 755, so a direct exec of that one
// works only by accident.
//
// Resolved once, preferring the system interpreter over any PATH entry.
var pythonBin = sync.OnceValue(func() string {
	for _, candidate := range []string{
		"/usr/bin/python3",
		"/opt/homebrew/bin/python3",
		"/usr/local/bin/python3",
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if resolved, err := exec.LookPath("python3"); err == nil {
		return resolved
	}
	return "python3"
})

// CloserOf returns the session's newest-closer read in close-line.py's
// vocabulary: closed / open / unreadable.
//
// ⚠️ A failed read maps to `unreadable`, NEVER to `closed`: only CLOSED may
// withhold a reap, so a broken read must not suppress a notice.
func CloserOf(ctx context.Context, runner ExitRunner, cfg Config, sessionID string) string {
	_, exitCode, err := runner.Run(ctx,
		pythonBin(), filepath.Join(scriptsDir(), "reap-closer.py"), sessionID)
	if err != nil {
		return "unreadable"
	}
	switch exitCode {
	case 0:
		return "closed"
	case 1:
		return "open"
	default:
		return "unreadable"
	}
}

// CloseLine renders the stamped close line through its one producer.
//
// Exit 0 returns the trimmed stdout; exit 1 returns "" with a nil error (the
// producer declining is a normal outcome); any other exit or a spawn failure is
// an error. The line is never hand-assembled here.
func CloseLine(
	ctx context.Context,
	runner ExitRunner,
	cfg Config,
	task, session, liveness, closer string,
) (string, error) {
	stdout, exitCode, err := runner.Run(ctx,
		pythonBin(), filepath.Join(scriptsDir(), "close-line.py"),
		"--task", task,
		"--session", session,
		"--liveness", liveness,
		"--closer", closer,
	)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "close-line render failed")
	}
	switch exitCode {
	case 0:
		return strings.TrimRight(string(stdout), "\n"), nil
	case 1:
		return "", nil
	default:
		return "", errors.Errorf(ctx, "close-line render failed: exit %d: %s",
			exitCode, strings.TrimSpace(string(stdout)))
	}
}

// actionOrder is the concatenation order of the emitted lines.
var actionOrder = []string{"ready", "yours", "done", "upstream", "unregistered", "hold", "backlog"}

// actionCollector gathers the action lines keyed by bucket.
type actionCollector struct {
	ctx    context.Context
	cfg    Config
	runner ExitRunner
	now    time.Time
	holds  map[string]Hold
	lines  map[string][]string
	err    error
}

// ActionLines is the runbook's non-empty action lines, printed under the box.
//
// Only the lines a model-free read can honestly produce are emitted. A held
// session emits nothing at all; a deferred task emits nothing; and the
// `approval`, `external` and `declared` keys emit nothing. Each silence is an
// explicit branch, not a fall-through.
//
// ⚠️ Every line is built with a literal format and the name as an argument. A
// vault-authored title may carry `%s` or `%!`, so it must never be the format
// string, and ExitRunner's argv slice keeps it out of a shell.
func ActionLines(ctx context.Context, cfg Config, prepared Prepared) ([]string, error) {
	collector := &actionCollector{
		ctx:    ctx,
		cfg:    cfg,
		runner: ExecRunner{},
		now:    cfg.Now.Now().Time(),
		holds:  prepared.Holds,
		lines:  map[string][]string{},
	}
	for _, task := range prepared.Tasks {
		collector.add(task)
	}
	if collector.err != nil {
		return nil, collector.err
	}
	out := make([]string, 0)
	for _, key := range actionOrder {
		out = append(out, collector.lines[key]...)
	}
	return out, nil
}

// add collects one row's line, applying the hold and defer silences first.
func (c *actionCollector) add(task Task) {
	if c.err != nil {
		return
	}
	// A held session emits ZERO action lines. Checked before deferred: a hold
	// says "not by you", a defer says "not yet".
	if _, held := holdEntry(c.holds, task.Session); held {
		return
	}
	// A deferred task is deliberately parked until its date: no action line.
	if IsDeferred(c.now, task) {
		return
	}
	_, key := Bucket(c.now, c.holds, task)
	line, emit, err := c.lineFor(key, task)
	if err != nil {
		c.err = err
		return
	}
	if emit {
		c.lines[key] = append(c.lines[key], line)
	}
}

// lineFor renders one bucket's line, or reports that the bucket is silent.
func (c *actionCollector) lineFor(key string, task Task) (string, bool, error) {
	switch key {
	case "upstream":
		blocker := "?"
		if len(task.BlockedByUnmet) > 0 {
			blocker = task.BlockedByUnmet[0]
		}
		return fmt.Sprintf(
			"⏸️ BLOCKED UPSTREAM: %s — blocked_by %s unshipped — no action until it lands",
			task.Name, blocker), true, nil
	case "unregistered":
		return fmt.Sprintf(
			"⌛ PARKED-ON-UNREGISTERED-GATE: %s — no feed row and the file has not "+
				"moved for ≥2 intervals — SendMessage a check-in: "+
				`"booted? first step? or parked on a gate?"`, task.Name), true, nil
	case "ready":
		return fmt.Sprintf(
			`🚀 READY TO START: %s — start a session: /supervisor:open %q`,
			task.Name, task.Name), true, nil
	case "yours":
		return fmt.Sprintf("👤 YOURS: %s — not a spawn target", task.Name), true, nil
	case "done":
		if task.Liveness != LivenessLive && task.Liveness != LivenessParked {
			return "", false, nil
		}
		line, err := CloseLine(c.ctx, c.runner, c.cfg, task.Name, task.Session,
			task.Liveness, CloserOf(c.ctx, c.runner, c.cfg, task.Session))
		if err != nil {
			return "", false, err
		}
		return line, line != "", nil
	case "hold":
		return fmt.Sprintf("⏸️ HOLD: %s — parked", task.Name), true, nil
	case "backlog":
		return fmt.Sprintf("◦ BACKLOG: %s — someday, no action", task.Name), true, nil
	case "approval", "external", "declared":
		// NO action line: an unapproved row is the operator's inbox, a machine
		// is producing an external change, and a declared wait is recorded.
		// Explicit branch so the silence is deliberate, never a fall-through.
		return "", false, nil
	default:
		return "", false, nil
	}
}
