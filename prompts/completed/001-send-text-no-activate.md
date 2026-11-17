---
status: completed
summary: Removed the activate-pane call from tabClient.SendText, updated both doc comments to the 2026-11-09 measurement, added a stub-binary spec proving no activation, and recorded the fix under Unreleased.
execution_id: claude-wrapper-send-text-no-activate-exec-001-send-text-no-activate
dark-factory-version: v0.198.17
created: "2026-11-17T16:08:52Z"
queued: "2026-11-17T16:10:36Z"
started: "2026-11-17T16:13:43Z"
completed: "2026-11-17T16:15:55Z"
---

<summary>
- Typing a message into a worker's WezTerm pane no longer switches the operator's visible tab
- Fleet wakes and roster sends deliver text to background tabs without stealing focus
- Delivery is still confirmed from the pane's own output, exactly as before
- The pane-focus capability stays available; only the send path stops using it
- Code comments describing the old "must focus first" measurement are replaced with the newer measurement that disproves it
- A new test drives the real WezTerm client against a fake executable and proves no focus command is issued
- Changelog records the fix under Unreleased
</summary>

<objective>
`tabClient.SendText` must run only `wezterm cli send-text --pane-id <id> --no-paste <text>\r` and never `wezterm cli activate-pane`. Activating on every send pulls the operator's WezTerm tab away on every fleet wake; a 2026-11-09 measurement showed `send-text` reaches a pane in a background tab without activation. Doc comments must reflect the new measurement, and a test against a stub `wezterm` binary must pin the behaviour.
</objective>

<context>
Read CLAUDE.md (if present) and `README.md` for project conventions.

Detail level: Level 2 (annotated skeleton). Change is small and mechanical in production code (delete one call, rewrite two comments); the test is the only new code and follows an established stub-executable pattern, so the test file shape is given while assertions stay explicit.

Files to read before changing anything:
- `pkg/tab.go` — `TabClient` interface, `NewTabClient(bin string) TabClient`, `type tabClient struct { bin string }`, `func (t *tabClient) Activate(ctx context.Context, paneID string) error`, `func (t *tabClient) SendText(ctx context.Context, paneID string, text string) error`, and the doc comment above `func SendWhenReady(...)`.
- `pkg/pkg_suite_test.go` — the suite; specs are in-package `package pkg` and use QUALIFIED `ginkgo.` / `gomega.` (no dot-imports — a dot-imported `Pending` collides with the package's own `Pending` type).
- `pkg/tab_test.go` — existing tab specs (style: `var _ = ginkgo.Describe(...)`, `ginkgo.It(...)`, `gomega.Expect(...)`).
- Stub-executable exemplars (shell script written to a temp dir, made executable, path handed to the code under test):
  - `pkg/ledger_verdict_test.go` — `answeringChild` / `silentChild` helpers: `script := "#!/bin/sh\n" + ...`, `gomega.Expect(os.WriteFile(path, []byte(script), 0o700)).To(gomega.Succeed())`, `dir = ginkgo.GinkgoT().TempDir()`, `ctx = context.Background()` in `BeforeEach`.
  - `pkg/durable_test.go` — `echoChild` helper, same pattern.
- `pkg/tab_spawn_test.go` and `pkg/fleet_pusher_test.go` — hold fake `TabClient` implementations (`fakeTabClient`, `fakeTabs`) with an `Activate` method; they must keep compiling, which they will because the interface is unchanged.
- `CHANGELOG.md` — top of file; currently the first version heading is `## v0.30.0` and there is no `## Unreleased`.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
1. **`pkg/tab.go` — `tabClient.SendText`: remove the activate call.**
   Delete exactly this block at the start of the method body (~line 122):
   ```go
   	if err := t.Activate(ctx, paneID); err != nil {
   		return err
   	}
   ```
   Leave everything else in the method unchanged: the `\r` framing (`if !strings.HasSuffix(framed, "\r") { framed += "\r" }`), the `#nosec G204` comment, the `exec.CommandContext(ctx, t.bin, "cli", "send-text", "--pane-id", paneID, "--no-paste", framed).Output()` call, and the `errors.Wrapf(ctx, err, "send-text to pane %s failed", paneID)` error path (it stays the only failure path: a non-zero exit of the binary returns the wrapped error).

2. **`pkg/tab.go` — `SendText` doc comment.** Keep the first two paragraphs (`SendText types text into a pane and submits it.` and the `--no-paste` / `\r` paragraph) unchanged. Replace the paragraph beginning `The pane is ACTIVATED first, always. Measured 2026-09-14: ...` (through `... may never have arrived.`) with a paragraph stating, in the file's existing comment voice:
   - The pane is NOT activated. Measured 2026-11-09: `send-text` reaches a pane in a background tab — 6/6 bytes arrived, and `focused_pane_id` was unchanged before and after the send.
   - Activating pulled the operator's WezTerm tab away on every fleet wake, so it is not done here.
   - Callers confirm delivery from the pane's output (see `SendWhenReady`), not from focus; the send's exit code only says the keystrokes reached the pty.
   The comment must not contain the phrase `ACTIVATED first` nor claim that a send without activation reaches nothing.

3. **`pkg/tab.go` — `SendWhenReady` doc comment (~lines 352–360).** Rewrite so it:
   - no longer says it "activates a pane" — first sentence becomes e.g. `SendWhenReady waits for an empty composer, types text and confirms the submit landed.`
   - no longer says `a send without activate-pane reaches nothing (2026-09-14)`.
   - keeps the remaining measured requirements: a submit into a drawn-but-occupied composer is swallowed (2026-09-20, 81 ms and 137 ms after a placeholder sample), and the send's exit code reports only that keystrokes reached the pty, so the verdict is the pane's own output and an unconfirmed submit is retried once with a bare Enter before being reported as a failure.
   Do not change any code in `SendWhenReady` / `SendWhenReadyWith`.

4. **Keep unchanged:** the `TabClient` interface (including `Activate(ctx context.Context, paneID string) error`), `func (t *tabClient) Activate(...)` and its doc comment, `Spawn`, `Text`, and every fake `TabClient` in tests. The spawn path is out of scope. After the change, `rg -n 't\.Activate\(|\.Activate\(ctx' pkg/*.go | grep -v _test` must return nothing (no production caller of `Activate` remains); that is expected, do not delete the method.

5. **New test file `pkg/tab_send_text_test.go`** (`package pkg`, copyright header identical to `pkg/tab_test.go`, qualified `ginkgo` / `gomega` imports, joins the existing `Pkg Suite` — do NOT add a new `TestXxx`/`RunSpecs`). Shape:
   ```go
   var _ = ginkgo.Describe("tabClient.SendText", func() {
   	var (
   		ctx     context.Context
   		dir     string
   		logPath string
   		client  TabClient
   	)

   	ginkgo.BeforeEach(func() {
   		ctx = context.Background()
   		dir = ginkgo.GinkgoT().TempDir()
   		logPath = filepath.Join(dir, "argv.log")
   		stubPath := filepath.Join(dir, "wezterm")
   		// one line per invocation: the stub's full argv
   		script := "#!/bin/sh\n" +
   			"printf '%s\\n' \"$*\" >> '" + logPath + "'\n"
   		gomega.Expect(os.WriteFile(stubPath, []byte(script), 0o700)).To(gomega.Succeed())
   		client = NewTabClient(stubPath)
   	})

   	ginkgo.It("types into the pane without activating it", func() {
   		gomega.Expect(client.SendText(ctx, "42", "hello")).To(gomega.Succeed())
   		// read logPath, split into non-empty lines, then assert (see below)
   	})
   })
   ```
   Assertions in the `It`:
   - Read `logPath` with `os.ReadFile`; `gomega.Expect(err).NotTo(gomega.HaveOccurred())`.
   - Split on `"\n"` and drop empty lines (the trailing newline yields one empty element).
   - `gomega.Expect(lines).To(gomega.HaveLen(1))`.
   - `gomega.Expect(lines[0]).To(gomega.ContainSubstring("cli send-text --pane-id 42 --no-paste"))`.
   - Count lines containing `activate-pane` and assert the count is `0` (e.g. loop + counter, or `gomega.Expect(lines).NotTo(gomega.ContainElement(gomega.ContainSubstring("activate-pane")))`).
   Imports needed: `context`, `os`, `path/filepath`, `strings`, `github.com/onsi/ginkgo/v2`, `github.com/onsi/gomega`.
   This test crosses the real boundary: real `NewTabClient`, real `exec.CommandContext`, real argv — no fake `TabClient`.

6. **Acceptance: the test must fail against the pre-fix code (commit `4e30b16`).** On that code, `SendText` first invokes the stub with `cli activate-pane --pane-id 42`, so the log holds two lines and both the `HaveLen(1)` and zero-`activate-pane` assertions fail. Demonstrate this either by (a) writing the test first, running `go test ./pkg/... -run TestPkg -ginkgo.focus='tabClient.SendText'` (or `make test`) BEFORE applying requirement 1 and observing the failure, then applying the fix and observing it pass; or (b) stating that reasoning explicitly in your final report. Prefer (a).

7. **`CHANGELOG.md`.** Insert a `## Unreleased` heading directly above the first `## v` heading (currently `## v0.30.0`), with a blank line before and after, if no `## Unreleased` heading exists. There must be exactly one `## Unreleased` in the file afterwards. Under it add exactly this bullet:
   ```
   - fix: tabClient.SendText no longer runs wezterm activate-pane, so fleet wakes and roster sends stop switching the operator's tab
   ```
   Do not modify any released `## vX.Y.Z` section.
</requirements>

<constraints>
- Remove only the `t.Activate(ctx, paneID)` call from `tabClient.SendText`; `SendText` must run only `wezterm cli send-text --pane-id <id> --no-paste <text>\r`.
- Keep the `Activate` method and the `TabClient` interface unchanged (no other caller of `Activate`; the spawn path is out of scope).
- Do not change behaviour of `SendWhenReady` / `SendWhenReadyWith` — doc comment only.
- Tests: Ginkgo v2 + Gomega, in-package `package pkg`, qualified (non-dot) imports, existing `Pkg Suite`; no new test suites, no counterfeiter mocks for this test (it must use the real client against a stub binary).
- Error wrapping stays `github.com/bborbe/errors` (`errors.Wrapf(ctx, err, ...)`); do not introduce `fmt.Errorf`.
- Exactly one `## Unreleased` heading in `CHANGELOG.md`; never edit released sections.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Additional checks:
- `rg -n 'activate-pane' pkg/tab.go` — only matches inside `Activate` (the exec call) and nowhere in `SendText` or the `SendWhenReady` comment.
- `rg -n 'ACTIVATED first' pkg/tab.go` — no matches.
- `rg -c '^## Unreleased' CHANGELOG.md` — prints `1`.
- `go test ./pkg/... -run TestPkg -ginkgo.focus='tabClient.SendText' -ginkgo.v` — the new spec passes.
</verification>
