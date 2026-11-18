// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// toolLogLoop matches a loop keyword at a command position.
var toolLogLoop = regexp.MustCompile(`(^|[;&|]\s*)(for|while|until)\s`)

// toolLogRecord is one line of the execution log.
//
// The command itself is never stored: it can carry a secret. Only the shape-class
// flags are, which is what a "this class of command no longer prompts" claim is
// written about.
type toolLogRecord struct {
	TS        string   `json:"ts"`
	Epoch     float64  `json:"epoch"`
	SessionID string   `json:"session_id"`
	ToolName  string   `json:"tool_name"`
	ToolUseID string   `json:"tool_use_id"`
	Cwd       string   `json:"cwd"`
	Classes   []string `json:"cls"`
}

// ToolLogGuard replaces log-tool-execution.py.
//
// Stream A of the prompt denominator: the attention store records only the prompts
// that happened, so a command the allow rules absorbed leaves no trace there. This
// records that the tool ran, prompted or not. It never blocks the call — every
// failure path is silent.
type ToolLogGuard struct{}

// NewToolLogGuard returns the execution-log guard.
func NewToolLogGuard() *ToolLogGuard {
	return &ToolLogGuard{}
}

// Name returns the script this guard replaces.
func (g *ToolLogGuard) Name() string { return "log-tool-execution.py" }

// Applies reports whether this is a completed Bash call.
func (g *ToolLogGuard) Applies(input Input) bool {
	if input.HookEventName != EventPostToolUse &&
		input.HookEventName != EventPostToolUseFailure {
		return false
	}
	return input.IsBash()
}

// Evaluate appends one execution record and never blocks.
func (g *ToolLogGuard) Evaluate(input Input) Decision {
	command, _ := input.BashCommand()
	now := time.Now().UTC()
	record := toolLogRecord{
		TS:        now.Format("2006-01-02T15:04:05.000Z"),
		Epoch:     float64(now.UnixNano()) / float64(time.Second),
		SessionID: input.SessionID,
		ToolName:  input.ToolName,
		ToolUseID: input.ToolUseID,
		Cwd:       input.Cwd,
		Classes:   classifyCommand(command),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return Decision{}
	}
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "state", "tool-executions")
	if err := os.MkdirAll(dir, 0o750); err != nil { //#nosec G703 -- the operator's own state dir
		return Decision{}
	}
	file, err := os.OpenFile( //#nosec G304 G703 -- a dated name under the operator's own state dir
		filepath.Join(dir, now.Format("2006-01-02")+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600,
	)
	if err != nil {
		return Decision{}
	}
	defer func() { _ = file.Close() }()
	_, _ = file.Write(append(data, '\n'))
	return Decision{}
}

// classifyCommand returns the shape classes present in a shell command, in the
// order the script emits them.
func classifyCommand(command string) []string {
	found := []string{}
	if strings.TrimSpace(command) == "" {
		return found
	}
	if toolLogLoop.MatchString(command) {
		found = append(found, "loop")
	}
	if strings.Contains(command, "$(") || strings.Contains(command, "`") {
		found = append(found, "subst")
	}
	if strings.ContainsAny(command, "<>") {
		found = append(found, "redir")
	}
	if hasSinglePipe(command) {
		found = append(found, "pipe")
	}
	if strings.Contains(command, "&&") || strings.Contains(command, "||") ||
		strings.Contains(command, ";") {
		found = append(found, "chain")
	}
	return found
}

// hasSinglePipe reports whether the command has a pipe that is not part of `||`.
// RE2 has no lookaround, so this is a scan rather than a regex.
func hasSinglePipe(command string) bool {
	for i := 0; i < len(command); i++ {
		if command[i] != '|' {
			continue
		}
		prevPipe := i > 0 && command[i-1] == '|'
		nextPipe := i+1 < len(command) && command[i+1] == '|'
		if !prevPipe && !nextPipe {
			return true
		}
	}
	return false
}
