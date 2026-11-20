// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"os/exec"
	"time"

	"github.com/bborbe/errors"
)

// ScriptResult is what one external script run produced.
type ScriptResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ScriptRunner runs one external script as an argv, never through a shell.
//
// A non-zero exit is a RESULT, not an error: the scripts this daemon calls
// refuse by exiting 1 with their reason, and that reason is the message the
// caller must pass through. The error is reserved for "the script could not
// run or did not finish" — an exec failure or the timeout.
type ScriptRunner interface {
	RunScript(ctx context.Context, name string, args ...string) (ScriptResult, error)
}

// defaultScriptTimeout bounds one script run. None of the three scripts the
// open endpoint calls reads more than a few vault pages.
const defaultScriptTimeout = 30 * time.Second

// ExecScriptRunner runs scripts with os/exec under a timeout.
type ExecScriptRunner struct {
	// Timeout defaults to defaultScriptTimeout when zero.
	Timeout time.Duration
}

// RunScript runs the script and reports its output and exit code.
//
// A non-zero exit is returned as a result with its code, never as an error, so
// the caller can pass the script's own refusal text through unchanged.
func (r ExecScriptRunner) RunScript(
	ctx context.Context,
	name string,
	args ...string,
) (ScriptResult, error) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultScriptTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	// #nosec G204,G702 -- the name and script path are daemon flags, and every
	// caller-supplied value (a vault path, a page path, a subject, a session id)
	// is one argv element, never interpolated into a shell. G702's taint analysis
	// traces a session id from the MCP request header down to this argv; argv is
	// the safe sink, and the answer-path probe additionally refuses an id that is
	// not a bare filename before it runs.
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := ScriptResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if runCtx.Err() != nil {
		return result, errors.Wrapf(
			ctx, runCtx.Err(), "%s did not finish within %s", name, timeout,
		)
	}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, errors.Wrapf(ctx, err, "run %s failed", name)
}
