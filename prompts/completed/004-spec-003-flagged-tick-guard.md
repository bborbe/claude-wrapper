---
status: completed
spec: [003-parked-observable-reopen-gate]
summary: Added a Parked gate to the reopen tick that holds rows parked on cards, PRs, hosts or clocks as skips, wired it in the daemon, and proved all four classes plus fail-closed and no-reservation behaviour end-to-end.
execution_id: claude-wrapper-parked-observable-exec-004-spec-003-flagged-tick-guard
dark-factory-version: v0.198.17
created: "2026-11-17T12:00:00Z"
queued: "2026-11-17T20:40:52Z"
started: "2026-11-17T23:40:12Z"
completed: "2026-11-17T23:46:36Z"
---

# Hold a parked row in the reopen tick

<summary>
- The reopen tick asks the park gate before it spends a cap check or a rate token.
- A held row is a skip, not a refusal, so the tick keeps going and the row still counts in the totals.
- A held row does not consume the per-tick row budget, so it cannot displace a row that genuinely needs reopening.
- The gate is switched on by one field in the daemon's wiring, and an absent gate changes nothing.
- Every existing guard keeps its position and its reason string.
- A row parked on a card, a pull request, a host or a clock is reopened within one tick of its observable changing.
- A probe that cannot be read holds the row instead of reopening it.

</summary>

<objective>
Put the park gate in the reopen decision so a flagged row whose last session deliberately parked on an external observable is not resumed every 15 minutes. This is the prompt that makes the change real: it adds the guard to the tick, wires it in the daemon, and proves each class end-to-end through the tick's own buckets and log line. AC13 and AC14 (the overnight observation and the release) are spec-level verification and are not part of this prompt.
</objective>

<context>
Read these before writing anything:

- `pkg/flagged_tick.go` — `FlaggedTick`, `tickFromRows`, `decision`, `reasonClass`, `FlaggedTickResult.Line()` / `Detail()`, and the existing guards' ordering comments.
- `pkg/parked_gate.go` (prompt 3) — `ParkedHold`, `NewParkedGate`, `StateDirs`.
- `pkg/parked_closer.go` (prompt 1) and `pkg/parked_probe.go` (prompt 2).
- `pkg/flagged_tick_test.go` — `recordingReopener`, `recordingCaps` (`modes()` records each `CheckWorker` call), `recordingRunner`, `staticSource`, `ledgerWith`, `fixedNow`, `rowWith`, `newTick`.
- `main.go` — `startFlaggedTick` (its tick literal and its `if interval <= 0` early return), `startFleetFaces` (the single caller of `startFlaggedTick`), `openCardsFromURL`, `run()`'s `now` and `store := pkg.NewSessionStore(*stateDir)`.
- `pkg/sessionstore.go` — `sessionEventsName`, and the state-dir chain `StateDirs` resolves.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. In `pkg/flagged_tick.go`, add one field to `FlaggedTick`:

   ```go
   // Parked is the park gate. A nil gate is inactive, which is what a test that
   // predates it gets and what the daemon gets when the state directories cannot
   // be resolved.
   Parked ParkedHold
   ```

   The field name `Parked` is frozen: the daemon's wiring is verified by grepping `main.go` for it.

2. In `tickFromRows`, insert the guard **after** the `decision(...)` block and **before** `t.Caps.CheckWorker(...)`:

   ```go
   if t.Parked != nil {
       if reason, held := t.Parked.Hold(ctx, row); held {
           result.Skipped = append(result.Skipped, row.Key()+": "+reason)
           continue
       }
   }
   ```

   A hold must:
   - be recorded in `result.Skipped`, never in `result.Refused`;
   - not set `halted`, so later rows are still considered;
   - not touch `len(result.Opened)`, so the `maxRowsPerTick` budget is unaffected;
   - not call `CheckWorker` or `ReserveStart`.

   The reason arrives already shaped as `class` or `class: cause`, so `reasonClass` cuts it into the same class count as the other skip reasons. Do not re-format it.

3. In `main.go`, add one helper next to `openCardsFromURL`:

   ```go
   // newParkedGate builds the flagged tick's park gate, or nil when the state
   // directories cannot be resolved — a nil gate is an inactive gate.
   func newParkedGate(
       stateDir string,
       attentionURL string,
       now libtime.CurrentDateTimeGetter,
   ) pkg.ParkedHold {
       sessionsDir, parkedDir, err := pkg.StateDirs(stateDir)
       if err != nil {
           log.Printf("WARNING: parked gate disabled: %v", err)
           return nil
       }
       return pkg.NewParkedGate(
           sessionsDir, parkedDir, openCardsFromURL(attentionURL), pkg.ExecCommandRunner{}, now,
       )
   }
   ```

   `openCardsFromURL` already returns nil for an empty URL; the card probe fails closed on a nil reader, so an unconfigured attention store holds card parks rather than opening them.

4. Wire it through the two existing entry points:

   - In `run()`, after `store := pkg.NewSessionStore(*stateDir)` and the `now := libtime.NewCurrentDateTime()` it depends on, build `parkedGate := newParkedGate(*stateDir, *attentionURL, now)`.
   - Add `parked pkg.ParkedHold` as the **last** parameter of `startFleetFaces` and pass `parkedGate` at its single call site in `run()`.
   - Add `parked pkg.ParkedHold` as the **last** parameter of `startFlaggedTick`, pass it from `startFleetFaces`, and set `Parked: parked,` inside the tick literal in `startFlaggedTick`. The tick literal must carry a line matching `^[[:space:]]*Parked:`.

   Both functions are unexported in package `main` and each has exactly one call site; update both. Do not change any other wiring.

5. Extend `pkg/flagged_tick_test.go` with a helper that builds a tick wired to the **real** gate over real temp directories, plus a fake `OpenCardReader` and a mutable `recordingRunner`. The helper writes a real `events.jsonl` for the row's session id under the temp sessions directory and returns the tick. Name the fake card reader so it cannot collide with existing fakes, and give it a settable result and error.

6. Add these five specs to `pkg/flagged_tick_test.go`. The `It` descriptions are load-bearing: the spec's verification greps them.

   - `ginkgo.It("holds a row parked on an unanswered card until the card is answered", ...)` — one ABSENT row with a UUID session id, a closer carrying `⏰ Ends: card 3f9a1c7e answered`, and a fake card reader reporting that session id as owning a card on the first tick and not on the second. Drive the same tick object twice and print `fmt.Printf("tick=%d %s\n", n, result.Line())`. Tick 1 must show `opened=0` and `skipped_by_reason=parked-on-card:1`; tick 2 must show `opened=1`.
   - `ginkgo.It("holds a row parked on a pull request until the pull request changes", ...)` — three ticks against a closer carrying `⏰ Ends: bborbe/claude-wrapper#412 merges`: tick 1 records the baseline and holds; tick 2 returns the same `headRefOid`, `reviewDecision` and `state` and holds; tick 3 returns a new `headRefOid` and opens. Print the three tick lines plus `argv=%s`. The recorded argv, after the binary name, must be exactly `pr view 412 --repo bborbe/claude-wrapper --json headRefOid,reviewDecision,state`. Then repeat the three ticks for a second row whose closer names `bborbe/other-repo#9` and assert the recorded argv is exactly `pr view 9 --repo bborbe/other-repo --json headRefOid,reviewDecision,state`. A `state` of `MERGED` or `CLOSED` must count as satisfied on its own.
   - `ginkgo.It("resolves a host and a clock park", ...)` — four cases, each printed as `case=<name> opened=<bool> skipped=<reason>`: an unreachable host (a port taken from a listener that was opened and then closed) is held with `parked-on-host`; a reachable host (a real `net.Listener` on `127.0.0.1:0`) is opened; a clock one hour in the future is held with `parked-until`; a clock in the past is opened.
   - `ginkgo.It("fails closed on an unreadable probe", ...)` — two cases in one spec: a card reader returning an error, and a command runner returning an error for `gh`. Each asserts zero reopen calls and a skip whose reason is the class. Print `reopens=%d skipped=%s` and assert both name `parked-on-card` and `parked-on-pr` respectively.
   - `ginkgo.It("skips a parked row without spending a reservation", ...)` — one parked row followed by one eligible row, with a stub `ParkedHold` that holds only the first row's key. Print `result.Line()` and assert it contains `opened=1 refused=0 skipped=1 considered=2`, that the opened key is the second row's, and that `recordingCaps.modes()` has exactly one entry. A hold that halted the tick or consumed a reservation would show `opened=0` or two entries.

7. Do not modify any existing spec in `pkg/flagged_tick_test.go`, and do not change any existing guard's reason string or position. `recentlySpawnedWindow` keeps its mutation-check comment and its value.

8. Add the CHANGELOG entry under a new `## Unreleased` heading at the top of `CHANGELOG.md`, above `## v0.30.0`, in the file's existing long-form style: one `- fix:` bullet stating that a flagged row whose last session parked on a card, a PR, a host or a clock is no longer reopened every tick, that a declaration this build cannot probe expires after one hour, that a probe that cannot be read holds the row, and that a hold is a skip rather than a refusal.

9. Before finishing, re-run `<verification>` and confirm it passes, then walk each numbered requirement against the code you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- **The park gate is a `Parked` field on the tick, set in `startFlaggedTick`'s tick literal.** That field name is frozen; a nil gate means the gate is inactive.
- **A hold is a skip**: it does not spend a reservation, does not end the tick's opening pass, and does not count toward the row budget.
- **The PR probe's argv is frozen**: `pr view <N> --repo <owner>/<repo> --json headRefOid,reviewDecision,state`, derived from the declaration and never hardcoded.
- **Every existing guard keeps its position and its reason string.** Do not reorder `decision`, the row budget, `CheckWorker`, `ReserveStart` or the reopener.
- Do NOT change the 15-minute interval, the 50-row per-tick budget, `recentlySpawnedWindow`, `predicateTimeout`, or any spawn cap.
- Do NOT add a config key or flag that disables the park gate, and do NOT add a per-class tunable timeout. `MAX_AGE_S`, the probe timeout and the dial timeout are the only bounds, and all three are constants.
- Do NOT post, answer or read any card beyond the existing open-card read.
- Do NOT touch `claude-supervisor`'s `scripts/closer-verdict.py`, `scripts/who-needs-me.py` or `scripts/declared-wait.py`.
- Do NOT change `flagged-rows.py`, its eligibility predicate, or the fields it emits.
- The CHANGELOG entry goes under a new `## Unreleased` heading. Do not edit a released section.
- Repo conventions: `github.com/bborbe/errors` wrapping (no `fmt.Errorf`, no bare `return err`), `ctx` threaded through every call, `libtime.CurrentDateTimeGetter` for the clock, GoDoc on every exported item.
- Tests are in-package ginkgo specs; import `ginkgo`/`gomega` qualified, never dot-imported. Real temp directories, a real `events.jsonl` and a real `net.Listener`; only the card reader and the `gh` runner are fakes.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then run the five focused specs and confirm exit 0 plus the printed evidence lines:

```
go test -v -count=1 ./pkg -args -ginkgo.focus='holds a row parked on an unanswered card' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='holds a row parked on a pull request' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='resolves a host and a clock park' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='fails closed on an unreadable probe' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='skips a parked row without spending a reservation' -ginkgo.v
```

Then confirm the gate is wired in the daemon:

```
grep -nE '^[[:space:]]*Parked:' main.go
```

It must print at least one line.
</verification>
