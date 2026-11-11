---
status: draft
---

## Summary

- A flagged task, goal or topic is supposed to be reopened automatically once its session goes away.
- Closing such a session is reliable; reopening it is not, so flagged work silently goes dark.
- Measured 2026-11-11: of 28 eligible flagged tasks, 19 had no live session.
- The reopening step lives in a fleet round that mostly does not run.
- This change moves the reopening into this daemon as one periodic tick with no model in the path.

## Problem

Two halves decide whether a flagged row stays alive, and only one of them is dependable.

**The close half is deterministic.** launchd's `com.bborbe.idle-reaper` runs `idle-reaper-tick.py` every 900 s and closes idle headless workers. It always runs.

**The reopen half is a model step.** Fleet-drive row 5c (`commands/steps/flagged-open.md` → `supervisor:fleet-flagged-open-actor`) is executed by a model inside the fleet round. Across the 100 of 250 digests in `~/.claude/state/fleet-handback/` that mention the leg: **54 recorded `not run`, 46 recorded `unactioned`, and 0 recorded an open.** The one real open in the window is a separate `.flagged-open` hand-back (2026-11-11T08-53-19Z, `opened=8`).

A row's liveness is therefore decided by whichever half is more reliable, and that is always the closer. The measurement is direct: `flagged-rows.py --json` listed 28 eligible flagged tasks, **19 of them with no live session**.

`claude-wrapper` already holds every part a deterministic reopen needs and uses none of them for this:

- `pkg/tab_spawn.go`'s `TabSpawner` opens a WezTerm tab under the vault's `cc-*` launcher, resolving window, colour and launcher from the role map. `pkg/roster_mcp.go:358-372` already spawns through it, so this is a shipped path, not a new capability.
- `pkg/spawn_limits.go`'s `SpawnCaps` enforces the fleet's caps.
- `main.go`'s `startStartupSweeper` is the established shape for a periodic goroutine.

The gap is placement, not capability.

## Goal

The daemon runs one reopen tick per 15 minutes with no model in the path. Each tick reads the fleet's existing eligibility predicate, opens the eligible rows that are not live, and writes exactly one log line naming what it opened.

A tick evaluates each row in this order:

1. **Refused by the predicate** — the row's `refused` list is non-empty (`agent-set-flag` for a task whose `flag_set_by` is outside `OPENING_ACTORS`, `manager-row` for the branch partition) → counted refused.
2. **Blocked** — an unresolved `blocked_by` → skipped.
3. **Already live** — `liveness: LIVE` or `branch: jump` → skipped.
4. **Recently spawned** — the session's spawn-ledger row is under 60 minutes old → skipped. A session that has not yet registered reads ABSENT and would otherwise be opened twice.
5. **Refused by the caps** — `CheckWorker(ctx, row.Mode, false)` refuses → refused, and the tick stops opening further rows this tick. `row.Mode` is the predicate's own per-row `mode`; `operatorNamed` is `false` because the tick asserts nothing about who named the row.
6. **Refused by the rate limiter** — `ReserveStart(ctx)` refuses → refused, and the tick stops opening further rows this tick.

The tick owns exactly one cap of its own: **at most 50 rows opened per tick** — the operator ruling of 2026-11-07 (open item `e6576cc2`), not a fleet concurrency limit. It is a per-tick row budget, which is a different axis from `SpawnCaps`: those bound what is live and how fast sessions may start, while this bounds how much one tick does.

⚠️ The 50 is **not** `spawn.maxHeadless: 50`. On a tab spawn the resolved mode is `interactive`, so the mode axis that binds is `spawn.maxInteractive` (10), not `maxHeadless` — a tick relying on `maxHeadless` to enforce the ruling would never reach 50. The ruling is enforced by the tick's own budget.

`role: human` rows are ordered before `role: agent` and `role: manager` rows, so the row budget cannot displace an operator's row with a fleet one.

## Non-goals

- The close side (`idle-reaper-tick.py`, `reap-idle-headless.py`) — a sibling under the same goal.
- Porting the eligibility predicate to Go; the first iteration execs `flagged-rows.py`.
- Raising any spawn cap to open more rows per tick.
- Changing flag semantics or who may set a flag.
- Deleting the supervisor's row 5c (separate task, sequenced after this one is proven).

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0 — evidence: the exit code, quoted.

- [ ] **AC2** — **One table-driven test covers the whole decision rule, and its evidence is a mutation check.** Two runs of
      `go test -v -count=1 ./pkg -args -ginkgo.focus='decides whether a flagged row is reopened' -ginkgo.v`:

  1. exits 0 and prints one `case=<name> reason=<reason> opened=<bool>` line per case, with the expected `opened` per the table below;
  2. with `recentlySpawnedWindow` changed from `60 * time.Minute` to `0`, **exits non-zero with `[FAILED]` and still prints every `case=` line**. The verifier applies the mutation, runs, and restores with `git checkout -- pkg/flagged_tick.go`.

  A build failure does not satisfy the second run — a non-zero exit from a broken compile would let a fabricated-print test read as a passing mutation check. `-v -count=1` is required on both: without `-v` a passing package's output is swallowed, and without `-count=1` a cached result replays a pre-fix pass.

  | Case | Row | Opened? |
  |---|---|---|
  | `agent-set-flag` | task, `refused: ["agent-set-flag"]` | no |
  | `manager-row` | goal, `refused: ["manager-row"]` | no |
  | `blocked` | `blocked_by: ["[[X]]"]` | no |
  | `live` | `liveness: LIVE` | no |
  | `jump` | `liveness: ABSENT`, `branch: jump` | no |
  | `recently-spawned` | ABSENT, ledger row `spawned_at` = now − 5 min | no |
  | `cap-refused` | ABSENT, `CheckWorker` returns an error | no |
  | `rate-refused` | ABSENT, `ReserveStart` returns an error | no |
  | `eligible` | ABSENT, no ledger row, caps pass | yes |

- [ ] **AC3** — **The production source actually runs the predicate and parses its fields.** A test runs `ExecFlaggedRowsSource` against a checked-in fixture (`pkg/testdata/flagged-rows.json`, a real `flagged-rows.py --json` capture) via an injectable command runner, and asserts: the argv is exactly `<python> <script> --json`; `kind`, `vault`, `name`, `role`, `mode`, `session_id`, `liveness`, `branch`, `blocked_by` and `refused` all decode non-zero from the fixture; and a fixture whose JSON is truncated returns a wrapped error rather than an empty row list. Evidence: the recorded argv and the decoded row count, both printed by the focused test.

- [ ] **AC4** — **A human row is opened before both agent and manager rows.** A test drives the tick with three eligible ABSENT rows in the order `agent`, `manager`, `human`, and asserts the first three `TabSpawnRequest`s the recording spawner received carry the labels `human`, then `agent`/`manager` in their original relative order. Evidence: the recorded request order, printed by the focused test.

  ⚠️ This re-sort is required, not incidental: `flagged-rows.py:883` already sorts by `kind` with **topics and managers first**, so a tick that preserves the predicate's order would open managers ahead of a `role: human` row. A test that only compares agent against human passes while managers still jump the queue.

- [ ] **AC5** — **A cap or rate refusal stops the tick rather than being retried per row.** A test drives the tick with three eligible ABSENT rows where `CheckWorker` refuses on the first call, and asserts the recording spawner received **zero** calls and the result reports three refused entries. A second case does the same for `ReserveStart`. Evidence: the recording spawner's call count.

- [ ] **AC6** — **The tick never blocks the daemon on a slow predicate.** `flagged-rows.py` measured ~2 min against the real vaults. A test drives `Run` with a source whose `Rows` blocks until the context is cancelled, cancels after 100 ms, and asserts `Run` returns an error satisfying `errors.Is(err, context.Canceled)`. Evidence: the printed `err=` value and the focused test's exit 0.

- [ ] **AC7** — **The 50-row per-tick budget binds.** A test drives the tick with 60 eligible ABSENT rows and a caps fake that always passes, and asserts the recording spawner received exactly 50 calls, the result reports 50 opened, and the remaining 10 appear in `refused` carrying a `row budget` reason. Evidence: the recording spawner's call count and the result's refused count, both printed.

- [ ] **AC8** — **`CheckWorker` receives the row's own mode.** A test drives the tick over a headless row and an interactive row, and asserts the caps fake recorded `("headless", false)` and `("interactive", false)` respectively — never `""` and never a hard-coded mode. Evidence: the recorded argument pairs, printed.

- [ ] **AC9** — **The predicate contract is written down.** `docs/eligibility-predicate.md` exists and names each of the ten consumed fields (`kind`, `vault`, `name`, `role`, `mode`, `session_id`, `liveness`, `branch`, `blocked_by`, `refused`) with its meaning, plus the `OPENING_ACTORS = ("operator", "legacy")` rule and both `REFUSAL_REASONS` tokens (`agent-set-flag`, `manager-row`). Evidence: ten per-field `grep -c` lines, each non-zero, and four per-token lines, each non-zero — see `## Verification`.

- [ ] **AC10** — **A hanging predicate is abandoned and the next tick still fires.** A test drives `Run` with a source whose `Rows` never returns, asserts the first tick's line is written with a timeout reason within the tick timeout, and that a second tick line appears after the following interval. Evidence: two printed `flagged-tick` lines from one `Run` call.

- [ ] **AC12** — **`main.go` actually starts the goroutine.** `grep -n 'go startFlaggedTick' main.go` returns at least one match. Evidence: the grep output, quoted. This exists because an unwired tick is otherwise indistinguishable from a working one until AC11's 30-minute observation on the host — a 30-minute wait is not a usable gate for a one-line omission.

- [ ] **Post-Deploy (Rung-2):** AC11 — A real eligible flagged row reopens without a model in the path.

  - `deploy_check:` `go version -m ~/.local/bin/claude-wrapper | sed -n 's/.*vcs\.revision=\(.*\)/\1/p'`
  - `deploy_target:` `$(git rev-parse HEAD)`

  **Precondition:** choose a row whose spawn-ledger `spawned_at` is already more than 60 minutes old, so the recently-spawned guard does not apply to it.

  After `make install` and `launchctl kickstart -k gui/$UID/de.bborbe.claude-wrapper`, within **30 minutes** — the 15-minute tick interval plus up to 12.5 minutes of start-rate pacing (50 rows at `spawn.maxStartsPerMinute: 4`); the recently-spawned guard contributes nothing because the precondition excludes it — the daemon's log contains at least one line whose `rows=` names a session id that a `flagged-rows.py --json` reading taken afterwards reports as `liveness: LIVE` for that row. Evidence: the `flagged-tick` line quoted verbatim, the matching row quoted from the post-tick `--json` read, and the `deploy_check` revision matching `deploy_target`.

  ⚠️ This AC is the one the laziest implementation fails: a tick wired to a source that returns no rows logs `flagged-tick opened=0 refused=0 skipped=0 rows=` forever and satisfies every other AC.

## Verification

**Container rung** (runs in the dark-factory container and in CI):

- `make precommit` — exits 0.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='decides whether a flagged row is reopened' -ginkgo.v` — AC2, both runs.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='reads the predicate' -ginkgo.v` — AC3.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='orders a human row' -ginkgo.v` — AC4.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='stops on a cap refusal' -ginkgo.v` — AC5.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='cancels a slow predicate' -ginkgo.v` — AC6.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='bounds rows per tick' -ginkgo.v` — AC7.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='passes the row mode to the caps' -ginkgo.v` — AC8.
- `for f in kind vault name role mode session_id liveness branch blocked_by refused; do printf '%s ' "$f"; grep -c "$f" docs/eligibility-predicate.md; done` — AC9, ten lines each with a non-zero count.
- `for t in OPENING_ACTORS REFUSAL_REASONS agent-set-flag manager-row; do printf '%s ' "$t"; grep -c "$t" docs/eligibility-predicate.md; done` — AC9, four lines each with a non-zero count.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='abandons a hanging predicate' -ginkgo.v` — AC10.

**Operator rung** (needs the host, the launchd job and the real vaults):

- `make install` — builds, codesigns, installs to `~/.local/bin/claude-wrapper`.
- `launchctl kickstart -k gui/$UID/de.bborbe.claude-wrapper` — restarts the daemon.
- `grep -n 'go startFlaggedTick' main.go` — AC12, at least one match.
- AC11's 30-minute observation.

## Desired Behavior

1. The daemon writes one `flagged-tick` line per 15 minutes, and the line's counts account for every row the predicate returned.
2. A flagged row with no live session is reopened within one tick plus the pacing the caps impose.
3. A row the predicate refused, a blocked row, a live row and a just-spawned row are never opened.
4. A cap or rate refusal ends the tick's opening rather than being retried against every remaining row.
5. A predicate that hangs is abandoned after `predicateTimeout` (2 min), the tick still writes its line, and the next tick still runs.
6. No model is invoked anywhere in the path.

## Constraints

- **`claude-supervisor` is a read-only reference.** `scripts/flagged-rows.py` is exec'd, never modified. Neither that repo nor the marketplace copy is a modification target.
- **The predicate's contract is documented in this repo**, at `docs/eligibility-predicate.md` — which fields are consumed and what each means. The spec is not the contract's home. (Created by this change; AC9 verifies it.)
- **`TabSpawner` and `SpawnCaps` are consumed as-is.** No new interface duplicating either; the tick depends on the existing types.
- **`recentlySpawnedWindow` is a package-level named constant** (`60 * time.Minute`), not a struct field — AC2's mutation edits it. The tick interval, `maxRowsPerTick` and `predicateTimeout` (`2 * time.Minute`, just over the ~2 min the real predicate takes) are likewise named constants, not new config keys.
- **Existing goroutines are unchanged** — `startStartupSweeper`, the heartbeat poster, board delivery and the startup deadline sweeper keep their behaviour.
- **The 50-row per-tick budget is the tick's own**: `maxRowsPerTick = 50`, a package-level named constant. It is the operator's 2026-11-07 ruling (open item `e6576cc2`), not a config key, and deliberately not `spawn.maxHeadless`.
- **`CheckWorker` is called as `CheckWorker(ctx, row.Mode, false)`** — `row.Mode` is the predicate's own per-row `mode`, never an empty string (an empty mode takes no mode sub-cap at all) and never a hard-coded `headless` (a tab spawn resolves interactive). `operatorNamed` is `false`.
- **`ReserveStart(ctx)` is called before each spawn**, in addition to `CheckWorker`. Omitting it bypasses `spawn.maxStartsPerMinute` and opens tabs in one burst.

## Failure Modes

| Trigger | Expected | Recovery | Detection | Concurrency |
|---|---|---|---|---|
| `flagged-rows.py` missing, moved or renamed | Tick logs `flagged-tick failed: …`, daemon healthy | Fix the path | `flagged-tick failed` in stderr | — |
| Predicate JSON schema drifts | Decode error logged, no rows opened | Fix the decode | `decode flagged rows` in stderr | — |
| Predicate returns zero rows silently | `opened=0` forever | AC11 catches it; a live reading of `flagged-rows.py` shows rows | AC11 | — |
| Predicate takes ~2 min | Tick overlaps its own interval | Next tick waits on the goroutine; no unbounded growth | Tick line spacing > 15 min | Single goroutine, sequential ticks |
| `ReserveStart` paces slowly | Tick opens fewer rows and stops | Remaining rows go to the next tick | `refused=` count | — |
| Two daemons both tick | Duplicate tabs | Recently-spawned guard is the only defence (ledger-based, not a lock) | Duplicate `rows=` ids across daemons | ⚠️ Not fully prevented — single-daemon launchd job is the assumption |
| `spawned_at` clock skew | Guard fires early or late | None needed at minute granularity | — | — |
| `CheckWorker` refuses at the cap | Tick stops opening, logs refused | Rows wait for a slot | `refused=` count | — |

## Suggested Decomposition

`DB1`–`DB6` are `## Desired Behavior` items 1–6, in order.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `pkg/flagged_tick.go`: row type, decision rule, ordering, constants, tick loop — tests written red first | DB1, DB2, DB3, DB4, DB5, DB6 | AC2, AC4, AC5, AC6, AC7, AC8, AC10 | — |
| 2 | `pkg/flagged_rows_source.go` + `pkg/testdata/flagged-rows.json`: the exec source, its argv and decode | DB1 | AC3 | — |
| 3 | `docs/eligibility-predicate.md`: the cross-repo field contract | DB6 | AC9 | — |
| 4 | `main.go` wiring: one flag, one goroutine, the source and caps constructed from existing wiring | DB6 | AC1, AC12 | 1, 2 |

Prompt 4 is small and could ride with 1; it is separate only so the goroutine's start/stop is reviewed against `startStartupSweeper` rather than buried in a large diff. Prompt 1 is the bulk and the only one whose tests are adversarial (AC2 and AC5 are the two that would pass on a stub).

## Do-Nothing Option

Nothing changes: the close half keeps winning, the 19 of 28 flagged rows stay dark, and the fleet silently shrinks while every dashboard reports it as healthy — because from each reader's point of view a dark row looks exactly like a finished one.

The alternative — keeping the reopen leg as a model step in fleet row 5c — is what the measurement already rules out: 0 opens across 100 digests. The cost of this change is one goroutine and ~300 lines; the cost of not making it is the flag mechanism becoming decorative.
