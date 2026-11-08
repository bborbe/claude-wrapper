// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package supervisorconfig resolves the node supervisor's own user-editable
// paths.
//
// A leaf package on purpose: the roster (pkg) and the A2A client (pkg/a2a) both
// read `~/.config/claude-supervisor/config.json`, and pkg/a2a cannot import pkg.
// One resolver here is what keeps a scratch config (`SUPERVISOR_CONFIG`) and an
// XDG home pointed at the same file by every reader, instead of each package
// holding its own hardcoded path and ignoring the override.
package supervisorconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
)

// Env names, exactly as the node supervisor's `config.mjs` reads them.
const (
	// EnvConfig overrides the config file itself.
	EnvConfig = "SUPERVISOR_CONFIG"
	// EnvConfigHome is the XDG base the config lives under.
	EnvConfigHome = "XDG_CONFIG_HOME"
	// EnvStateHome is the XDG base the state lives under.
	EnvStateHome = "XDG_STATE_HOME"
	// DirName is the directory both files sit in.
	DirName = "claude-supervisor"
)

// Path returns the supervisor config file: SUPERVISOR_CONFIG, else
// `$XDG_CONFIG_HOME/claude-supervisor/config.json`, else
// `~/.config/claude-supervisor/config.json` — the node's own precedence.
//
// An empty home resolves to the user's home directory; a nil getenv reads the
// process environment.
func Path(ctx context.Context, home string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if explicit := strings.TrimSpace(getenv(EnvConfig)); explicit != "" {
		return explicit, nil
	}
	if xdg := strings.TrimSpace(getenv(EnvConfigHome)); xdg != "" {
		return filepath.Join(xdg, DirName, "config.json"), nil
	}
	resolved, err := homeDir(ctx, home)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, ".config", DirName, "config.json"), nil
}

// StateDir returns the supervisor state directory:
// `$XDG_STATE_HOME/claude-supervisor`, else `~/.local/state/claude-supervisor`.
func StateDir(ctx context.Context, home string, getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if xdg := strings.TrimSpace(getenv(EnvStateHome)); xdg != "" {
		return filepath.Join(xdg, DirName), nil
	}
	resolved, err := homeDir(ctx, home)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, ".local", "state", DirName), nil
}

// homeDir resolves an empty home to the user's home directory.
func homeDir(ctx context.Context, home string) (string, error) {
	if home != "" {
		return home, nil
	}
	detected, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve home directory failed")
	}
	return detected, nil
}
