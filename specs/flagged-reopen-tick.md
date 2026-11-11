---
status: draft
kind: feature
---

## Summary

- Flagged tasks, goals and topics go dark because the close side is deterministic and the reopen side is not.
- launchd `com.bborbe.idle-reaper` (`idle-reaper-tick.py`, 900 s) closes idle headless workers; nothing reopens them without a model.
- Measured 2026-11-11: `flagged-rows.py --json` listed 28 eligible flagged tasks, **19 with `liveness: ABSENT`**.
- The reopen leg is fleet-drive row 5c, a model step: across the 100 of 250 fleet digests that mention it, **54 were `not run`, 46 `unactioned`, and 0 logged `opened=`**.
- The fix is one model-free ticker in this daemon that reopens eligible flagged rows, so a flagged row cannot be closed without being reopened.

## Problem

`claude-wrapper` already holds every piece a deterministic reopen needs, and uses none of them for this:

- `pkg/tab_spawn.go`'s `TabSpawner` opens a WezTerm tab under the vault's `cc-*` launcher, resolving window, colour and launcher from the role map. It is what the roster's `spawn_agent` already calls, so a spawn from this daemon is an existing, shipped path rather than a new capability.
- `pkg/spawn_limits.go`'s `SpawnCaps` enforces `spawn.maxConcurrent` (60 soft / 90 hard), `spawn.maxManagers` (12) and `spawn.maxStartsPerMinute` (4).
- `main.go`'s `startStartupSweeper` is the established shape for a periodic goroutine: a ticker, a `ctx.Done()` arm, and one `log.Printf` per action.

Meanwhile the eligibility predicate lives outside this daemon entirely, in `claude-supervisor`'s `scripts/flagged-rows.py` — ~900 lines of vault parsing that already answers, per row, `liveness`, `branch`, `blocked_by` and `refused` (including the task-only `flag_set_by` provenance rule, `OPENING_ACTORS = ("operator", "legacy")`).

The gap is not capability but **placement**: the reopen decision is made by a model in a fleet round that mostly does not run, while the close decision is made by a launchd job that always runs. A row's liveness is therefore determined by whichever of the two is more reliable, and that is always the closer.

## Goal

The daemon runs one reopen tick per 15 minutes with no model in the path. Each tick reads the existing predicate, opens the eligible rows that are not live, and writes exactly one log line naming what it opened.

Guards, in the order they are evaluated:

1. a row the predicate already refused (task `flag_set_by` outside `OPENING_ACTORS`) is refused;
2. a row with an unresolved `blocked_by` is skipped;
3. a `LIVE` row (or `branch: jump`) is skipped;
4. a row whose session was spawned less than 60 minutes ago is skipped — a session that has not yet registered reads ABSENT and would otherwise be opened twice;
5. past the operator's hard cap of 50 per tick, rows are refused;
6. `SpawnCaps.CheckWorker` is consulted before each spawn and a refusal is logged, never retried in a loop.

Direct (`role: human`) rows are ordered before agent rows, so the cap cannot displace an operator's row with a fleet one.

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0 — evidence: the exit code, quoted.
- [ ] **AC2** — **The tick writes one line per tick in the documented format.** `grep -c 'flagged-tick opened=' ~/Library/Logs/claude-wrapper/stderr.log` counts at least three lines within 45 minutes of the daemon restarting, and every line matches `^flagged-tick opened=[0-9]+ refused=[0-9]+ skipped=[0-9]+ rows=`. A line with `opened>=1` carries a non-empty `rows=` and a line with `opened=0` carries `rows=` empty — both asserted from the same three lines.
- [ ] **AC3** — **One table-driven test covers the whole decision rule, and its evidence is a mutation check.** Two runs of
      `go test -v -count=1 ./pkg -args -ginkgo.focus='decides whether a flagged row is reopened' -ginkgo.v`:
  1. exits 0 and prints one `case=<name> reason=<reason> opened=<bool>` line per case, with the expected `opened` per the table below;
  2. with the recently-spawned guard's window set to zero, **exits non-zero with `[FAILED]` and still prints every `case=` line**. The verifier applies the mutation, runs, and restores with `git checkout -- pkg/flagged_tick.go`.

  A build failure does not satisfy the second run — a non-zero exit from a broken compile would let a fabricated-print test read as a passing mutation check. `-v -count=1` is required on both: without `-v` a passing package's output is swallowed, and without `-count=1` a cached result replays a pre-fix pass.

  | Case | Row | Opened? |
  |---|---|---|
  | `agent-set-flag` | task, `refused: ["agent-set-flag"]` | no |
  | `blocked` | `blocked_by: ["[[X]]"]` | no |
  | `live` | `liveness: LIVE` | no |
  | `recently-spawned` | ABSENT, ledger row `spawned_at` = now − 5 min | no |
  | `eligible` | ABSENT, no ledger row | yes |

- [ ] **AC4** — **The hard cap is a real ceiling, not a comment.** A table-driven test drives the tick with 60 eligible rows and a cap of 50, and asserts the recording spawner received exactly 50 calls and that the result reports `opened=50` with the remaining 10 in `refused` carrying `hard cap reached`. Evidence is the recording spawner's call count, not the result struct alone.
- [ ] **AC5** — **Direct rows are ordered before agent rows.** A test drives the tick with a cap of 1 and two eligible ABSENT rows — an agent row listed first and a `role: human` row listed second — and asserts the single spawn the recording spawner received carries the **human** row's label. Evidence is the recorded `TabSpawnRequest.Label`.
- [ ] **AC6** — **The tick never blocks the daemon on a slow predicate.** `flagged-rows.py` measured ~2 min against the real vaults. A test drives `Run` with a source whose `Rows` blocks until the context is cancelled, cancels after 100 ms, and asserts `Run` returns `context.Canceled` rather than hanging — evidence: the test completing inside its Ginkgo suite timeout.

## Out of Scope

- The close side (`idle-reaper-tick.py`, `reap-idle-headless.py`) — a sibling under the same goal.
- Porting the eligibility predicate to Go; the first iteration execs `flagged-rows.py`.
- Raising any spawn cap to open more rows per tick.
- Changing flag semantics or who may set a flag.
