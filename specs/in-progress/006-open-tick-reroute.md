---
status: prompted
approved: "2026-11-19T10:30:41Z"
generating: "2026-11-19T19:45:04Z"
prompted: "2026-11-19T19:45:04Z"
branch: dark-factory/open-tick-reroute
---

Task: bee8464c-9e24-4f63-9c67-0f0bd218f91e

## Summary

- The wrapper reopens flagged work on its own every 15 minutes.
- Today it picks "terminal tab or background worker" from a field on the task that is often stale. An agent task with that field set the old way still gets a tab.
- Once the open endpoint exists, the tick hands each row it has decided to open to that same decision. Agents then always run in the background, humans open in Direct, and managers open in Managers.
- The tick's own rules for which rows to open stay exactly as they are. Only the way an opened row is routed changes.
- After this, no code path in the wrapper can open an agent as a tab or put anything in the Hold window.

## Problem

The flagged reopen tick routes a row by its stored `mode:`. A row reading `interactive` goes to the tab spawner whatever its `role:` says. So an agent row with a stale `mode: interactive` reopens as a tab every 15 minutes. Spec `agents-key-removal` turns that into a refusal, which is safe but still leaves the row unopened. Goal and topic rows are worse off. The tick opens a worker whose only job is to run `/supervisor:open "<goal>"`, rather than the manager itself. The tick and the open endpoint (spec `open-endpoint`) would then make two different routing decisions for the same row. That is the drift the fleet's single-implementation rule exists to prevent.

## Goal

The flagged reopen tick routes every row it has decided to open through the open decision and executor from spec `open-endpoint`:
- agent rows always resume or start headless;
- human rows open in `Direct`;
- goal and topic rows open their manager in `Managers`.

The tick keeps every one of its own gates. No code path in the wrapper (the open operation, the tick, or the tab spawner) can produce an agent tab, a tab in the role map's `Hold` window, or a tab in an unmapped window.

## Non-goals

- The tick's own gates: predicate refusals, blocked, live, recently spawned, caps, rate limiter, the 50-row budget, the parked-wait gate. All unchanged.
- Running the answer-path probe from the tick. The tick is the daemon, not a session with a manager registration, so the probe has no session to check. The tick never ran it, and this spec keeps that. See `docs/open-endpoint.md` § Answer-path probe.
- Activating panes. A tick that focused windows every 15 minutes would take the operator's screen.
- Changing the eligibility predicate (`flagged-rows.py`) or its contract in `docs/eligibility-predicate.md`.
- Do NOT add a per-tick switch back to mode-based routing. If a future consumer needs it, that is a separate spec.

## Assumptions

- Spec `open-endpoint` has landed: the decision function, the executor with its tick caller mode (no probe, no pane activation; that spec's DB8 and AC24), and `docs/open-endpoint.md`. This spec calls the tick entry point and depends on it.
- Spec `agents-key-removal` has landed. The tab spawner refuses agent roles, so a routing bug here fails loud rather than opening a tab.
- `~/.cache/wezterm-role-map.json` carries `Managers`, `Direct` and `Hold` (read 2026-11-18).

## Desired Behavior

1. A row that passes the tick's gates is opened through the open executor, with the tick's row facts as input: role, stored mode, kind, vault, name and recorded session id. Routing follows role, never stored mode.
2. On the tick path the executor never activates a pane and never runs the answer-path probe. A row the decision classifies `jump` or `live` is counted skipped, and a refused row is counted refused with its reason token in the tick's detail line.
3. Tick task rows keep the readiness first step ahead of `/vault-cli:work-on-task`, passed as the row's `first_step`. Goal and topic rows open their manager directly, per `docs/open-endpoint.md` § Manager rows.
4. The tick's two log lines stay in their current shape: the `flagged-tick` summary line and the `flagged-tick detail` line, both built in `pkg/flagged_tick.go`. A row the executor refuses is recorded as `<row key>: <reason token>: <message>`, so the summary line's existing `refused_by_reason=` field counts it under its reason token (for example `role-map-key-missing:1`). A row the decision classifies `jump` or `live` is recorded under the tick's existing `already-live` skip reason, so `skipped_by_reason=` counts it as `already-live`. No new field is added. The executor's per-row `open:` line is also written for each opened or refused row.

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0. Evidence: exit code.
- [ ] **AC2 — tick never opens an agent row as a tab.** A tick fed a flagged row `role: agent, mode: interactive, session_id: <uuid>` that passes the tick's gates makes one headless resume with that id and 0 tab-spawner calls. With `session_id` empty it makes one headless create. Evidence: tick unit test output with both call counts.
- [ ] **AC3 — the other roles go through the same decision.**
  - A tick `role: human` row opens a tab in `Direct.window_id`.
  - A tick goal row opens a `<goal> Manager` tab in `Managers.window_id`, with no `/supervisor:open` worker.
  - With the `Direct` key absent, a tick human row is counted refused, the summary line carries `refused_by_reason=role-map-key-missing:1`, and nothing spawns.
  - The tick never activates a pane. A `jump`/`live` row is counted skipped, the summary line carries `skipped_by_reason=already-live:1`, and the pane-activation fake records 0 calls.
  - The probe fake records 0 calls.
  - Task rows still carry the readiness first step.

  Evidence: tick unit test output.
- [ ] **AC4 — tick gates unchanged.** Every existing tick test that does not assert routing passes unmodified: refusal, blocked, live, recently spawned, caps, rate limiter, row budget, parked-wait. Tests that asserted mode-based routing are updated to role-based routing, and the prompt lists each one. Evidence: `go test ./pkg/ -args -ginkgo.focus='FlaggedTick|FlaggedReopen'` exit 0 and the list in the completion report. (The package has one Ginkgo suite under a single `TestPkg`, so a `-run FlaggedTick` pattern selects nothing.)
- [ ] **AC5** — `CHANGELOG.md` has a bullet under `## Unreleased` that starts with `- fix: **the flagged reopen tick routes every row by role through the open decision`. Evidence: `awk '/^## /{s=$0} /^- fix: \*\*the flagged reopen tick routes every row by role through the open decision/{print s}' CHANGELOG.md` prints exactly `## Unreleased`. Today it prints nothing.

## Verification

### Container-executable

```
make precommit
go test ./pkg/ -args -ginkgo.focus='FlaggedTick|FlaggedReopen' -ginkgo.v
awk '/^## /{s=$0} /^- fix: \*\*the flagged reopen tick routes every row by role through the open decision/{print s}' CHANGELOG.md
```

## Constraints

- The decision and executor from spec `open-endpoint` are reused as they are. The tick passes its row facts in and adds no routing logic of its own.
- `docs/eligibility-predicate.md`'s `FlaggedRow` contract is unchanged.
- The tick stays model-free. No model is invoked in its path.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Role map missing `Direct`/`Managers` | Those rows are counted refused `role-map-key-missing`; agent rows still open | Operator restores the key; the next tick retries | Tick detail line |
| `manager-launcher.py` / `topic-spawn.py` refuses | Row counted refused `spawn-refused` | Next tick retries; the operator fixes the launcher config | Tick detail line |
| Cap or rate refusal | Unchanged: the tick stops opening further rows this tick | Next tick | Tick log line |
| Registry unreadable | Row counted refused `liveness-unreadable`, never opened as "not live" | Next tick | Tick detail line |

## Do-Nothing Option

The open endpoint routes by role while the tick routes by stored mode, so the same row opens two ways depending on who opened it. After spec `agents-key-removal`, agent rows with a stale `mode:` are refused every tick instead of opened, and that work goes dark. Not acceptable.
