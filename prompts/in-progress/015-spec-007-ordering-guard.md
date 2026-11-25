---
status: approved
spec: [007-bug-flagged-tick-reopens-a-stopped-row]
created: "2026-11-25T07:30:00Z"
queued: "2026-11-25T06:43:14Z"
branch: dark-factory/bug-flagged-tick-reopens-a-stopped-row
---

<summary>
- The fleet tick reopens rows the operator deliberately stood down, and no log line says why.
- This step makes the reopen decision honour the stand-down: a row whose stop instant is newer than its flag instant is skipped.
- The rule is ordering, not presence: a deliberate re-flag after a stop still opens the row.
- The skip gets its own reason token so the tick line names it instead of reporting an ordinary open.
- A stopped row with no readable stop instant, or no readable flag instant, is skipped — absent evidence never reopens a retired row.
- The stand-down check runs before the liveness guards, so a stopped row reports the stand-down rather than a derived verdict.
- A single table-driven test covers all seven ordering cases, and it is shaped so the spec's mutation check flips the two load-bearing cases.
- Rows without the marker behave exactly as before; the existing reopen-decision table does not move.
</summary>

<objective>
Teach `decision()` in `pkg/flagged_tick.go` to skip a stopped row unless the operator flagged it again after the stop. The comparison is between the two parsed instants carried on the row; a nil instant is fail-closed. The skip carries a new `reasonStopped` token so the tick line's `skipped_by_reason=` names it. This is the rule half of spec 007 and cannot compile without prompt 1's fields.
</objective>

<context>
Read these before writing:

- `pkg/flagged_tick.go` — the block of `reason*` constants (add `reasonStopped` there); `decision()` (add the guard); `classify()` and `classifySkip()` (a returned skip reason flows straight into `result.Skipped`, so no log change is needed); `formatCounts` / `reasonClass` / `Detail()` (they already group `key: stopped` into `stopped:N` and render it on the detail line).
- `pkg/flagged_rows_source.go` — the `Stopped`, `StopDate` and `FlagSetAt` fields added by prompt 1. `StopDate` / `FlagSetAt` are `*time.Time`; nil means absent or unparseable.
- `pkg/flagged_tick_test.go` — the existing `ginkgo.Describe("decides whether a flagged row is reopened", ...)` table (its helper calls `decision(row, recent, nil, nil, now, neverOpened(row), false)` and prints `case=%s reason=%s opened=%t\n`). Add the new table near it and do not modify the existing one.
- `docs/eligibility-predicate.md` § *Fields the tick consumes* — the field names you are consuming (do not edit it here; prompt 3 does).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo `DescribeTable` conventions.
</context>

<requirements>
1. **Add the skip reason.** In the `reason*` constant block in `pkg/flagged_tick.go`, add:

   ```go
   // reasonStopped marks a row the operator deliberately stood down: its
   // stop_date is newer than its flag_set_at, so the stand-down outranks the
   // reopen. A nil stamp is fail-closed.
   reasonStopped = "stopped"
   ```

   No `Line()` or `Detail()` change is required: a skip appends `row.Key()+": "+reason` to `result.Skipped`, `formatCounts` renders `stopped:N`, and `Detail()` lists the row. Confirm this by reading those functions; do not add a new summary field.

2. **Add the ordering guard to `decision()`.** Place it immediately after the `BlockedBy` check and before the `if row.Liveness != "ABSENT" && !fresh` liveness block, so it runs ahead of every derived verdict (`not-absent`, `already-live`, `recently-spawned`):

   ```go
   // The operator's stand-down outranks every derived state. A row whose
   // stop_date is newer than its flag_set_at was stopped after it was flagged and
   // must not be reopened; only a flag strictly newer than the stop reopens it.
   // A nil stamp (absent or unparseable) is fail-closed — absent evidence never
   // reopens a retired row.
   if row.Stopped {
       if row.StopDate == nil || row.FlagSetAt == nil || !row.FlagSetAt.After(*row.StopDate) {
           return reasonStopped, false, true
       }
   }
   ```

   - Skip when `stop_date >= flag_set_at`; open only when `flag_set_at` is strictly newer. Equal timestamps are not "newer", but fail-closed still skips them.
   - Keep the ordering comparison in exactly one `After` call. The spec's mutation check swaps the two `After` operands, and that swap must flip `stopped-after-flag` to open and `flag-after-stop` to skip. Do not scatter the comparison across branches.

3. **Keep `decision()` the single home of the rule.** The source populates fields and decides nothing; do not duplicate the guard in `classify()` or anywhere else.

4. **Do not move any existing reopen case.** Leave every entry in the existing `decides whether a flagged row is reopened` table unchanged (AC5). Rows without `Stopped` set are inert under the new guard.

5. **Add the ordering table test.** Add a new top-level `ginkgo.Describe("skips a stopped row only when the stop is newer than the flag", ...)` in `pkg/flagged_tick_test.go` containing a `ginkgo.DescribeTable`. For each entry call `decision(row, nil, nil, nil, now, false, false)` and print

   ```
   case=%s reason=%s opened=%t\n
   ```

   with `opened = !refused && !skipped`, exactly as the existing table does, BEFORE asserting. Build each `FlaggedRow` with `Kind: "task"` and `Liveness: "ABSENT"` plus the marker fields; parse the literal stamps with `time.Parse(time.RFC3339, ...)` and take their address. Assert `opened == wantOpened` and, when not opened, `reason == reasonStopped`.

   | Case name | `Stopped` | `StopDate` | `FlagSetAt` | Opened? | Reason |
   |---|---|---|---|---|---|
   | `stopped-after-flag` | true | `2026-11-24T18:05:33+01:00` | `2026-11-24T17:10:00+01:00` | false | `stopped` |
   | `flag-after-stop` | true | `2026-11-24T18:05:33+01:00` | `2026-11-24T19:36:58+01:00` | true | — |
   | `no-stop-date` | true | nil | `2026-11-24T19:36:58+01:00` | false | `stopped` |
   | `unparseable-flag-instant` | true | `2026-11-24T18:05:33+01:00` | nil | false | `stopped` |
   | `unparseable-stop-date` | true | nil | `2026-11-24T19:36:58+01:00` | false | `stopped` |
   | `not-stopped` | false | nil | `2026-11-24T19:36:58+01:00` | true | — |
   | `stopped-false` | false | nil | `2026-11-24T19:36:58+01:00` | true | — |

   `flag-after-stop` is the load-bearing case: it is the operator's deliberate re-flag, and a presence-only guard fails it while passing every other row. At the decision boundary `no-stop-date` and `unparseable-stop-date` are the same shape (nil `StopDate`); keep both named entries because the AC table lists them, and because prompt 1's source test distinguishes the real-world triggers.

6. **Self-check before finishing:** re-run `<verification>`, confirm it passes, and walk each item above against the change.
</requirements>

<constraints>
- **The stopped guard runs before the liveness guards.** A stopped row reports `stopped`, not `not-absent`, `already-live` or `recently-spawned` — the operator's stand-down outranks a derived state, and that is what makes the skip observable in the runtime AC.
- **No presence-only guard.** Every read of `Stopped` is paired with the timestamp comparison; the no-date fallback is a skip, not an open. A bare `if row.Stopped { skip }` is the defect this spec exists to prevent.
- **Fail-closed.** A `stopped` row with a nil `StopDate` or a nil `FlagSetAt` is skipped.
- **The flag instant is the raw `flag_set_at`, never an `approved_at` fallback.**
- **The existing reopen cases must not move.** Every row in the existing case set keeps its verdict.
- Do NOT commit — dark-factory handles git.
- Do NOT edit `docs/eligibility-predicate.md` (prompt 3) and do NOT add a CHANGELOG entry (spec 007 has no CHANGELOG acceptance criterion).
- Existing tests must still pass unchanged.
</constraints>

<verification>
Run inside the container:

```
make precommit
go test -v -count=1 ./pkg -args -ginkgo.focus='skips a stopped row only when the stop is newer than the flag' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='decides whether a flagged row is reopened' -ginkgo.v
```

Expected: `make precommit` exits 0. The first focused run exits 0 and prints seven `case=<name> reason=<reason> opened=<bool>` lines, with `opened=false` for `stopped-after-flag`, `no-stop-date`, `unparseable-flag-instant`, `unparseable-stop-date` and `opened=true` for `flag-after-stop`, `not-stopped`, `stopped-false`. The second focused run (the pre-existing reopen table) still exits 0 with the same verdicts as before.
</verification>

## Acceptance criteria mapping

| AC | Requirement |
|---|---|
| AC1 `make precommit` exits 0 | Verification |
| AC2 one table-driven ordering test, mutation-shaped | Req 2, 5; Verification |
| AC5 no row without a marker changes behaviour | Req 4; second focused run in Verification |
