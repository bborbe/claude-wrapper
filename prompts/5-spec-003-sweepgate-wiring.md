---
spec: ["003-sweep-gate-port"]
status: draft
created: "2026-11-18T00:35:03Z"
---

# sweepgate wiring: four CLI modes and the daemon tick

<summary>
- The daemon binary can run any one of the gate's four modes and exit with that mode's own exit code, so an external caller can invoke it exactly as it invoked the script
- The daemon itself starts one periodic tick that covers every configured subject and writes each one's table file
- The tick can be disabled with one interval flag or by configuring no subjects, and disabling it is announced rather than silent
- No vault path is compiled in; the vault, the subjects and the state directory all come from flags
- A test proves the wiring actually writes a tick file, not merely that a goroutine was started
</summary>

<objective>
Wire `pkg/sweepgate` into the daemon: add the two remaining mode entry points, the four-mode CLI dispatch that runs before the listener binds, the periodic tick goroutine over the configured subjects, and the in-process test that observes a tick file being written. This satisfies AC7 and AC9, and gives the plugin's `--invariant` caller a daemon-owned command to be repointed at.
</objective>

<context>
Read `main.go` in full — especially `registerFlaggedTickFlags`, the `--mcp-stdio` early return, the `--fleet-pusher-once` early return (the precedent for a mode that runs before the supervisor is built and never binds the listener), `startFleetFaces`, `startFlaggedTick` (the precedent for a tick goroutine that is disabled by a zero interval and logs its own stop), and `expandHome`.

Read the package as it stands: `pkg/sweepgate/*.go` — `Config`, `Prepare`, `Tick`, `Invariant`, `CardBuilder`, `NewVaultSnapshotter`, the exit constants and the state paths.

Repo patterns to follow:
- `pkg/flagged_tick.go` — `Run(ctx) error` returns `ctx.Err()` when the context ends; the loop is sequential, so an overrun delays the next tick rather than stacking.
- `main.go`'s `startFlaggedTick` — an interval of 0 disables the tick and says so in the log.
- `pkg/fleet_pusher.go` and `main.go`'s `runFleetPusherOnce` — the shape of a one-shot mode that returns an exit code instead of serving.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-cli-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. **`pkg/sweepgate/mode_check.go` and `pkg/sweepgate/mode_render.go` — the two remaining modes.**
   - `func Check(ctx context.Context, cfg Config, stdout, stderr io.Writer) int` — `Prepare`, print `park: <basis>` to stderr, compare the digest against the stored one, then print `CHANGE <reason>` (exit `ExitChange`) or `NOCHANGE` (exit `ExitNoChange`). `<reason>` is the stored-state failure reason when the load failed, otherwise `digest differs`. It writes no tick file, no card and no state.
   - `func Render(ctx context.Context, cfg Config, stdout, stderr io.Writer) int` — `Prepare`, print `park: <basis>` to stderr, print the frame plus one trailing newline, return `ExitChange` when changed else `ExitNoChange`. It writes no tick file, no card and no state.
   - Both: an unresolvable or unreadable subject prints `fail-open: subject unreadable/unparseable: <err>` to stderr, NOTHING to stdout, and returns `ExitFailed` (2) — never a `CHANGE` line.

2. **`pkg/sweepgate/config.go` — add the daemon's subject list and the injectable source.** Add two fields to `Config` without renaming any existing one:
   - `Subjects []string` — "the subjects the daemon tick covers; empty disables the daemon tick". `Subject` remains the single subject a one-shot mode runs against.
   - `Source Snapshotter` — nil means "build the real vault snapshotter from this config". Every mode and `Prepare` resolve through `cfg.Source` when it is non-nil, which is what lets a test drive any mode, including the daemon runner, without a vault.
   `Validate` requires `Subject` only for the one-shot modes; the daemon path validates that `Vault` is set and each subject is non-empty. Update `Prepare` so it reads the subject through `cfg.Source` when that field is non-nil and only falls back to `NewVaultSnapshotter(cfg)` when it is nil — every mode then inherits the injected source with no signature change.

3. **`pkg/sweepgate/runner.go` — the periodic tick over subjects.**
   ```go
   // Runner ticks every configured subject on an interval until the context ends.
   type Runner interface {
       Run(ctx context.Context) error
   }
   // TickRunner is the periodic tick. Its collaborators are exported fields, the
   // same shape as pkg.FlaggedTick, so a test can drive it without a vault.
   // Configs carries one Config per subject, each with Subject set.
   type TickRunner struct {
       Configs  []Config
       Interval time.Duration
       Logf     func(format string, args ...any)
   }
   func (r *TickRunner) Run(ctx context.Context) error
   // NewTickRunner expands cfg.Subjects into one Config per subject.
   func NewTickRunner(cfg Config) (Runner, error)
   ```
   - `Run` ticks IMMEDIATELY on start, then waits one interval between passes — a daemon that restarts after a deploy must not wait a full interval to re-render.
   - Each pass iterates `Configs` sequentially; a subject that fails (unresolvable, unreadable, tick-file write failure) is logged with its subject name and does NOT stop the pass or the loop. The next subject runs, and the next pass retries.
   - Per subject it calls `Tick(ctx, config, io.Discard, <log writer>, CardBuilder(config))` — stdout DISCARDED, stderr sent to `cfg.Logf` line by line, and the card builder passed so a daemon tick writes the card file exactly as a one-shot `--tick` does. On a change, log one line `sweep-gate <subject>: VERDICT change (<reason>)` — the daemon has no Monitor, so the change signal is the log line plus the tick file's mtime. Do not log the whole frame.
   - `Run` returns `ctx.Err()` when the context ends, and the caller treats `context.Canceled` as a clean stop.
   - The tick is sequential by construction: a pass that overruns the interval delays the next pass rather than starting a second one concurrently.

4. **`pkg/sweepgate/card.go` — the card builder the tick mode takes.** Add `func CardBuilder(cfg Config) CardFunc`, returning the `CardFunc` the tick mode takes (defined in the render prompt as `func(ctx context.Context, prepared Prepared) ([]byte, error)`), so `--tick` also writes the card file. It calls the invariant prompt's `InvariantPayload` with a stderr writer that goes to `cfg.Logf` line by line, never to the tick's stdout. A build failure is an error; the tick mode names it on stderr and keeps its verdict.

5. **`main.go` — the flag group.** Add a `sweepGateFlags` struct and `registerSweepGateFlags()`, in the same style as `registerFlaggedTickFlags`:
   - `-sweep-gate-mode` (string, default `""`) — one of `""`, `check`, `render`, `tick`, `invariant`. Empty means "do not run a one-shot mode".
   - `-sweep-gate-topic` (string, default `""`) — the single subject a one-shot mode runs against.
   - `-sweep-gate-vault` (string, default `""`) — the vault root. Empty disables every gate path with a log line; it is NEVER defaulted to a compiled-in path (AC7).
   - `-sweep-gate-topics` (string, default `""`) — comma-separated subjects the daemon tick covers.
   - `-sweep-gate-interval` (duration, default `pkg/sweepgate`'s `DefaultInterval`) — `0` disables the daemon tick.
   Build one `pkg/sweepgate.Config` from these with a small helper (`sweepGateConfigFromFlags`), splitting `-sweep-gate-topics` on commas and trimming whitespace.

6. **`main.go` — the one-shot dispatch, before the listener binds.** Immediately after the signal context is created and before the `fleetPusher.once` branch, add: when `-sweep-gate-mode` is non-empty, build the config, run the named mode against `os.Stdout` / `os.Stderr`, and `os.Exit(code)` with the mode's return value. An unknown mode value prints a usage line to stderr and exits 2. This branch must never construct the supervisor or bind the listener, so it can be invoked beside the live daemon — the same reason `--fleet-pusher-once` runs there.

7. **`main.go` — the goroutine.** Add a `startSweepGate(ctx context.Context, cfg pkg/sweepgate.Config)` function:
   - when `cfg.Interval <= 0` or `len(cfg.Subjects) == 0` or `cfg.Vault == ""`, log `sweep gate disabled (interval=<d> subjects=<n> vault=<empty|set>)` and return;
   - otherwise build the runner via `NewTickRunner`, run it, and log `sweep gate stopped: <err>` when it returns with a non-nil error while `ctx.Err() == nil`.
   In `main()`, start it with exactly this line, at line start with only leading whitespace:
   ```go
   go startSweepGate(ctx, sweepGateConfigFromFlags(sweepGate))
   ```
   The spec's AC9 greps `^[[:space:]]*go[[:space:]]+startSweepGate`, so the function name and the `go` statement shape are part of the contract.

8. **AC9 test — the wiring actually ticks.** `Describe("writes a tick file from the wiring", ...)`:
   - build a `pkg/sweepgate.Config` with `Vault` = `testdata/sweepgate/vault` (resolved relative to the package directory with `filepath.Join("..", "..", "testdata", ...)`), `Subjects` = the fixture subject, `StateDir` and `SettledDir` in `ginkgo.GinkgoT().TempDir()`, `Interval` = 50ms, `ReadTimeout` = 2s, and a fixed clock;
   - `go runner.Run(ctx)` under a context with a 5-second deadline, poll for `<StateDir>/<slug>.tick.txt` every 10ms, and fail if it does not appear within 2 seconds;
   - print, via `ginkgo.GinkgoWriter.Printf`, exactly `tick_file=%s mtime=%s` with the file's `os.Stat` mtime;
   - cancel the context and assert `Run` returns an error satisfying `errors.Is(err, context.Canceled)`.
   This is what distinguishes a real wiring from `go startSweepGate(...)` whose body returns immediately — the grep alone cannot.

9. **AC5 test — a hanging read is abandoned and the next tick still fires.** `Describe("cancels a hanging read", ...)` (verbatim, because the spec's verification greps for it):
   - construct `&TickRunner{Configs: []Config{{Vault: "unused", Subject: "Parity Topic", Source: blockingSnapshotter{}, ReadTimeout: 10 * time.Millisecond, Now: fixedClock, Logf: <collector>}}, Interval: 20 * time.Millisecond, Logf: <collector>}` directly — a `blockingSnapshotter` whose `Snapshot` waits on `<-ctx.Done()` and returns `ctx.Err()`;
   - let at least two tick passes elapse (each pass abandons its read at `ReadTimeout`), then cancel the outer context;
   - assert the collector saw at least two tick log lines from ONE `Run` call (the abandoned read did not wedge the loop) and that `Run` returns an error satisfying `errors.Is(err, context.Canceled)`;
   - print `err=%v` via `ginkgo.GinkgoWriter.Printf`.
   Use `errors.Is` from `github.com/bborbe/errors`.

10. **`CHANGELOG.md`.** Add one bullet under `## Unreleased`:
   `- feat: claude-wrapper owns the sweep gate — one flag group, four CLI modes with their own exit codes, and one daemon goroutine ticking every configured subject`
   Do not create a second `## Unreleased` heading.

11. **Self-check before finishing.** Re-run `<verification>` and confirm it passes. Walk requirements 1-10. Confirm the AC9 grep matches, and confirm the test fails if `startSweepGate`'s body is replaced with an immediate `return` (reason about it or try it, then restore).
</requirements>

<constraints>
- `main.go` keeps its shape: a flag block plus wiring. Add the new flag group through `registerSweepGateFlags` rather than inline, so `main` stays inside its length budget.
- No vault path is compiled in anywhere (AC7). An empty `-sweep-gate-vault` disables the gate and says so; it is never replaced by a default path.
- The one-shot mode branch runs before the supervisor is built and never binds the listener.
- The daemon tick is sequential; an overrun delays the next pass rather than stacking a second one.
- A failing subject never stops the pass or the loop; it is logged with its subject name.
- `github.com/bborbe/errors` only in `pkg/sweepgate`; `main.go` keeps its existing `errors` usage. Do not introduce `fmt.Errorf`.
- The clock is `libtime.CurrentDateTimeGetter`; the runner uses `Config.Now`.
- Ginkgo v2 + Gomega, in-package tests in `pkg/sweepgate`, qualified imports, no counterfeiter mocks.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Additional checks (all container-executable):
- `grep -nE '^[[:space:]]*go[[:space:]]+startSweepGate' main.go` — prints at least one matching line (AC9).
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='writes a tick file from the wiring' -ginkgo.v` — exits 0 and prints `tick_file=… mtime=…`.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='cancels a hanging read' -ginkgo.v` — exits 0 and prints `err=context canceled` (AC5).
- `! grep -q 'Obsidian/Personal' pkg/sweepgate/*.go main.go` — no hardcoded vault path (AC7).
- `go build ./...` — the binary still builds.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
</verification>
