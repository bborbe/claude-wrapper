// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/bborbe/errors"
)

// promptGlyph marks the composer line of a Claude Code tab.
const promptGlyph = "❯"

// TabClient drives WezTerm's CLI for the local-tab spawn path.
//
// An interface so the orchestration can be tested without a terminal: every
// method here shells out to a GUI application, and a test that needs one is a
// test that only runs on the operator's laptop.
type TabClient interface {
	// Spawn opens a tab and returns its pane id.
	Spawn(ctx context.Context, options TabSpawnOptions) (string, error)
	// Activate focuses a pane.
	Activate(ctx context.Context, paneID string) error
	// SendText types text into a pane and submits it.
	SendText(ctx context.Context, paneID string, text string) error
	// Text returns the pane's recent output.
	Text(ctx context.Context, paneID string) (string, error)
}

// TabSpawnOptions is everything one tab spawn needs.
type TabSpawnOptions struct {
	Cwd      string
	Launcher string
	TabName  string
	Prompt   string
	// Env is exported into the pane before the launcher runs.
	Env map[string]string
	// WindowID is the WezTerm window, or nil for the current one.
	WindowID *int
	// ExitRecord is the session-exit recorder handed to the pane.
	//
	// Empty omits the hook entirely rather than naming a script that does not
	// exist: every call in the hook is `|| true`, so a missing script costs a
	// record, but an empty path would run `"" append …` and print noise into the
	// worker's own tab.
	ExitRecord string
}

// NewTabClient creates a TabClient over the wezterm binary.
//
// bin defaults to "wezterm" when empty.
func NewTabClient(bin string) TabClient {
	if bin == "" {
		bin = "wezterm"
	}
	return &tabClient{bin: bin}
}

type tabClient struct {
	bin string
}

// Spawn opens a tab and returns its pane id.
func (t *tabClient) Spawn(ctx context.Context, options TabSpawnOptions) (string, error) {
	args := []string{"cli", "spawn"}
	if options.WindowID != nil {
		args = append(args, "--window-id", strconv.Itoa(*options.WindowID))
	}
	args = append(args, "--")
	if options.ExitRecord != "" {
		args = append(args, "env", "SESSION_EXIT_RECORD="+options.ExitRecord)
	}
	args = append(args, "bash", "-lc", BuildTabInner(options))
	// #nosec G204 -- t.bin is the configured wezterm binary, and every
	// caller-supplied value (cwd, launcher, tab name, prompt, env) is single-quoted
	// into the one bash -lc string BuildTabInner renders, so none of them reaches
	// this argv as a separate word.
	output, err := exec.CommandContext(ctx, t.bin, args...).Output()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "could not open a tab")
	}
	return strings.TrimSpace(string(output)), nil
}

// Activate focuses a pane.
//
// By PANE id, never tab id: WezTerm renumbers tab ids when a tab moves between
// windows, so a tab id recorded at spawn time addresses the wrong tab later.
func (t *tabClient) Activate(ctx context.Context, paneID string) error {
	// #nosec G204 -- t.bin is the configured wezterm binary and paneID is an
	// identifier WezTerm assigned to a pane this daemon opened.
	if _, err := exec.CommandContext(ctx, t.bin, "cli", "activate-pane", "--pane-id", paneID).
		Output(); err != nil {
		return errors.Wrapf(ctx, err, "could not activate pane %s", paneID)
	}
	return nil
}

// SendText types text into a pane and submits it.
//
// `--no-paste` sends the text as keystrokes rather than through the paste
// mechanism, which is what lets the trailing carriage return submit it: a
// bracketed paste leaves the newline inside the composer. `\r` and not `\n` —
// a terminal's Enter is a carriage return, and a bare `\n` types a line break
// without submitting.
func (t *tabClient) SendText(ctx context.Context, paneID string, text string) error {
	framed := text
	if !strings.HasSuffix(framed, "\r") {
		framed += "\r"
	}
	// #nosec G204 -- t.bin is the configured wezterm binary; paneID is an
	// identifier WezTerm assigned, and framed is passed as one argv word with
	// --no-paste, so it is typed into the pty rather than interpreted by a shell.
	if _, err := exec.CommandContext(
		ctx, t.bin, "cli", "send-text", "--pane-id", paneID, "--no-paste", framed,
	).Output(); err != nil {
		return errors.Wrapf(ctx, err, "send-text to pane %s failed", paneID)
	}
	return nil
}

// Text returns the pane's recent output.
//
// The window is large enough to hold the composer and the last exchange: the
// readiness and submission checks both read the tail, and a window that cuts
// the composer reads as "not ready" forever.
func (t *tabClient) Text(ctx context.Context, paneID string) (string, error) {
	// #nosec G204 -- t.bin is the configured wezterm binary; paneID is an
	// identifier WezTerm assigned, passed as its own argv word with no shell.
	output, err := exec.CommandContext(
		ctx, t.bin, "cli", "get-text", "--pane-id", paneID, "--start-line", "-600",
	).Output()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "could not read pane %s", paneID)
	}
	return string(output), nil
}

// BuildTabInner renders the `bash -lc` command a spawned pane runs.
//
// Three details are load-bearing and each was a measured defect:
//
//   - The exports come BEFORE the `cd`, so they survive the login shell's own
//     profile — a profile that sets the ambient model would otherwise overwrite
//     the launcher's pin.
//   - The whole tail is one brace group. Without it, `cd X && f() {...}; a; b`
//     lets a failed `cd` skip only the function DEFINITION and still launch the
//     worker, in the wrong directory.
//   - There is no `exec`. `exec` replaces the pane's shell, so the EXIT trap
//     never runs and exit 137 can never be recorded.
func BuildTabInner(options TabSpawnOptions) string {
	var builder strings.Builder
	for name, value := range options.Env {
		builder.WriteString("export ")
		builder.WriteString(name)
		builder.WriteString("=")
		builder.WriteString(shellQuote(value))
		builder.WriteString("; ")
	}
	builder.WriteString("cd ")
	builder.WriteString(shellQuote(options.Cwd))
	builder.WriteString(" && { ")
	if options.ExitRecord != "" {
		builder.WriteString(`_sx() { rc=$?; "$SESSION_EXIT_RECORD" append --pid $$ --rc "$rc" || true; }; `)
		builder.WriteString(`"$SESSION_EXIT_RECORD" append --kind spawn --pid $$ || true; `)
		builder.WriteString(`"$SESSION_EXIT_RECORD" observe --pid $$ || true; `)
		builder.WriteString("trap _sx EXIT; ")
	}
	builder.WriteString(shellQuote(options.Launcher))
	builder.WriteString(" -n ")
	builder.WriteString(shellQuote(options.TabName))
	builder.WriteString(" ")
	builder.WriteString(shellQuote(options.Prompt))
	builder.WriteString("; }")
	return builder.String()
}

// shellQuote wraps a value in single quotes, escaping embedded ones.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ComposerReady reports whether a pane's composer is empty and accepting input.
//
// Readiness is an EMPTY composer at the prompt glyph — the LAST line that starts
// with the glyph, with everything after it trimming to nothing. A composer that
// is drawn but still holds a placeholder is not ready: a submit into it is
// swallowed, which reads as a delivered message that never ran.
func ComposerReady(text string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for _, rawLine := range slices.Backward(lines) {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, promptGlyph) {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, promptGlyph)) == ""
	}
	return false
}

// SubmissionConfirmed reports whether the pane shows a fresh prompt below the
// message, which is what separates a submitted message from a typed one.
//
// The empty composer is deliberately NOT the signal: it is also what a pane
// shows before anything was typed, so it cannot distinguish "the Enter landed"
// from "nothing happened yet".
func SubmissionConfirmed(text string, message string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	messageSeen := false
	for _, line := range lines {
		if !messageSeen {
			if strings.Contains(line, message) {
				messageSeen = true
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), promptGlyph) {
			return true
		}
	}
	return false
}
