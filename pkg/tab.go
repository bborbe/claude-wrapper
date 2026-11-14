// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
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
//
// The pane is ACTIVATED first, always. Measured 2026-09-14: two `send-text`
// calls against a running worker did nothing and left its transcript unchanged
// for three minutes; `activate-pane` first made the identical call land
// immediately. A send that reports success without activating is a message that
// may never have arrived.
func (t *tabClient) SendText(ctx context.Context, paneID string, text string) error {
	if err := t.Activate(ctx, paneID); err != nil {
		return err
	}
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
//
// `--escapes` is load-bearing: the wake guard distinguishes Claude Code's dim
// next-prompt suggestion from text somebody typed by its SGR attributes, and a
// plain read returns the two identically — which turns every long-idle pane into
// `skip:composer-not-empty` and starves the wake layer of exactly the sessions it
// exists for. Callers that want visible text strip the escapes themselves.
func (t *tabClient) Text(ctx context.Context, paneID string) (string, error) {
	// #nosec G204 -- t.bin is the configured wezterm binary; paneID is an
	// identifier WezTerm assigned, passed as its own argv word with no shell.
	output, err := exec.CommandContext(
		ctx, t.bin, "cli", "get-text", "--escapes", "--pane-id", paneID, "--start-line", "-600",
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

// shellEnvName is a legal shell variable name.
//
// The name is written into the `bash -lc` string UNQUOTED — only the value is
// quoted — so anything outside this alphabet is a command-injection surface, and
// a name that cannot be exported has to be refused here rather than dropped
// downstream.
var shellEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvNames are the variables the server owns and a caller may not set.
//
// `PERMISSION_MODE` is the shipping carrier: the launchers read
// `${PERMISSION_MODE:-auto}` and pass it to `--permission-mode`, so a caller
// setting `bypassPermissions` starts a tab worker with every approval gate
// disabled — and the export outlives the pane for anything launched from it.
// The two mode variables decide whether the worker's own attention relay runs,
// so a caller setting them can silence a worker's relay by telling it it is
// headless.
var reservedEnvNames = map[string]string{
	"PERMISSION_MODE": "the server owns a worker's permission mode. A caller setting " +
		"it could widen the worker past the launcher's own default — " +
		"`bypassPermissions` starts it with every approval gate disabled, and the " +
		"export outlives the pane",
	"SUPERVISOR_WORKER_MODE": "the server owns a worker's spawn mode and hands it to " +
		"the worker itself. A caller setting it would tell a tab worker it is " +
		"headless, which silences its attention-store relay",
	"SUPERVISOR_WORKER_MODE_SOURCE": "the server owns a worker's spawn mode and hands " +
		"it to the worker itself. A caller setting it would misreport where the " +
		"mode came from",
}

// ValidateTabEnv refuses an env map the pane cannot safely export.
//
// Refused, never dropped: a value silently missing from the worker looks exactly
// like a value that was never asked for.
func ValidateTabEnv(ctx context.Context, env map[string]string) error {
	for name := range env {
		if !shellEnvName.MatchString(name) {
			return errors.Errorf(
				ctx,
				"env name %q is not a legal shell variable name — it must start with a "+
					"letter or underscore and contain only letters, digits and underscores",
				name,
			)
		}
		if why, reserved := reservedEnvNames[name]; reserved {
			return errors.Errorf(ctx, "env cannot set %s: %s. Omit it", name, why)
		}
	}
	return nil
}

// ComposerReady reports whether a pane's composer is empty and accepting input.
//
// Readiness is an EMPTY composer at the prompt glyph — the LAST line that starts
// with the glyph, with everything after it trimming to nothing. A composer that
// is drawn but still holds a placeholder is not ready: a submit into it is
// swallowed, which reads as a delivered message that never ran.
//
// The line is stripped of escapes first, because `Text` reads with `--escapes`:
// an empty composer still carries a trailing SGR reset, and a raw comparison
// would call every empty composer non-empty.
func ComposerReady(text string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for _, rawLine := range slices.Backward(lines) {
		line := strings.TrimSpace(StripANSI(rawLine))
		if !strings.HasPrefix(line, promptGlyph) {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, promptGlyph)) == ""
	}
	return false
}

// submissionHead is the part of a message the pane is matched on.
//
// A pane WRAPS a long line, so requiring the whole message on one rendered line
// reports a landed message as unsubmitted — the caller then retries and the
// worker receives the turn twice. The first line, trimmed and truncated, is what
// survives wrapping and is still specific enough to identify this send.
func submissionHead(message string) string {
	first := message
	if index := strings.IndexAny(first, "\r\n"); index >= 0 {
		first = first[:index]
	}
	first = strings.TrimSpace(first)
	if len(first) > 40 {
		first = first[:40]
	}
	return first
}

// SubmissionConfirmed reports whether the pane shows a fresh prompt below the
// message, which is what separates a submitted message from a typed one.
//
// The empty composer is deliberately NOT the signal: it is also what a pane
// shows before anything was typed, so it cannot distinguish "the Enter landed"
// from "nothing happened yet".
func SubmissionConfirmed(text string, message string) bool {
	head := submissionHead(message)
	if head == "" {
		return false
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	messageSeen := false
	for _, line := range lines {
		if !messageSeen {
			if strings.Contains(line, head) {
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

// WakeConfirmed is the fleet wake path's positive evidence: a spinner over an
// empty composer.
//
// It mirrors `pane-wake.py`'s send() and is the escape-safe alternative to
// SubmissionConfirmed for this path. SubmissionConfirmed looks for the echoed
// message text on the raw escaped screen; with `--escapes` the SGR sequences
// interleave that text, so a send that DID land reads as unconfirmed — measured
// live on 2026-11-14, where three woken sessions carried the message in their
// transcripts while the log reported `failed:`.
func WakeConfirmed(screen, _ string) bool {
	if !PaneIsReady(screen) {
		return false
	}
	return PaneGuardVerdict(&screen, false, "") == VerdictSkipSpinner
}

// composerDeadline bounds the wait for a fresh pane's composer to appear.
//
// The launcher starts a shell, runs the `cc-*` script and boots the TUI, so the
// composer is not drawn when `spawn` returns. Sending before it is up types the
// text into the shell instead.
const (
	composerDeadline = 8 * time.Second
	confirmDeadline  = 8 * time.Second
	panePollInterval = 100 * time.Millisecond
)

// SendWhenReady activates a pane, waits for an empty composer, types text and
// confirms the submit landed.
//
// Every stage is a measured requirement, not caution: a send without
// `activate-pane` reaches nothing (2026-09-14), a submit into a drawn-but-
// occupied composer is swallowed (2026-09-20, 81 ms and 137 ms after a
// placeholder sample), and the send's exit code reports only that keystrokes
// reached the pty — true whether or not anything submitted. So the verdict is
// the pane's own output, and an unconfirmed submit is retried once with a bare
// Enter before being reported as a failure.
func SendWhenReady(
	ctx context.Context,
	tabs TabClient,
	paneID string,
	text string,
	now libtime.CurrentDateTimeGetter,
) error {
	return SendWhenReadyWith(ctx, tabs, paneID, text, now, ComposerReady, SubmissionConfirmed)
}

// SendWhenReadyWith is SendWhenReady with the readiness and confirmation rules
// injected.
//
// The spawn path must refuse a drawn-but-placeholder composer (a submit while the
// TUI is still painting is swallowed), so its readiness rule is ComposerReady and
// its confirmation is the echoed-message check. The fleet wake path is the
// opposite case on both counts: it reaches LONG-IDLE panes, where the dim
// next-prompt suggestion is always present and a send into it lands (PaneIsReady),
// and its positive evidence is a spinner over an empty composer (WakeConfirmed) —
// the echoed-message check reads the raw escaped screen, where SGR sequences
// interleave the text and make the substring match fail on a send that DID land.
func SendWhenReadyWith(
	ctx context.Context,
	tabs TabClient,
	paneID string,
	text string,
	now libtime.CurrentDateTimeGetter,
	ready func(string) bool,
	confirmed func(screen, message string) bool,
) error {
	if err := waitComposerWith(ctx, tabs, paneID, composerDeadline, now, ready); err != nil {
		return err
	}
	if err := tabs.SendText(ctx, paneID, text); err != nil {
		return err
	}
	if confirmSubmissionWith(ctx, tabs, paneID, text, now, confirmed) {
		return nil
	}
	// The Enter was swallowed. An extra Enter on an empty composer is harmless,
	// so one retry is cheaper than reporting a message that never ran.
	if err := tabs.SendText(ctx, paneID, ""); err != nil {
		return err
	}
	if confirmSubmissionWith(ctx, tabs, paneID, text, now, confirmed) {
		return nil
	}
	return errors.Errorf(
		ctx,
		"pane %s never showed the message as submitted — the keystrokes reached the pty "+
			"but the Enter did not take effect, so the message must not be reported as delivered",
		paneID,
	)
}

// waitComposerWith polls a pane until ready reports its composer accepting input.
func waitComposerWith(
	ctx context.Context,
	tabs TabClient,
	paneID string,
	timeout time.Duration,
	now libtime.CurrentDateTimeGetter,
	ready func(string) bool,
) error {
	deadline := now.Now().Time().Add(timeout)
	var lastErr error
	for {
		output, err := tabs.Text(ctx, paneID)
		if err == nil && ready(output) {
			return nil
		}
		lastErr = err
		if now.Now().Time().After(deadline) {
			if lastErr != nil {
				return errors.Wrapf(
					ctx, lastErr, "pane %s could not be read within %s", paneID, timeout,
				)
			}
			return errors.Errorf(
				ctx,
				"pane %s showed no empty composer within %s, so nothing was typed — a "+
					"message into a composer that is still booting is swallowed",
				paneID,
				timeout,
			)
		}
		select {
		case <-ctx.Done():
			return errors.Wrapf(ctx, ctx.Err(), "waiting for pane %s failed", paneID)
		case <-time.After(panePollInterval):
		}
	}
}

// confirmSubmissionWith polls a pane until the injected predicate confirms the submit.
func confirmSubmissionWith(
	ctx context.Context,
	tabs TabClient,
	paneID string,
	text string,
	now libtime.CurrentDateTimeGetter,
	confirmed func(screen, message string) bool,
) bool {
	deadline := now.Now().Time().Add(confirmDeadline)
	for {
		if output, err := tabs.Text(ctx, paneID); err == nil && confirmed(output, text) {
			return true
		}
		if now.Now().Time().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(panePollInterval):
		}
	}
}
