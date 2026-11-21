// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	libtime "github.com/bborbe/time"
)

// wakeTimeout bounds one wake invocation. A pane wake types one prompt and
// returns; a hung wake must not stall the tick, so it is abandoned at this bound.
const wakeTimeout = 60 * time.Second

// Runner ticks every configured subject on an interval until the context ends.
type Runner interface {
	Run(ctx context.Context) error
}

// TickRunner is the periodic tick. Its collaborators are exported fields, the
// same shape as pkg.FlaggedTick, so a test can drive it without a vault.
//
// Configs carries one Config per subject, each with Subject set.
type TickRunner struct {
	Configs  []Config
	Interval time.Duration
	Logf     func(format string, args ...any)
}

// Run ticks immediately, then waits one interval between passes.
//
// A daemon that restarts after a deploy must not wait a full interval to
// re-render, so the first pass runs before the first wait. The loop is
// sequential: a pass that overruns the interval delays the next pass rather than
// starting a second one concurrently.
func (r *TickRunner) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.tickOnce(ctx)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// tickOnce runs one pass over every configured subject.
func (r *TickRunner) tickOnce(ctx context.Context) {
	for _, cfg := range r.Configs {
		if ctx.Err() != nil {
			return
		}
		r.tickSubject(ctx, cfg)
	}
}

// tickSubject ticks one subject and never stops the pass.
//
// stdout is discarded: the daemon has no Monitor, so the change signal is the
// log line plus the tick file's mtime. A subject that fails is named through the
// stderr line's prefix and the next subject still runs.
func (r *TickRunner) tickSubject(ctx context.Context, cfg Config) {
	logf := r.logfFor(cfg)
	stderr := newLogWriter(logf, "sweep-gate "+cfg.Subject+": ")
	reason := "digest differs"
	if statePath, err := StatePath(ctx, cfg, cfg.Subject); err == nil {
		if _, failReason, _ := LoadStored(ctx, statePath); failReason != "" {
			reason = failReason
		}
	}
	if exit := Tick(ctx, cfg, io.Discard, stderr, CardBuilder(cfg)); exit == ExitChange {
		logf("sweep-gate %s: VERDICT change (%s)", cfg.Subject, reason)
		r.wake(ctx, cfg, logf)
	}
}

// wake runs the configured wake command for a verdict change.
//
// It is the port of the vault script's stdout CHANGE line: the vault sweep-gate.py
// woke a manager by printing a line a Monitor read; the Go tick has no Monitor, so
// on ExitChange it invokes manager-wake.py directly, which resolves the subject's
// manager pane and types the wake prompt. A no-op when no command is configured.
func (r *TickRunner) wake(ctx context.Context, cfg Config, logf func(format string, args ...any)) {
	if len(cfg.WakeCmd) == 0 {
		return
	}
	wakeCtx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	args := append(append([]string{}, cfg.WakeCmd[1:]...),
		"--vault", cfg.Vault, "--subject", cfg.Subject)
	cmd := exec.CommandContext(wakeCtx, cfg.WakeCmd[0], args...) //nolint:gosec // argv is the operator-set --sweep-gate-wake-cmd flag plus the config's Vault/Subject, never user input
	out, err := cmd.CombinedOutput()
	if err != nil {
		logf("sweep-gate %s: WAKE failed: %v (output: %s)",
			cfg.Subject, err, strings.TrimSpace(string(out)))
		return
	}
	logf("sweep-gate %s: WAKE ok (%s)", cfg.Subject, strings.TrimSpace(string(out)))
}

// logfFor prefers the per-subject logger and falls back to the runner's.
func (r *TickRunner) logfFor(cfg Config) func(format string, args ...any) {
	if cfg.Logf != nil {
		return cfg.Logf
	}
	if r.Logf != nil {
		return r.Logf
	}
	return log.Printf
}

// NewTickRunner expands cfg.Subjects into one Config per subject.
func NewTickRunner(cfg Config) (Runner, error) {
	configs := make([]Config, 0, len(cfg.Subjects))
	for _, subject := range cfg.Subjects {
		child := cfg
		child.Subject = subject
		child.Subjects = nil
		if child.Now == nil {
			child.Now = libtime.NewCurrentDateTime()
		}
		if child.ReadTimeout <= 0 {
			child.ReadTimeout = DefaultReadTimeout
		}
		configs = append(configs, child)
	}
	return &TickRunner{Configs: configs, Interval: cfg.Interval, Logf: cfg.Logf}, nil
}

// logWriter forwards complete lines to a log function, prefixing each.
//
// It never writes to the process's stdout: the daemon's stderr is a log line,
// not a Monitor event stream.
type logWriter struct {
	logf   func(format string, args ...any)
	prefix string
	line   strings.Builder
}

// newLogWriter builds a line writer, defaulting to the standard logger.
func newLogWriter(logf func(format string, args ...any), prefix string) *logWriter {
	if logf == nil {
		logf = log.Printf
	}
	return &logWriter{logf: logf, prefix: prefix}
}

// Write logs every complete line and buffers the trailing partial one.
func (w *logWriter) Write(p []byte) (int, error) {
	w.line.Write(p)
	for {
		text := w.line.String()
		index := strings.IndexByte(text, '\n')
		if index < 0 {
			break
		}
		w.logf("%s%s", w.prefix, text[:index])
		w.line.Reset()
		w.line.WriteString(text[index+1:])
	}
	return len(p), nil
}
