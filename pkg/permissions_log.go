// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// PermissionLogEnv is the environment variable naming the permissions log.
//
// The supervisor's own name, so a record written here lands in the same file the
// node server has been writing since 2026-10-30 and the two are comparable in one
// log — which is the whole point of keeping the record shape identical.
const PermissionLogEnv = "SUPERVISOR_PERMISSION_LOG"

// DefaultPermissionLogPath is where the log lives when the env var is unset.
const DefaultPermissionLogPath = "~/.local/state/claude-supervisor/permissions.jsonl"

// DecidedBy names the actor that settled a park.
//
// The set is the node server's, verbatim: a record written by either process
// must be comparable, and a new value would read as an actor no reader knows.
const (
	DecidedByPolicy    = "policy"
	DecidedByEscalated = "escalated"
	DecidedByStore     = "store"
	DecidedByTimeout   = "timeout"
	DecidedByManager   = "manager"
)

// PermissionRecord is one line of `permissions.jsonl`.
//
// The key set is the node server's exactly. `decision` is a pointer so an
// escalated row can carry an explicit null rather than an empty string — a
// reader that distinguishes "decided allow" from "not decided" would otherwise
// see two absences.
type PermissionRecord struct {
	TS          string  `json:"ts"`
	Agent       string  `json:"agent"`
	Label       string  `json:"label,omitempty"`
	RequestID   string  `json:"request_id"`
	Tool        string  `json:"tool"`
	Key         string  `json:"key"`
	MatchedRule *string `json:"matched_rule"`
	Policy      *string `json:"policy"`
	Via         string  `json:"via"`
	Decision    *string `json:"decision"`
	DecidedBy   string  `json:"decided_by"`
	// Message carries an operator's own words for a board answer, so a released
	// question is readable back rather than recorded as an unreadable deny.
	Message   string `json:"message,omitempty"`
	LatencyMS *int   `json:"latency_ms,omitempty"`
}

// PermissionLog appends decision records.
type PermissionLog interface {
	Write(ctx context.Context, record PermissionRecord) error
}

// NewPermissionLog creates a JSONL appender at path.
//
// An empty path resolves the environment variable, then the default. A log that
// cannot be opened is reported once and then silently dropped for that record:
// a decision that landed must never be withheld because its audit line could
// not be written.
func NewPermissionLog(path string, now libtime.CurrentDateTimeGetter) PermissionLog {
	if path == "" {
		path = os.Getenv(PermissionLogEnv)
	}
	if path == "" {
		path = DefaultPermissionLogPath
	}
	if expanded, err := expandHome(path); err == nil {
		path = expanded
	}
	return &permissionLog{path: path, now: now}
}

type permissionLog struct {
	path  string
	now   libtime.CurrentDateTimeGetter
	mutex sync.Mutex
}

// Write appends one record.
func (l *permissionLog) Write(ctx context.Context, record PermissionRecord) error {
	if l.path == "" {
		return nil
	}
	if record.TS == "" {
		record.TS = l.now.Now().Time().Format("2006-01-02T15:04:05.000Z07:00")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal permission record failed")
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o750); err != nil {
		log.Printf("WARNING: permissions log directory could not be created: %v", err)
		return nil
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("WARNING: permissions log could not be opened: %v", err)
		return nil
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		log.Printf("WARNING: permissions record could not be written: %v", err)
	}
	return nil
}

// expandHome expands a leading `~`.
func expandHome(path string) (string, error) {
	if len(path) == 0 || path[0] != '~' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(context.Background(), err, "resolve home failed")
	}
	if len(path) == 1 {
		return home, nil
	}
	return filepath.Join(home, path[1:]), nil
}
