---
status: prompted
approved: "2026-11-18T16:53:35Z"
generating: "2026-11-18T17:37:49Z"
prompted: "2026-11-18T17:37:49Z"
branch: dark-factory/agents-key-removal
---

Task: bee8464c-9e24-4f63-9c67-0f0bd218f91e

## Summary

- The wrapper still maps the agent role to a terminal window called "Agents". The operator removed that window on 2026-11-12.
- Because of that stale entry, an agent opened as a terminal tab lands in whatever window happens to be focused. On 2026-11-18 that was the Hold window.
- Agents are never supposed to get a tab at all. Only the operator's own sessions and managers do.
- This change deletes the stale entry and makes every tab-opening path in the wrapper refuse an agent outright, with a message saying to open it headless.
- Headless agents, human tabs and manager tabs keep working exactly as before.

## Problem

The wrapper's role table maps `agent → "Agents"`, `manager → "Managers"` and `human → "Direct"`. `~/.config/wezterm` commit `8acaf37` (2026-11-12) removed the Agents window, and the role map no longer has an `Agents` key. A tab spawn for an agent therefore resolves no window. The tab path then opens the tab in the focused window, without saying so.

Nothing in the wrapper refuses an agent tab. `spawn_agent` with `interactive=true` and an agent role (passed explicitly, or defaulted from an empty `role`) opens one. The flagged reopen tick does the same for any agent row whose stored `mode` is `interactive`. Matrix Rule 2 forbids exactly this. `claude-supervisor` `commands/open.md` 0.341.2 records the gap: "the server does **not** refuse the pair … nothing stops it at the call boundary and the mistake reaches disk."

## Goal

The wrapper's code carries no `Agents` role-map key. Its tab spawner refuses any request whose role resolves to `agent` before it opens a pane. So `spawn_agent` `interactive=true` with an agent role returns a refusal naming matrix Rule 2, and an agent can reach a worker only through a headless spawn. Human and manager tabs, and every headless spawn, behave as before.

## Non-goals

- The open endpoint (spec `open-endpoint`) and the tick reroute (spec `open-tick-reroute`). This spec ships first and depends on neither.
- The `claude-supervisor` plugin's own `agent → Agents` mappings. That is separate work in that repo.
- Auto-converting an agent tab request into a headless spawn. A refusal keeps the mistake visible. A silent conversion would hide a caller that still asks for tabs.
- Changing how the spawn mode is resolved (argument, then `SUPERVISOR_SPAWN_MODE`, then `spawn.mode`, then the built-in default).
- Do NOT add a flag, environment variable or config key that re-enables agent tabs. The invariant is the point. If a future consumer needs it, that is a separate spec.

## Assumptions

- `~/.cache/wezterm-role-map.json` carries keys `Managers`, `Direct` and `Hold` only (read 2026-11-18). The Agents window was removed by `~/.config/wezterm` commit `8acaf37` (2026-11-12).
- The fleet's `spawn.mode` in `~/.config/claude-supervisor/config.json` is `headless`, and `SUPERVISOR_SPAWN_MODE` is unset (both read 2026-11-18). So an ordinary `spawn_agent` call with no `interactive` argument takes the headless path and is unaffected.
- Role × mode rules: `claude-supervisor/docs/role-mode-matrix.md` Rule 2 (agent → headless always).

## Desired Behavior

1. The role table has no entry for `agent`. `agent` stays a valid role for headless spawns, ledger rows and the spawn caps. It just never resolves to a window or a role-map chip.
2. The tab spawner refuses a request whose role is `agent`, given explicitly or defaulted from an empty `role`. The refusal comes before any side effect: no pane, no colour, no ledger row. The message names matrix Rule 2 and says to spawn headless (`interactive=false`).
3. `spawn_agent` returns that refusal whenever an agent-role call would take the tab path, whatever the mode source (argument, env, config or default). Agent calls that take the headless path, and human and manager tab calls, are unchanged.
4. A role map that still carries a stale `Agents` key changes nothing. The key is never read.

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0. Evidence: exit code.
- [ ] **AC2 — no `Agents` key in wrapper code.** `grep -rnw --exclude='*_test.go' 'Agents' pkg cmd main.go` returns 0 lines. On the pre-change tree the same command returns 2 lines, both in `pkg/role_map.go`, the comment included, which proves the probe fires. Evidence: command output on both trees, recorded in the completion report.
- [ ] **AC3 — tab spawner refuses an agent.** A tab-spawn request with `role: "agent"`, and one with `role: ""`, each return an error whose text contains `Rule 2` and `interactive=false`. The tab-client fake records 0 spawns and 0 sends, and the ledger fake records 0 posts. Evidence: unit test output.
- [ ] **AC4 — spawn_agent refuses an agent tab whatever the mode source.** These calls return the AC3 refusal as a tool error, and the tab-client fake records 0 spawns:
  - `spawn_agent` with `interactive=true` and `role: "agent"`;
  - `spawn_agent` with `interactive=true` and no `role`;
  - `spawn_agent` with no `interactive` argument, an agent role, and the mode resolving `interactive` from `SUPERVISOR_SPAWN_MODE`.

  The same agent call with `interactive=false` reaches the headless supervisor fake. Evidence: unit test output.
- [ ] **AC5 — other roles unaffected.** `spawn_agent` `interactive=true` with `role: "human"` opens a tab in `Direct.window_id`, and with `role: "manager"` a tab in `Managers.window_id`, as before. Evidence: existing tab-spawn tests pass. Those that used the `Agents` fixture are rewritten to a human or manager role, and the prompt lists each one.
- [ ] **AC6 — stale key ignored.** With a role map that still carries `"Agents": {chip: "pink", window_id: 16}`, an agent tab request is refused as in AC3. Nothing spawns in window 16. Evidence: unit test output.
- [ ] **AC7** — `CHANGELOG.md` has a bullet under `## Unreleased` that names the refusal of agent tabs. Evidence: `awk '/^## /{s=$0} /agent tab|Agents/{print s}' CHANGELOG.md` prints `## Unreleased`.

## Verification

### Container-executable

```
make precommit
go test ./pkg/... -run 'TabSpawn|RoleMap|Roster' -v
grep -rnw --exclude='*_test.go' 'Agents' pkg cmd main.go      # expect no output
awk '/^## /{s=$0} /agent tab|Agents/{print s}' CHANGELOG.md
```

## Constraints

- Headless `spawn_agent`, `spawn_session`, `POST /api/1.0/sessions`, the spawn caps and the ledger schema are unchanged.
- Human tabs keep their vault-family chip, and manager tabs keep the `Managers` window and chip.
- The tab spawner's existing order holds: every refusal is decided before the first side effect.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| A caller still passes `interactive=true` for an agent | Refused naming Rule 2; nothing opens | Caller passes `interactive=false` or omits it | Tool error text |
| Operator sets `spawn.mode` or `SUPERVISOR_SPAWN_MODE` to `interactive` | Every agent-role `spawn_agent` without `interactive=false` is refused | Set the mode back to `headless`, or pass `interactive=false` | Tool error text on every agent spawn |
| The flagged reopen tick meets an agent row with stored `mode: interactive` (until spec `open-tick-reroute` lands) | The tab spawner refuses it, the tick counts it refused, and no tab opens. It used to open a tab | Spec `open-tick-reroute`, or fix the row's `mode:` to `headless` | Tick log line, refused count |

## Do-Nothing Option

An agent tab keeps landing in whichever window has focus, and nothing at the call boundary refuses it. That is half of the 2026-11-18 failure, and it stays reachable from `spawn_agent` and the tick even after the open endpoint ships. Not acceptable.
