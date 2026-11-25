---
status: draft
kind: bug
---

## Summary

- The fleet tick reopens rows the operator has deliberately stopped.
- `/supervisor:stop` writes `stopped: true` + `stop_date` onto the task page; the tick reads neither field.
- The rule must be **ordering, not presence**: a row is skipped only when its `stop_date` is newer than its `flag_set_at`, so a deliberate re-flag after a stop still opens.
- Measured 2026-11-24 on one row: flag cleared 17:10, stopped 18:05:33, deliberately re-flagged 19:36:58. A presence-only guard would ignore the operator's newest mark on exactly that row.
- The row source already serves the page's whole frontmatter map, so this is a single-repo change.

## Problem

A row opens only when two facts hold: the operator flagged it, and the operator has not since stopped it. The tick checks the first and never asks the second.

`scripts/task-standdown.py` (claude-supervisor) stamps a task page with `stopped: true` and `stop_date` when `/supervisor:stop` retires a row. Its own docstring states the pair is what readers gate on. Two readers in that repo honour the marker, presence-only, and are out of scope here. The wrapper's tick has no read of it at all: `FlaggedRow` carries no stopped field, and `decision()`'s reason constants (`pkg/flagged_tick.go:132-176` on `2b6bc5f`) contain no `reasonStopped`.

The row source is not the obstacle. `main.go:1414` wires `pkg.VaultAPIFlaggedRowsSource`, and `rowForItem` in `pkg/flagged_rows_vaultapi.go` already pulls `role`, `mode`, `flag_set_by`, `defer_date`, `blocked_by`, `managed_by` and `goals` out of the task record endpoint's frontmatter map. `stopped`, `stop_date` and `flag_set_at` arrive in that same map today; nothing reads them.

The consequence is asymmetric and silent. A stopped row reopens on the next tick, and no line says why — from the tick's point of view the row was simply eligible.

## Goal

A row the operator stopped is never reopened by the tick unless the operator has flagged it again **after** the stop, and the tick line names the skip. The decision sees both stamps as instants and the marker as a boolean, both sourced from the page's own frontmatter; nothing else about the reopen decision changes.

## Reproduction

1. Take a task page carrying all three marks:

   ```yaml
   flag: true
   flag_set_at: 2026-11-24T19:36:58.408271+01:00
   flag_set_by: operator
   stopped: "true"
   stop_date: "2026-11-24T18:05:33+01:00"
   ```

   This is the measured state of `25 Tasks/Classify the Heat-Pump Penetration Seal.md` at commit `f1012cb7ee` (2026-11-24 19:37:49).

2. Run one tick against that vault.

3. Observed: the row is opened. `grep -nE '\.(Stopped|StopDate|FlagSetAt)\b' pkg/flagged_tick.go` returns 0 lines; `decision()` has no branch that can produce a skip for it. Observed on the wrapper at `2b6bc5f` (release v0.66.1).

The same page also demonstrates the counter-case this fix must not regress: at `f1012cb7ee` the flag is **newer** than the stop, and the operator intended the row to open.

## Expected vs Actual

| | |
|---|---|
| **Expected** | A row whose `stop_date` is newer than its `flag_set_at` is skipped with its own reason. A row whose `flag_set_at` is newer than its `stop_date` opens. A row with `stopped: true` but no readable `stop_date` — or no readable `flag_set_at` — is **skipped**: absent evidence never reopens a retired row. |
| **Actual** | Neither field is read. Every flagged, open, due row opens, stopped or not. |

## Why this is a bug

`docs/eligibility-predicate.md` § *Fields the tick consumes* is the contract for what the tick reads, and it exists precisely so a field the source serves but the tick ignores is a visible gap rather than a silent one. `/supervisor:stop` is documented as a stand-down, not a close: the row keeps its status and its flag, and the marker is the only record that the operator ended it. A tick that reopens it makes the marker decorative.

## Workaround

Until this ships, retire a row by clearing its flag as well as stopping it: `vault-cli task clear '<task>' flag`. The tick opens only flagged rows, so a cleared flag removes the row from the eligible set whatever the tick does with the marker.

## Non-goals

- A presence-only guard. The whole point is the ordering comparison; a bare `if stopped` is the defect this spec exists to prevent.
- Any change to goal or topic rows: only a task carries the marker, and only the task record endpoint serves frontmatter.
- Any change in `claude-supervisor` or `vault-cli` — the fields already arrive, and those two repos' own presence-only readers are a separate follow-up.
- Changing what `/supervisor:stop` writes.
- Reopening the completed task that scoped the marker to `open-queue.py` and `worker-keepalive.py`.

## Do-Nothing Option

The marker stays decorative. A row the operator stopped reopens on the next tick, silently — the tick line records it as an ordinary open, so nothing distinguishes a deliberate stand-down from a live row. The cost compounds: every stopped-but-flagged row becomes a session the operator has to stop again, and each stop is undone by the next tick. The workaround (clear the flag as well) is a manual step every operator must remember, and forgetting it produces exactly this bug.

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0 — evidence: the exit code, quoted.

- [ ] **AC2** — **One table-driven test covers the whole ordering rule, and its evidence is a mutation check.** Two runs of
      `go test -v -count=1 ./pkg -args -ginkgo.focus='skips a stopped row only when the stop is newer than the flag' -ginkgo.v`:

  1. exits 0 and prints one `case=<name> reason=<reason> opened=<bool>` line per case, with the expected `opened` per the table below;
  2. with the ordering comparison inverted (swap the `After` operands in the guard), **exits non-zero with `[FAILED]` and still prints every `case=` line**. The fix is committed before this run; the verifier restores with `git checkout -- pkg/flagged_tick.go`, which would otherwise discard an uncommitted fix.

  A build failure does not satisfy the second run — a non-zero exit from a broken compile would let a fabricated-print test read as a passing mutation check. `-v -count=1` is required on both: without `-v` a passing package's output is swallowed, and without `-count=1` a cached result replays a pre-fix pass.

  | Case | Row marks | Opened? |
  |---|---|---|
  | `stopped-after-flag` | `stopped: true`, `stop_date` 18:05, `flag_set_at` 17:10 | no |
  | `flag-after-stop` | `stopped: true`, `stop_date` 18:05, `flag_set_at` 19:36 | yes |
  | `no-stop-date` | `stopped: true`, `flag_set_at` 19:36, no `stop_date` | no |
  | `unparseable-flag-instant` | `stopped: true`, `stop_date` 18:05, `flag_set_at: not-a-time` | no |
  | `unparseable-stop-date` | `stopped: true`, `stop_date: not-a-date`, `flag_set_at` 19:36 | no |
  | `not-stopped` | `flag_set_at` 19:36, no `stopped` | yes |
  | `stopped-false` | `stopped: false`, `flag_set_at` 19:36 | yes |

  `flag-after-stop` is the load-bearing case: it is the operator's deliberate re-flag, and a presence-only guard fails it while passing every other row.

- [ ] **AC3** — **The decision receives the marker and both stamps from the record frontmatter.** `go test -v -count=1 ./pkg -args -ginkgo.focus='populates the stand-down marker from the record frontmatter' -ginkgo.v` exits 0 and prints `stopped=true stop_date=2026-11-24T18:05:33+01:00 flag_set_at=2026-11-24T19:36:58.408271+01:00` for a task whose record page carries those three keys, `stopped=true` for a page whose marker is an **unquoted** YAML bool (`stopped: true`) — the live pages use the quoted form and `/supervisor:stop` writes through vault-cli, so both shapes occur — and `stopped=false stop_date= flag_set_at=` for a page carrying none. Evidence: those three printed lines.

- [ ] **AC4** — **The contract doc names the fields and the rule, inside the right section.** `sed -n '/## Fields the tick consumes/,/^## /p' docs/eligibility-predicate.md | grep -n 'stop_date'` returns ≥1 line, and that line names the ordering rule — evidence: the matched line, quoted.

- [ ] **AC5** — **No row without a marker changes behaviour.** The existing reopen-decision spec still passes unchanged — evidence: capture its case list **before** the change (`go test -v -count=1 ./pkg -args -ginkgo.focus='decides whether a flagged row is reopened' -ginkgo.v | grep -o 'case=.*' > /tmp/reopen-cases.before`), then after the change run the same command into `/tmp/reopen-cases.after` and require `diff /tmp/reopen-cases.before /tmp/reopen-cases.after` to print nothing — a non-empty diff is a moved verdict. The spec's name is still present (`grep -c 'decides whether a flagged row is reopened' pkg/flagged_tick_test.go` returns ≥1).

- [ ] **AC6** — **The runtime path stops reopening a stopped row.** After the released build is running, two rows are observed in one fleet round. The tick logs on **stderr** — `~/Library/Logs/claude-wrapper/stderr.log`; `stdout.log` carries no `flagged-tick` line at all.

  **Input state.** No page is both flagged and stopped today (`grep -rl '^flag: true' "25 Tasks"` and `grep -rl '^stopped: "true"' "25 Tasks"` do not intersect), so the pair is constructed and then restored. Both flags are written **`--by operator`**: `--by` defaults to `unknown`, and an unrecorded flag is refused (`refused_by_reason=flag-provenance-unrecorded`) before the stopped guard ever runs — so a bare `task set … flag true` can never reach this AC's evidence.

  1. Flag the measured row — `vault-cli task set 'Classify the Heat-Pump Penetration Seal' flag true --by operator` — then confirm its `flag_set_at` is newer than its `stop_date` (`2026-11-24T18:05:33+01:00`) **and** that its recorded session reads ABSENT (a LIVE row is skipped `not-absent`, for a reason unrelated to this fix). This is the **flag-after-stop** row.
  2. Flag a second stopped page `--by operator`, then stop it again (`/supervisor:stop "<task>"`), so its `stop_date` is newer than its `flag_set_at`. Its recorded session must also read ABSENT and it must not have been opened inside the `recently-spawned` window — otherwise the liveness guards answer first and the row contributes no `stopped` count however correct the fix is. This is the **stopped-after-flag** row.
  3. Restore both afterwards with `vault-cli task clear '<task>' flag`.

  **Evidence** — all reads bound to one captured round:

  ```bash
  line=$(grep 'flagged-tick opened=' ~/Library/Logs/claude-wrapper/stderr.log | tail -1)
  printf '%s\n' "$line" | grep -c 'rows=.*Classify the Heat-Pump Penetration Seal'
  printf '%s\n' "$line" | grep -o 'stopped:[0-9]*'
  grep 'flagged-tick detail' ~/Library/Logs/claude-wrapper/stderr.log | tail -1
  ```

  The first read returns ≥1: an opened row is listed on the **summary** line's `rows=` as `vault/kind/name`, and that is the row-bound evidence the flag-after-stop row opened. A `[^ ]*` capture cannot be used here — the key contains spaces. The second prints `stopped:1` or higher; `skipped_by_reason=` is space-separated and `;`-terminated, so the pattern is matched within the line. The third prints the round's detail line, which by construction lists only **refused and skipped** rows (`Detail()` renders `refused=… skipped=…` and is empty when neither is populated) — the stopped-after-flag row must appear there with its reason. A unit-test pass alone does not satisfy this AC: the bug is a runtime decision.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — lint + vet + unit tests clean
- `go test -v -count=1 ./pkg -args -ginkgo.focus='skips a stopped row only when the stop is newer than the flag' -ginkgo.v` — the ordering table, both directions
- `go test -v -count=1 ./pkg -args -ginkgo.focus='populates the stand-down marker from the record frontmatter' -ginkgo.v` — the source half
- `sed -n '/## Fields the tick consumes/,/^## /p' docs/eligibility-predicate.md | grep -n 'stop_date'` — the contract update

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- Restart the daemon so the released build is the one running — [[Claude Wrapper - Restart]] (the restart itself is a card, per the Decision Table)
- Then AC6's two log reads, over one real fleet round

## Desired Behavior

1. A flagged, open, due task whose `stop_date` is newer than its `flag_set_at` is skipped, and the tick line's `skipped_by_reason=` names the skip with a reason of its own (`stopped`). This check runs before the liveness guards, so a stopped row reports `stopped` rather than a derived verdict.
2. The same task with `flag_set_at` newer than `stop_date` opens exactly as it does today.
3. A task carrying `stopped: true` but no readable `stop_date`, or no readable `flag_set_at`, is skipped — the fail-closed default; absent evidence never reopens a retired row.
4. A task with no `stopped` marker, or with `stopped: false`, is unaffected.
5. The decision sees the marker as a boolean and both stamps as instants, each sourced from the page's own frontmatter — the source populates, it never decides.
6. `docs/eligibility-predicate.md` § *Fields the tick consumes* lists the three fields and the ordering rule.
7. No behaviour change for goal or topic rows: only a task carries the marker, and only the task record endpoint serves frontmatter.

## Constraints

- **The comparison parses both stamps to instants.** Both are written local-time ISO with an offset (`stop_date` by `task-standdown.py:175`, `flag_set_at` by vault-cli's `currentDateTime.Now().Time()`), and the two offsets need not agree across a DST boundary — so a lexicographic compare is wrong in both directions and fails silently in the permissive one.
- **The flag instant is the raw `flag_set_at`, never `approval_instant()`'s `approved_at`-first fallback.** That helper (claude-supervisor's `scripts/flag_provenance.py:107`) answers "when was this approved", which is a different question.
- **No new presence-only guard.** Every read of `stopped` is paired with the timestamp comparison; the no-date fallback is a skip, not an open.
- **`decision()` keeps its single home.** The rule lives in the one decision function; the row source populates fields and decides nothing.
- **The stopped guard runs before the liveness guards.** A stopped row reports `stopped`, not `not-absent`, `already-live` or `recently-spawned` — the operator's stand-down outranks a derived state, and this is what makes the skip observable in the runtime AC.
- **No producer change in another repo.** `stopped`, `stop_date` and `flag_set_at` already arrive in the frontmatter map. `VaultAPIPage.Frontmatter` is `map[string]any` and `frontmatterString` coerces a YAML bool and a quoted `"true"` identically, so the marker reads the same either way.
- **The existing reopen cases must not move.** Every row in AC5's case set keeps its verdict.

## Failure Modes

| Trigger | Detection | Expected behaviour | Recovery |
|---|---|---|---|
| `stop_date` present, `flag_set_at` absent (legacy flag) | tick line `skipped_by_reason=…stopped` | Row is skipped | Operator re-flags the row with `--by operator`, which writes a fresh `flag_set_at` |
| Both stamps present but one unparseable | same | Row is skipped | Fix the page's frontmatter; the next tick reads it |
| Clock skew between the two writes | none needed | The later write wins by its own stamp; a skew under the tick interval changes nothing | — |
| A re-flag arrives between the row read and the tick | the row opens one tick later than expected | The tick acts on the served set; the next read picks the flag up (≤ one interval + `serveGrace`) | Wait one tick |
| Frontmatter map absent for a task | all three zero in the tick line | Behaviour unchanged from today | — |
| A page carries `stopped: true` written by something other than `/supervisor:stop` | tick line `skipped_by_reason=…stopped` | Skipped — the marker is the marker whoever writes it | Operator re-flags with `--by operator` |

## Suggested Decomposition

Prompts must be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Row fields + source population | 5, 7 | 1, 3 | — |
| 2 | Ordering guard + `reasonStopped` + the case table | 1, 2, 3, 4, 7 | 1, 2, 5 | prompt 1 (needs the fields) |
| 3 | Contract doc + runtime evidence | 6 | 1, 4, 6 | prompt 2 |

Rationale: prompt 1 lands the plumbing with its own test and changes no decision; prompt 2 is the whole rule and cannot compile without prompt 1; prompt 3 is documentation plus the post-merge observation and is meaningless before prompt 2.
