---
spec: ["003-sweep-gate-port"]
status: draft
created: "2026-11-18T00:35:03Z"
---

# sweepgate render: box table, frame, action lines, tick mode

<summary>
- The daemon draws the same table the vault-side script draws, including the exact column widths, box drawing characters, icon cell widths and ellipsis truncation
- Grouping is by indent alone, the root sits flush left on both branches, and a goal subject is never drawn twice
- Action lines are emitted only where a model-free read can honestly produce one, and are silent for every row that is held, deferred, unapproved or waiting on something outside the worker
- A tick that finds nothing still writes the table to its state file and still prints why its staleness threshold is what it is, while printing nothing at all on stdout
- A tick that finds a change prints the frame and one verdict line, and the exit code distinguishes a change from a failure
- The parity test compares the daemon's frame and stderr against output captured from the Python script, byte for byte, and a hand-derived case proves the comparison is not just replaying stored bytes
- The table renderer no longer shells out to a second Python process
</summary>

<objective>
Port the rendering half of the gate into `pkg/sweepgate` and make it executable as the tick mode: the box-table renderer, the marker, the grouped frame, the action lines, the close line, the tick-file write and the change verdict. This is the prompt that satisfies AC2's byte-identical frame and AC4's "a no-change tick still proves it ran".
</objective>

<context>
Read the package created by the two previous prompts: `pkg/sweepgate/config.go`, `task.go`, `snapshot.go`, `digest.go`, `state.go`, `bucket.go`, `holds.go`, `liveness.go`, `parks.go`, and their tests.

Read the reference at `testdata/sweepgate/reference/sweep-gate.py`. The functions you are porting are `render_table`, `marker`, `status_label`, `session_cell`, `frame_of`, `action_lines`, `closer_of`, `close_line`, `strip_links`, `write_tick`, and the constants `HEADER` (`["Topic / Goal / Task", "Session", "Status", "Phase", "Met"]`), `WIDTHS` (`[42, 10, 34, 9, 17]`), `BOX_TABLE`, `CLOSE_LINE`, `REAP_CLOSER`.

The box-table renderer being replaced is `box-table.py` in the supervisor plugin — read it at `/home/node/.claude/plugins/marketplaces/claude-supervisor/scripts/box-table.py`. It is the byte-level contract: `rule("┌","┬","┐",widths)`, the header row, `rule("├","┼","┤",widths)`, one `line` per row, `rule("└","┴","┘",widths)`; a cell is `"│ " + " │ ".join(cells) + " │"`; a cell is truncated to its column's display width with `…` occupying the last cell, then padded with spaces; display width counts `️` and combining marks as 0 and East-Asian Wide/Fullwidth runes as 2.

Repo patterns to follow:
- `pkg/flagged_rows_source.go` — `CommandRunner` / `ExecCommandRunner` for the two plugin scripts this prompt still shells out to.
- `pkg/flagged_tick.go` — `Run(ctx) error` returning `ctx.Err()`, `Logf func(format string, args ...any)`, a sequential loop that delays the next tick rather than overlapping it.
- `pkg/state_ledger.go` — atomic file writes through a `.tmp` + rename.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md`
</context>

<requirements>
1. **`pkg/sweepgate/boxtable.go` — the renderer, in Go, with no subprocess.**
   - `func RenderBox(header []string, rows [][]string, widths []int) string` — reproduces `box-table.py`'s `render` exactly (five-part structure above, `─` repeated `w+2` per column, single spaces inside the borders).
   - `func DisplayWidth(s string) int` and `func CellWidth(r rune) int`: `️` → 0, `unicode.Is(unicode.Mn, r)` → 0, `width.LookupRune(r).Kind()` in `{width.EastAsianWide, width.EastAsianFullwidth}` → 2, otherwise 1. Import `golang.org/x/text/width`. ⚠️ Write the importing file BEFORE running `go mod tidy`; `golang.org/x/text` is currently an indirect dependency and tidy removes a direct entry that nothing imports yet.
   - `func FormatCell(s string, w int, url string) string` — strip any OSC 8 already present, truncate by display width reserving one cell for `…`, pad to `w`, then wrap the VISIBLE text in OSC 8 (`\x1b]8;;<url>\x1b\\<text>\x1b]8;;\x1b\\`) when `url` is non-empty, with the padding outside the escape.
   - The widths are `HeaderWidths = []int{42, 10, 34, 9, 17}` and the header is `FrameHeader`. Do not "improve" a width: the Status column's 34 is sized for `⏸️ HELD — <reason> · <age>`, and 30 truncates the age away.
   - No shell-out to `box-table.py`, ever. That dependency is the reason AC2's comparison is byte-level.

2. **`pkg/sweepgate/render.go` — marker, session cell, grouped table.**
   - `func SessionCell(t Task) string` — no ids → `—`; otherwise the current owner if set, else the newest `SessionIDs` entry, shortened to 8 characters; `[<sid8>]` when liveness is `live` or `parked`, `(<sid8>)` when `none`. `—` is a provenance claim (nothing was ever recorded), never a liveness one.
   - `func Marker(topic string, tasks []Task, changed bool, now time.Time) string` — `<HH:MM> ✓ <topic> — <n> tasks · <count> <glyph> · … · change|no change`, counts iterating `BucketOrder` and skipping zero counts. `HH:MM` is `now.Format("15:04")`.
   - `func RenderTable(ctx context.Context, cfg Config, snap Snapshot, changed bool) (string, error)`:
     - topic branch: root row flush left (`<topic>`, `—`, `StatusLabel(in_progress)`, `—`, `SC <sc> · Gate <gate>` from the topic page's `# Success Criteria` and `# Completion Gate`), goals at 3 spaces, tasks at 6.
     - goal branch: the goal IS the root (flush left, `—`, `StatusLabel(goal status)`, `—`, `<sc>`), tasks at 3 spaces, and **no synthetic topic row** — a synthetic row drew the goal name twice.
     - a task naming several member goals is listed once, under its FIRST member goal in `Members` order.
     - tasks that match no member goal go under one row `(declared directly — no member goal)` at the goal depth.
     - a goal with no owned tasks is skipped entirely.
     - task row: `<depth><name>`, `SessionCell`, `Bucket` label, `Phase` or `—`, `Met` or `—`.
     - an unreadable topic/goal page contributes empty text, never an error.
   - `func Frame(ctx context.Context, cfg Config, snap Snapshot, changed bool) (string, error)` — the marker, then the box two spaces indented (every line of it), then each action line two spaces indented, joined by `\n`.
   - Jump links: a cell that is EXACTLY `[<sid8>]` gets an OSC 8 URL resolved by two `ExitRunner` calls — `who-needs-me.py --pane-for <sid8>` then `jump-link.py <pane>` — and every failure (non-zero exit, non-numeric pane, a URL not starting `http`) yields no link, so the cell renders plain. Honour the same disable switch the reference does: `BOX_TABLE_NO_LINKS` in `{1,true,yes,on}` disables links. Resolve each prefix at most once per render.

3. **`pkg/sweepgate/actions.go` — the action lines.**
   - `func ActionLines(ctx context.Context, cfg Config, tasks []Task) ([]string, error)` — iterate in the reference's order and emit only these:
     - `🚀 READY TO START: <name> — start a session: /supervisor:open "<name>"` for key `ready`
     - `👤 YOURS: <name> — not a spawn target` for key `yours`
     - the close line for key `done` whose liveness is `live` or `parked`
     - `⏸️ BLOCKED UPSTREAM: <name> — blocked_by <blocker> unshipped — no action until it lands` for key `upstream` (first unmet blocker, `?` when the list is somehow empty)
     - `⌛ PARKED-ON-UNREGISTERED-GATE: <name> — no feed row and the file has not moved for ≥2 intervals — SendMessage a check-in: "booted? first step? or parked on a gate?"` for key `unregistered`
     - `⏸️ HOLD: <name> — parked` for key `hold`
     - `◦ BACKLOG: <name> — someday, no action` for key `backlog`
     - concatenation order: ready, yours, close, upstream, unregistered, hold, backlog.
   - **Silences, each an explicit `continue` with a one-line comment:** a held session emits NOTHING (checked before deferred — a hold says "not by you", a defer says "not yet"); a deferred task emits nothing; keys `approval`, `external` and `declared` emit nothing. These branches must exist explicitly, not be left to fall-through.
   - Define the command runner LOCALLY in this package, because these scripts' EXIT CODE is part of their contract and the root package's `CommandRunner` returns only stdout:
     ```go
     // ExitRunner runs one command and reports its stdout and its exit code.
     type ExitRunner interface {
         Run(ctx context.Context, name string, args ...string) (stdout []byte, exitCode int, err error)
     }
     // ExecRunner is the production runner.
     type ExecRunner struct{}
     ```
     `ExecRunner.Run` uses `exec.CommandContext` and returns the process exit code (a non-zero exit is NOT an error; only a spawn failure is). `pkg/flagged_rows_source.go`'s `CommandRunner` is the precedent for the interface shape, not an import target — do not import the root package for it.
   - `func CloserOf(ctx context.Context, runner ExitRunner, cfg Config, sessionID string) string` — `<scriptsDir>/reap-closer.py <sid>`; exit 0 → `closed`, 1 → `open`, 2 or unparsable → `unreadable`. A failed read maps to `unreadable`, never `closed`.
   - `func CloseLine(ctx context.Context, runner ExitRunner, cfg Config, task, session, liveness, closer string) (string, error)` — `<scriptsDir>/close-line.py --task <task> --session <session> --liveness <liveness> --closer <closer>`; exit 0 → the trimmed stdout; exit 1 → empty string and nil error (the producer declining is a normal outcome); any other exit or a spawn failure → an error. Never hand-assemble the line.
   - The two plugin scripts live under the supervisor marketplace clone, at `/home/node/.claude/plugins/marketplaces/claude-supervisor/scripts/` in the container and `~/.claude/plugins/marketplaces/claude-supervisor/scripts/` on the host; resolve the directory once with `os.UserHomeDir()` and a package constant for the relative part. A missing script is a spawn failure, which `CloseLine` returns as an error and `CloserOf` reports as `unreadable`.

4. **`pkg/sweepgate/tick.go` — the tick mode.**
   ```go
   // CardFunc builds the card payload for a tick, or nil when the invariant does not fire.
   type CardFunc func(ctx context.Context, prepared Prepared) ([]byte, error)
   // Tick runs one tick and writes what the reference writes.
   func Tick(ctx context.Context, cfg Config, stdout, stderr io.Writer, card CardFunc) int
   ```
   Behaviour, in the reference's order:
   1. `Prepare` (defined in the previous prompt) — resolve, enrich, apply parks/stuck/waits/blocked. On an unresolvable or unreadable subject: print `fail-open: subject unreadable/unparseable: <err>` to STDERR, print NOTHING to stdout, return `ExitFailed` (2). Never a `CHANGE` line — stdout is a Monitor's event stream.
   2. compute `ParkThreshold`, then print `park: <basis>` to stderr on EVERY tick, before rendering.
   3. load the stored digest; `changed` is true when the load failed (first run, unreadable, parse error) or the digest differs.
   4. if `card != nil`, build and write it (`WriteCardFile`); a write failure prints `fail-open: could not write card file (<err>)` to stderr and does not change the verdict.
   5. render the frame, then ALWAYS write the tick file (`WriteTickFile`); a write failure prints `fail-open: could not write tick file (<err>)` to stderr and returns `ExitFailed`.
   6. when `changed`: print the frame to stdout, then `VERDICT change (<failReason or "digest differs">)`. When not changed: print nothing on stdout.
   7. save the state; a save failure prints `fail-open: could not record state (<err>)` to stderr and returns `ExitFailed` (never `ExitChange` — that would send the host down a change-wake path for a tick that rendered nothing).
   8. return `ExitChange` (10) when changed, else `ExitNoChange` (0).
   - A tick file write must never be skipped, including a no-change tick: the file is the operator-facing table.
   - The tick file content is `StripLinks(frame)` with exactly one trailing newline, mode `0600` at creation.

5. **The parity test (AC2).** `Describe("renders the parity fixture", ...)` must appear verbatim — the spec's verification greps for it.
   - For each golden case under `testdata/sweepgate/` (`<case>.render.golden.txt` + `<case>.render.stderr.golden.txt`): read the golden, assert line 1 matches `^# generated by sweep-gate\.py@[0-9a-f]{8,}$`, take the sha as `marker`, strip that line, then render the fixture with the same configuration `generate.sh` used.
   - Compare stdout and stderr SEPARATELY — half the contract is stderr (the `park:` line and every failure path), so a stdout-only comparison checks the weaker half.
   - Normalise EXACTLY ONE thing before comparing: the marker's leading `HH:MM` on both sides (replace `^\d{2}:\d{2}` on the first line with `HH:MM`). The reference calls `datetime.now()` for the marker and the port uses the injected clock, so an un-normalised comparison fails whenever the minute changes between capture and run. Nothing else may be normalised, and a second normalisation is a bug: it hides a real byte difference.
   - Print, per case, via `ginkgo.GinkgoWriter.Printf`, exactly:
     `case=%s stdout_bytes=%d stderr_bytes=%d identical=%t marker=%s`
   - **At least one case is NOT golden-backed.** Add a second fixture with different subject names, row shapes and bucket mix (`testdata/sweepgate/vault-second/`), whose expected frame is stated INLINE in the test, derived by hand from the classification rules (a raw string literal in the test file, not a file the renderer could have produced). It follows the same mtime-independence rules as the first fixture (no checkboxes on `execution` rows, a far-future `defer_date`), so it is stable without a captured golden. Print `case=%s golden=false identical=%t` for it. A port that returns the checked-in bytes by case name passes every golden case and fails this one.
   - The fixture must contain no live session, so no jump link is resolved and the frame is machine-independent. Assert that the rendered frame contains no `\x1b]8;` escape.

6. **AC4 — a no-change tick proves it ran.** `Describe("writes the tick on a no-change tick", ...)` (verbatim) drives two ticks against unchanged fixture input and asserts, for the pair: two tick-file writes (observed through the file's mtime or content), two `park:` lines on stderr, and ZERO bytes on stdout. Print the counts via `ginkgo.GinkgoWriter.Printf` (e.g. `tick_writes=%d park_lines=%d stdout_bytes=%d`).

7. **The other mode entry points stay out of this prompt.** Only `Tick` is implemented here. `Check`, `Render` and `Invariant` arrive in the next two prompts; do not add them.

8. **`CHANGELOG.md`.** Add one bullet under `## Unreleased`:
   `- feat: pkg/sweepgate renders the sweep frame in Go — byte-identical box table, grouped rows, action lines and the tick mode, with the parity fixture as the proof and no subprocess for the table`
   Do not create a second `## Unreleased` heading.

9. **Self-check before finishing.** Re-run `<verification>` and confirm it passes. Walk requirements 1-8. Confirm the parity test fails if you delete the marker line from a golden (try it, then restore), and confirm a no-change tick writes the tick file.
</requirements>

<constraints>
- The frame must be byte-identical to the Python's for the same inputs (AC2). The only permitted normalisation is the marker's `HH:MM`.
- The renderer must NOT shell out to `box-table.py`. It MAY shell out to `close-line.py`, `reap-closer.py`, `who-needs-me.py` and `jump-link.py` through `ExitRunner` — those are separate producers with their own contracts, and porting them is not this change.
- Grouping is three spaces per level with the root flush left on BOTH branches; the goal branch emits no synthetic topic row; a task naming several goals is listed once under its first member goal; the Essential/Optional split is deliberately not rendered.
- Every failure path writes to stderr only and never emits a `CHANGE` line or a `VERDICT` line.
- `ctx` is threaded and checked; a cancelled context returns without writing.
- `github.com/bborbe/errors` only; do not introduce `fmt.Errorf`.
- The clock is `libtime.CurrentDateTimeGetter` from `Config.Now`; no `time.Now()`.
- Ginkgo v2 + Gomega, in-package tests, qualified imports, no counterfeiter mocks.
- No hardcoded vault path (AC7).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Additional checks (all container-executable):
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='renders the parity fixture' -ginkgo.v` — exits 0 and prints one `case=<name> stdout_bytes=<n> stderr_bytes=<n> identical=true marker=<sha>` line per golden case plus `case=<name> golden=false identical=true` for the hand-derived case.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='writes the tick on a no-change tick' -ginkgo.v` — exits 0 and prints counts with `stdout_bytes=0`.
- `! grep -rq 'box-table' pkg/sweepgate/` — the package never names or shells out to the old table renderer.
- `head -1 testdata/sweepgate/parity-topic.render.golden.txt | grep -qE '^# generated by sweep-gate\.py@[0-9a-f]{8,}$'` — the provenance marker is intact.
- `! grep -q 'Obsidian/Personal' pkg/sweepgate/*.go` — no hardcoded vault path.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
</verification>
