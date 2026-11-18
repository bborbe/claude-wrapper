// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

const (
	// DefaultInterval is how often a sweep tick runs.
	//
	// 15 minutes is the bound the fleet's liveness expectation is written in, the
	// same reasoning DefaultFlaggedTickInterval records: a row that goes dark must
	// be back inside one tick, not inside one model round.
	DefaultInterval = 15 * time.Minute

	// DefaultReadTimeout bounds one read of the tracked set.
	//
	// 25 minutes is deliberately larger than the tick interval. The loop is
	// sequential, so an overrun delays the next tick rather than overlapping it,
	// and a slow read is a liveness delay while a killed read is a tick that did
	// nothing at all. The bound exists to abandon a hung read, never a slow one.
	DefaultReadTimeout = 25 * time.Minute
)

// Config is one sweep gate's configuration.
//
// The vault is a required constructor argument and is never defaulted; no vault
// path is hardcoded anywhere in this package.
type Config struct {
	Vault            string
	Subject          string
	StateDir         string
	SettledDir       string
	FeedDir          string
	HoldsPath        string
	PredispatchDir   string
	WorkerManagerDir string
	RegistryDir      string
	HeartbeatDir     string
	ToolMarkerDir    string
	TranscriptDir    string
	StoreURL         string
	Now              libtime.CurrentDateTimeGetter
	Interval         time.Duration
	ReadTimeout      time.Duration
	Logf             func(format string, args ...any)
}

// Validate reports whether the required fields are set.
//
// A missing vault is an error naming the field; it is never defaulted.
func (c Config) Validate(ctx context.Context) error {
	if c.Vault == "" {
		return errors.Errorf(ctx, "config: Vault is required")
	}
	if c.Subject == "" {
		return errors.Errorf(ctx, "config: Subject is required")
	}
	return nil
}

// WithDefaults fills every empty field.
//
// Precedence for each directory or URL is: the explicit Config field, then its
// SWEEP_GATE_* environment variable, then the built-in default. The vault and
// subject are never defaulted.
func (c Config) WithDefaults(ctx context.Context) (Config, error) {
	var err error
	if c.StateDir, err = resolveDir(ctx, c.StateDir, "SWEEP_GATE_STATE_DIR",
		".claude", "state", "sweep-gate"); err != nil {
		return Config{}, err
	}
	if c.SettledDir, err = resolveDir(ctx, c.SettledDir, "SWEEP_GATE_SETTLED_DIR",
		".claude", "state", "sweep-gate-settled"); err != nil {
		return Config{}, err
	}
	if c.FeedDir, err = resolveDir(ctx, c.FeedDir, "SWEEP_GATE_FEED_DIR",
		".claude", "state", "attention"); err != nil {
		return Config{}, err
	}
	if c.HoldsPath, err = resolveDir(ctx, c.HoldsPath, "SWEEP_GATE_HOLDS",
		".claude", "state", "session-holds.json"); err != nil {
		return Config{}, err
	}
	if c.PredispatchDir, err = resolveDir(ctx, c.PredispatchDir, "SWEEP_GATE_PREDISPATCH_DIR",
		".claude", "state", "manager-predispatch"); err != nil {
		return Config{}, err
	}
	if c.WorkerManagerDir, err = resolveDir(ctx, c.WorkerManagerDir,
		"SWEEP_GATE_WORKER_MANAGER_DIR", ".claude", "state", "worker-manager"); err != nil {
		return Config{}, err
	}
	if c.RegistryDir, err = resolveDir(ctx, c.RegistryDir, "",
		".claude", "sessions"); err != nil {
		return Config{}, err
	}
	if c.ToolMarkerDir, err = resolveDir(ctx, c.ToolMarkerDir,
		"SWEEP_GATE_TOOL_MARKER_DIR", ".claude", "state", "attention"); err != nil {
		return Config{}, err
	}
	if c.TranscriptDir, err = resolveDir(ctx, c.TranscriptDir,
		"SWEEP_GATE_TRANSCRIPT_DIR", ".claude", "projects"); err != nil {
		return Config{}, err
	}
	if c.HeartbeatDir, err = resolveHeartbeatDir(ctx, c.HeartbeatDir); err != nil {
		return Config{}, err
	}
	if c.StoreURL = resolveStoreURL(c.StoreURL); c.StoreURL == "" {
		c.StoreURL = "http://localhost:18080"
	}
	if c.Now == nil {
		c.Now = libtime.NewCurrentDateTime()
	}
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = DefaultReadTimeout
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	return c, nil
}

// resolveDir applies the explicit -> env -> home-relative-default precedence.
func resolveDir(ctx context.Context, explicit, envKey string, defaultParts ...string) (
	string, error,
) {
	if explicit != "" {
		return expandHome(ctx, explicit)
	}
	if envKey != "" {
		if value := os.Getenv(envKey); value != "" {
			return expandHome(ctx, value)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home dir failed")
	}
	parts := append([]string{home}, defaultParts...)
	return filepath.Join(parts...), nil
}

// expandHome resolves a leading `~` with os.UserHomeDir, the way
// pkg/subject_state.go does. It never uses os.ExpandEnv on a path.
func expandHome(ctx context.Context, path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home dir failed")
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// resolveHeartbeatDir applies the explicit -> SWEEP_GATE_HEARTBEAT_DIR ->
// SUPERVISOR_HEARTBEAT_DIR -> XDG/default precedence the reference uses.
func resolveHeartbeatDir(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return expandHome(ctx, explicit)
	}
	if value := os.Getenv("SWEEP_GATE_HEARTBEAT_DIR"); value != "" {
		return expandHome(ctx, value)
	}
	if value := os.Getenv("SUPERVISOR_HEARTBEAT_DIR"); value != "" {
		return expandHome(ctx, value)
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.Wrapf(ctx, err, "resolve home dir failed")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-supervisor", "live"), nil
}

// resolveStoreURL applies the explicit -> SWEEP_GATE_STORE_URL ->
// ATTENTION_STORE_URL precedence, returning "" when none is set.
func resolveStoreURL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if value := os.Getenv("SWEEP_GATE_STORE_URL"); value != "" {
		return value
	}
	return os.Getenv("ATTENTION_STORE_URL")
}
