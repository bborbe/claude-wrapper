---
status: completed
spec: [006-open-tick-reroute]
summary: Routed every row the flagged reopen tick opens through one batched OpenExecutor.OpenTick call (shared with the HTTP/MCP surface), preserving all tick gates, log-line shapes, already-live skips, refusal reason tokens and the task readiness first step, deleting the goal/topic /supervisor:open worker prompt; added role-based tick tests and replaced the mode-based routing tests in flagged_reopen_test.go (goal/topic prompt and interactive-row cases), plus a nolint on an unrelated pre-existing gosec G115 finding so precommit passes under the container toolchain.
execution_id: 9fe6d449-2b0e-5c03-817b-0efb1c221a9c-exec-013-spec-006-open-tick-reroute
dark-factory-version: dev
created: "2026-11-19T19:45:04Z"
queued: "2026-11-19T19:45:04Z"
started: "2026-11-19T19:46:31Z"
completed: "2026-11-19T20:01:46Z"
---

# Route the flagged reopen tick through the open decision

## Summary

- The wrapper's flagged reopen tick decides which tasks, goals and topics to reopen every 15 minutes.
- Today it routes each opened row by the row's stored `mode:` field, and by a role fallback. Goal and topic rows get a worker that runs `/supervisor:open`, not the manager itself.
- This change sends every row the tick has decided to open through the open executor's tick entry point, so the tick and `POST /api/1.0/open` cannot route the same row two different ways.
- The tick's own gates — eligibility, blocked, live, recently spawned, caps, rate limit, row budget, parked wait — are untouched.
- After this, no wrapper code path can open an agent as a tab, or put anything in the role map's `Hold` window.

## Objective

`pkg/flagged_reopen.go` and `pkg/flagged_tick.go` open rows through `OpenExecutor.OpenTick` instead of choosing a spawn path themselves.

## Context

Read these first — they are the code and contracts this change must match:

- `specs/in-progress/006-open-tick-reroute.md` — the spec this prompt implements. Desired Behavior 1–4 and AC1–AC5 are the requirements.
- `docs/open-endpoint.md` — the open contract: § Verdict, § Decision order, § Answer-path probe, § Manager rows.
- `pkg/open_executor.go` — `OpenExecutor` interface (line ~41) and `OpenTick` (line ~90). **`OpenTick` already exists and is already wired to the decision.** Do not re-implement it; call it.
- `pkg/open_decision.go` — `OpenRow`, `OpenRowFacts`, `OpenDecision`, `OpenVerdict` (the verdict fields you will read: `Branch`, `Reason`, `Message`, `WindowKey`, `WindowID`, `SessionID`).
- `pkg/flagged_reopen.go` — the tick's opener: `Reopen`, `isHeadlessRow` (line ~295), and the tab branch that currently runs a `/supervisor:open` worker for goal and topic rows.
- `pkg/flagged_tick.go` — the `flagged-tick` summary line and the `flagged-tick detail` line, including the existing `refused_by_reason=` and `skipped_by_reason=` fields and the `already-live` skip reason.
- `docs/eligibility-predicate.md` — the `FlaggedRow` contract. **Unchanged by this work.**

## Requirements

1. **Route every opened row through `OpenTick`.**
   - Build one `OpenRow` per `FlaggedRow` the tick has already decided to open, carrying: kind (`task`/`goal`/`topic`), vault, name, role, stored mode, and the recorded session id.
   - Call `OpenTick` once with the batch. Do not add routing logic to the tick.
   - Routing follows `role`, never the stored `mode`. Do not read `mode` to choose a path anywhere in the tick.

2. **Keep the tick's gates exactly where they are.** Predicate refusals, blocked, live, recently-spawned, caps, rate limiter, the 50-row budget and the parked-wait gate all run before `OpenTick` and are not modified. If a row is refused by a tick gate, `OpenTick` never sees it.

3. **Tick mode does no probe and no pane activation.** `OpenTick` already behaves this way — verify it and do not regress it. A row the decision classifies `jump` or `live` is counted skipped; a refused row is counted refused with its reason token.

4. **Preserve the readiness first step for task rows.** A task row's `first_step` (the readiness step ahead of `/vault-cli/work-on-task`) is passed through on the `OpenRow`. Goal and topic rows open their manager directly per `docs/open-endpoint.md` § Manager rows — they no longer get a worker that runs `/supervisor:open "<name>"`. Delete that worker path.

5. **Keep both log lines in their current shape.**
   - A refused row is recorded in the detail line as `<row key>: <reason token>: <message>`, so the existing summary field counts it: `refused_by_reason=role-map-key-missing:1`.
   - A `jump`/`live` row is recorded under the existing `already-live` skip reason: `skipped_by_reason=already-live:1`.
   - Do not add a new summary field.

6. **Tests.**
   - Tick fed `role: agent, mode: interactive, session_id: <uuid>` → one headless resume with that id, zero tab-spawner calls. With `session_id` empty → one headless create.
   - Tick `role: human` → a tab in `Direct.window_id`.
   - Tick goal row → a `<goal> Manager` tab in `Managers.window_id`, and no `/supervisor:open` worker.
   - `Direct` key absent → row refused, summary carries `refused_by_reason=role-map-key-missing:1`, nothing spawns.
   - A `jump`/`live` row → counted skipped, `skipped_by_reason=already-live:1`, pane-activation fake records 0 calls, probe fake records 0 calls.
   - Task rows still carry the readiness first step.
   - Every existing tick test that does not assert routing must pass **unmodified**. Tests that asserted mode-based routing are updated to role-based routing — list each one you changed in the completion report.

7. **CHANGELOG.** Add a bullet under `## Unreleased` (create the section above the topmost `## vX.Y.Z` if it is absent) beginning exactly:
   `- fix: **the flagged reopen tick routes every row by role through the open decision`

## Constraints

- Reuse the decision and executor from spec `005-open-endpoint` as they are. The tick passes row facts in; it adds no routing of its own.
- The tick stays model-free. No model call in its path.
- `FlaggedRow` in `docs/eligibility-predicate.md` is unchanged.
- Do not add a switch back to mode-based routing.
- Do not touch `pkg/handler/open.go`, `pkg/open_surface.go`, `pkg/open_decision.go` or `pkg/open_tab.go` unless a compile error forces it — and say so if it does.
- Do not commit.

## Verification

Run inside the container; no git, docker or dark-factory commands.

```
make precommit
go test ./pkg/ -args -ginkgo.focus='FlaggedTick|FlaggedReopen' -ginkgo.v
awk '/^## /{s=$0} /^- fix: \*\*the flagged reopen tick routes every row by role through the open decision/{print s}' CHANGELOG.md
```

Expected: `make precommit` exits 0. The focused run passes. The `awk` prints exactly `## Unreleased` — today it prints nothing, so the check is real.

## Acceptance criteria mapping

| AC | Requirement |
|---|---|
| AC1 `make precommit` exits 0 | Verification |
| AC2 tick never opens an agent row as a tab | Req 1, 6 |
| AC3 other roles go through the same decision | Req 1, 3, 4, 6 |
| AC4 tick gates unchanged | Req 2, 6 |
| AC5 CHANGELOG bullet under `## Unreleased` | Req 7 |
