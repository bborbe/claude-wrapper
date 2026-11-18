// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// regressionGoal is one goal whose round refreshes its regression sweep.
type regressionGoal struct {
	Goal  string
	Slug  string
	Topic string
	Vault string
}

// regressionGoals is the per-goal table. It is deliberately per-goal rather than a
// plugin round step (operator decision 2026-11-01).
var regressionGoals = []regressionGoal{
	{
		Goal:  "Throughput Is Measured as Goals Delivered per Week at Bounded Operator Attention",
		Slug:  "throughput-is-measured-as-goals-delivered-per-week-at-bounded-operator-attention",
		Topic: "Development Pipeline Throughput",
		Vault: "Documents/Obsidian/private-personal",
	},
}

// regressionSweepScript runs the sweep, then the filer only when the sweep
// succeeded. Both are detached so the round never waits.
const regressionSweepScript = `"$0" "$1/scripts/regression-sweep.py" ` +
	`--topic "$2" --vault "$3" --out "$4" --round-receipt "$5" --history-dir "$6" || exit $?
"$0" "$1/scripts/regression-repair-file.py" ` +
	`--reading "$4" --vault "$3" --topic "$2" --goal "$7"`

// RegressionSweepGuard replaces regression-sweep-on-round.sh.
//
// It refreshes one goal's regression-sweep output on that goal's own round: the
// manager receipt is written every tick, and several receipt calls in one round
// produce one write because the sweep keys on the receipt's round.
type RegressionSweepGuard struct {
	start func(logPath string, args []string) error
}

// NewRegressionSweepGuard returns the regression-sweep guard.
func NewRegressionSweepGuard() *RegressionSweepGuard {
	return &RegressionSweepGuard{start: startDetached}
}

// NewRegressionSweepGuardWithStarter returns the guard over a stubbed starter.
func NewRegressionSweepGuardWithStarter(
	start func(logPath string, args []string) error,
) *RegressionSweepGuard {
	return &RegressionSweepGuard{start: start}
}

// Name returns the script this guard replaces.
func (g *RegressionSweepGuard) Name() string { return "regression-sweep-on-round.sh" }

// Applies reports whether this Bash call runs the manager receipt.
func (g *RegressionSweepGuard) Applies(input Input) bool {
	if input.HookEventName != EventPostToolUse || !input.IsBash() {
		return false
	}
	command, _ := input.BashCommand()
	return strings.Contains(command, "manager-receipt.py")
}

// Evaluate launches the sweep for every matching goal whose receipt exists.
func (g *RegressionSweepGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	receipts := filepath.Join(os.Getenv("HOME"), ".claude", "state", "manager-receipts")
	for _, goal := range regressionGoals {
		if !strings.Contains(command, goal.Goal) {
			continue
		}
		receipt := filepath.Join(receipts, goal.Slug+".json")
		if _, err := os.Stat(receipt); err != nil { //#nosec G703 -- a fixed name under the state dir
			continue
		}
		args := []string{
			"bash", "-c", regressionSweepScript, pythonBinary(),
			filepath.Join(os.Getenv("HOME"), ".claude"),
			goal.Topic,
			filepath.Join(os.Getenv("HOME"), goal.Vault),
			filepath.Join(receipts, goal.Slug+".regression-sweep"),
			receipt,
			filepath.Join(receipts, goal.Slug+".regression-history"),
			goal.Goal,
		}
		_ = g.start(filepath.Join(receipts, goal.Slug+".regression-sweep.log"), args)
	}
	return Decision{}
}

// pythonBinary returns the real interpreter, never the pyenv shim: in a Claude
// session the shim leaks kernel memory on macOS.
func pythonBinary() string {
	const homebrew = "/opt/homebrew/bin/python3"
	if info, err := os.Stat(homebrew); err == nil && !info.IsDir() {
		return homebrew
	}
	return "python3"
}

// startDetached launches the command with its output on logPath and returns at once.
func startDetached(logPath string, args []string) error {
	logFile, err := os.OpenFile( //#nosec G304 G703 -- a fixed name under the operator's state dir
		logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600,
	)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	if len(args) < 3 {
		return nil
	}
	command := exec.Command(args[0], args[1:]...) //#nosec G204 G702 -- fixed interpreter and script
	command.Stdout = logFile
	command.Stderr = logFile
	return command.Start()
}
