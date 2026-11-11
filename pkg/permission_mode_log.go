// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// PermissionModeLogPath is where a permission-mode switch is recorded.
//
// ⚠️ Deliberately not a flag, an env var or a config key: the path and the
// six-key line shape are a frozen interface for readers once shipped, and a
// per-install override would make two daemons disagree about where a worker's
// mode history lives.
const PermissionModeLogPath = "~/.local/state/claude-supervisor/permission-mode-changes.jsonl"

// PermissionModeChange is one line of `permission-mode-changes.jsonl`.
//
// The key set is frozen: a reader joins a switch to the worker and to the caller
// that asked for it by these six keys, and nothing else is written — never tool
// arguments, never a tool name.
//
// `From` is the mode the daemon reported for the session just before the call
// and may be empty when the child had not emitted `init` yet. `To` is the mode
// the CHILD reported back, never the value that was requested: a child that
// refuses the mode must not leave a line claiming it took.
type PermissionModeChange struct {
	TS              string `json:"ts"`
	AgentID         string `json:"agent_id"`
	CallerSessionID string `json:"caller_session_id"`
	From            string `json:"from"`
	Requested       string `json:"requested"`
	To              string `json:"to"`
}

// PermissionModeLog appends mode-change records.
//
// Unlike the permission log, a failure is RETURNED rather than swallowed: the
// switch has already landed in the child, so the caller has to be able to report
// `recorded: false` instead of implying a line nobody wrote.
type PermissionModeLog interface {
	Write(ctx context.Context, change PermissionModeChange) error
}

// NewPermissionModeLog creates a JSONL appender at path.
//
// An empty path means PermissionModeLogPath. There is no env-var step, unlike the
// permission log: that one has a name in the node server's environment, and this
// file has no such sibling to stay comparable with.
func NewPermissionModeLog(path string, now libtime.CurrentDateTimeGetter) PermissionModeLog {
	if path == "" {
		path = PermissionModeLogPath
	}
	if expanded, err := expandHome(path); err == nil {
		path = expanded
	}
	return &permissionModeLog{path: path, now: now}
}

type permissionModeLog struct {
	path  string
	now   libtime.CurrentDateTimeGetter
	mutex sync.Mutex
}

// Write appends one record, creating the parent directory when missing.
func (l *permissionModeLog) Write(ctx context.Context, change PermissionModeChange) error {
	if l.path == "" {
		return errors.Errorf(ctx, "no permission mode log path is configured")
	}
	if change.TS == "" {
		// Millisecond RFC3339, the same format `permissions.jsonl` writes, so one
		// reader can order both files without a second parser.
		change.TS = l.now.Now().Time().Format("2006-01-02T15:04:05.000Z07:00")
	}
	encoded, err := json.Marshal(change)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal permission mode change failed")
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create permission mode log directory failed")
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrapf(ctx, err, "open permission mode log failed")
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return errors.Wrapf(ctx, err, "write permission mode change failed")
	}
	return nil
}
