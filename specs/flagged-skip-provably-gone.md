---
status: draft
---

## Summary

- A flagged row whose session has ended is skipped on every tick and never opens.
- The tick already computes the set of ended sessions from the ledger but uses it only to label the skip `not-absent-dead`, never to change the decision.
- Measured 2026-11-23: `41cba464-…` logged `not-absent-dead` on every tick from 19:07 to 19:25; the 19:31:22 tick carried `3bc07c89`, `e8b515a3`, `0e52b9b9` in the same bucket.
- A terminal ledger status alone is not proof of death — `08367283-…` took `status=done` at 19:14:19 and 19:37:36 while its process was alive and heartbeating until 20:13:30.
- This change admits a row to the open batch only when the ledger says ended **and** the child table proves the process gone.

## Problem

`decision()` (`pkg/flagged_tick.go:1466`) skips every row whose liveness is not `ABSENT` (`:1492`). When the ledger records a terminal status it labels the skip `not-absent-dead` (`:1497-1500`) — truthfully, but without acting on it.

Two facts make the current behaviour wrong in both directions.

**It never reopens a dead row.** A `not-absent-dead` row is removed before `pending = append` (`:1075`) and before the batch `OpenTick` (`:1103`), so nothing downstream can open it. The row stays `in_progress` and flagged indefinitely, and because `not-absent-dead` is an ordinary skip bucket nothing pages.

**It cannot simply trust the ledger.** `08367283-…` was `done` at 19:14:19 and 19:37:36 while pid 21641 was alive and heartbeating (`POST /api/1.0/session-heartbeat` every 30 s, `pkg/heartbeat.go:28`). A ledger-only rule would open a second session on a live task — two writers on one conversation.

**The standing population is a stale child-table row.** `childVerdict` (`pkg/liveness_verdict.go:150-158`) treats a nil `ExitCode` as still running, so a child record whose process is gone still reads `LIVE` and `rowLiveness` (`pkg/flagged_rows_vaultapi.go:436-447`) reports `LIVE` for it. The pid is the discriminator, and it lives on the session store (`SessionRecord.ChildPID`, `pkg/sessionstore.go:45`), not on the ledger. `pidAlive` already exists at `pkg/session_registry.go:271`.

## Goal

`decision()` admits a row to the open batch only when all hold:

1. The ledger records a terminal status for the row's session (`dead[sessionID]`), and
2. the session store holds a child record for that session **and** `pidAlive(record.ChildPID)` is false.

Every other case keeps today's behaviour and today's label:

- a live child record → skipped `not-absent-dead` (it is alive, whatever the ledger says);
- no child record at all → skipped `not-absent-dead` (a registry-only session has already been proved live by the registry, and an absent record proves nothing);
- `UNKNOWN` (the liveness read failed) → skipped;
- `none` (no session id) → skipped.

The child-liveness map is built once per tick from `t.Sessions.List(ctx)` and passed into `decision()` as a `map[string]bool` (`true` = alive), computed in `t.classify` (`pkg/flagged_tick.go:864`). A session absent from the map is treated as "not provably gone" — the conservative direction, because opening a second session on a live task is the harm this rule exists to prevent. The map must not be built per row.

No `FlaggedRow` field is added: threading a pid onto the row would give a child-table row `PID = 0`, and `pidAlive(0)` is false, which would mark every such row gone.

## Acceptance Criteria

- `decision()` with a terminal ledger entry and a child record whose pid is dead returns neither refused nor skipped (eligible).
- `decision()` with a terminal ledger entry and a child record whose pid is alive returns skipped `not-absent-dead`.
- `decision()` with a terminal ledger entry and no child record returns skipped `not-absent-dead`.
- `decision()` with a terminal ledger entry and `row.Liveness == "UNKNOWN"` and no child record returns skipped `not-absent-dead`.
- `make precommit` is green.
- On the running daemon, the tick line's `rows=` names at least one row that carried `<id>: not-absent-dead` in the Detail line on the previous build.

## Out of Scope

- The live-but-foreign cause (`spawn-refused: resume_row_unreachable`) — a separate defect.
- Clearing stale `claude_session_id` values in the vault.
- The worker cap and its configuration.
- Rows excluded upstream by a future `defer_date` (`admissible()`, `pkg/flagged_rows_vaultapi.go:421-426`).
