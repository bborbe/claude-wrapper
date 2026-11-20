// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// NewKeepaliveScriptRunner builds a KeepaliveScriptRunner that execs the named
// script (resolved under SupervisorScriptsDir by the caller) with PythonBin.
//
// The adapters (NewKeepaliveClaim, NewKeepaliveHold, the record-resume pair) all
// take a KeepaliveScriptRunner; this is the one exported constructor main.go uses
// to build them, since the underlying `runScript` is unexported.
func NewKeepaliveScriptRunner(scriptPath string) KeepaliveScriptRunner {
	return func(ctx context.Context, args ...string) (string, int, error) {
		allArgs := append([]string{expandedDir(scriptPath)}, args...)
		return runScript(ctx, PythonBin, allArgs...)
	}
}

// NewKeepaliveFolderFor builds the lister's folder resolver over
// `resolve-folders.py --vault <root path> --require topics_dir|tasks_dir`.
//
// The script prints one `TOPICS|GOALS|TASKS|NAME` line (pipe-separated, absolute
// paths, empty field for an unrequired key); this reads the field matching
// `require`. An empty field or a non-zero exit is an error the caller turns into a
// skipped vault.
func NewKeepaliveFolderFor(run KeepaliveScriptRunner, require string) KeepaliveFolderFor {
	index := folderFieldIndex(require)
	return func(ctx context.Context, vault Vault) (string, error) {
		out, code, err := run(ctx, "--vault", vault.Path, "--require", require)
		if err != nil {
			return "", err
		}
		if code != 0 {
			// ⚠️ `UNKNOWN vault '<name>' does not configure <key>` (exit 1) is an
			// ABSENT folder, not a failure: the vault simply has no topics/tasks
			// dir. Measured 2026-11-20 — the first live ticks logged
			// `result=error` for NINE such vaults (private-boss, private-family,
			// private-agent, private-gaming, private-starcitizen, private-trading,
			// assistant-data, assistant-personal, assistant-brogrammers), which
			// reads as a broken tick when it is a real answer. Every OTHER
			// non-zero exit stays an error.
			if isUnconfiguredFolder(out) {
				return "", nil
			}
			return "", errors.Errorf(ctx, "resolve-folders for %s exited %d", vault.Name, code)
		}
		line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
		fields := strings.Split(line, "|")
		if index < 0 || index >= len(fields) {
			return "", errors.Errorf(ctx, "resolve-folders for %s printed %d fields, want index %d", vault.Name, len(fields), index)
		}
		return strings.TrimSpace(fields[index]), nil
	}
}

// isUnconfiguredFolder reports whether resolve-folders' output is its own
// `UNKNOWN … does not configure …` refusal rather than a real failure.
func isUnconfiguredFolder(output string) bool {
	line := strings.SplitN(strings.TrimSpace(output), "\n", 2)[0]
	return strings.HasPrefix(line, "UNKNOWN ") && strings.Contains(line, "does not configure")
}

// folderFieldIndex maps a resolve-folders `--require` key to its output field.
func folderFieldIndex(require string) int {
	switch require {
	case "topics_dir":
		return 0
	case "goals_dir":
		return 1
	case "tasks_dir":
		return 2
	default:
		return -1
	}
}
