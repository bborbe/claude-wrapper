---
status: approved
approved: "2026-11-11T11:04:14Z"
branch: dark-factory/roster-set-permission-mode
---

## Summary

- Managers get a new roster tool, `set_agent_permission_mode`, that switches a running headless worker's permission mode live, so a manager can unblock a worker stuck on a classifier denial without the operator pressing Shift+Tab in a pane that does not exist.
- Only two modes are allowed: `acceptEdits`, `auto`. `bypassPermissions`, `plan`, `dontAsk` and every other value are refused, and the refusal names the allowed set.
- It only works on headless workers this daemon runs. Tab, docker/k8s and unknown workers are refused with a reason that points to the route that does apply to them.
- The reply reports the mode the worker says it now holds, not the value the manager asked for.
- Every switch is written to an append-only audit file: who asked, which worker, from which mode to which mode, when.

## Problem

Headless supervisor workers run in `auto` mode, where a classifier decides each tool call. That classifier routinely refuses ordinary shipping steps such as `git worktree add`, `git commit`, `git push`, `gh pr merge` and a dev deploy. The documented fix is "switch to accept edits (Shift+Tab)". A headless worker has no pane, so nobody can press it. Between 2026-11-05 and 2026-11-11 the attention store logged 131 Shift+Tab closer lines and 279 `/reload-plugins` lines. Each one was an operator card that nobody could act on. The daemon can already switch a running child's mode live through the SDK control request, but only on its `/mcp` face, which serves Codex-style clients. Managers bind only `/supervisor/mcp`, so the capability exists and the people who need it cannot reach it. On 2026-11-11 the operator decided to give managers this switch, limited to a capped set of modes.

## Goal

A manager bound to `/supervisor/mcp` can move a local headless worker it can see on the roster between `acceptEdits` and `auto` with one tool call. The reply carries the mode the child reports for itself. The daemon's session listing shows the new mode immediately. An append-only audit file holds one line per switch. Nothing outside those two modes can be reached through this tool, and nothing other than a local headless worker can be targeted.

## Non-goals

- Do NOT change the `/mcp` face's tool list, tool names or argument shapes. Its accepted mode set gains `auto` only through the shared supervisor check (DB 1). This is intended and has a visible side effect: `/mcp` `spawn_session` and `POST /api/1.0/sessions`, which share that check, also start accepting `auto`. No other `/mcp` change.
- Do NOT switch modes automatically: no classifier-denial detector and no auto-revert after a step. A mode is changed only when a caller asks for it.
- Do NOT support tab workers. A tab worker's mode belongs to its pane.
- Do NOT support docker/k8s (A2A) workers. Their mode is fixed at spawn.
- Do NOT carry a switched mode across a resume or respawn. A resumed worker starts on its spawn mode again.
- Do NOT write the switch into the spawn-ledger row. The store keeps one row per session, a POST replaces the whole row, and it refuses unknown columns, so it cannot hold a from→to history.
- Do NOT add switch records to `permissions.jsonl`. Its key set is frozen to the node server's.
- Do NOT record refused calls in the audit file. The refusal goes back to the caller and nothing changed.
- Do NOT add a flag, env var or config key for the audit-file path or for the allowed-mode set. Both are invariants. If a future consumer needs variation, that is a separate spec.
- Do NOT add `manual` (listed by current `claude --help`) or any other mode to the allow-list. The operator capped it at `acceptEdits`; the allow-list is exactly two modes, `acceptEdits` and `auto`.
- Do NOT reconcile the shared `/mcp` / REST mode set with the current CLI choices. `default` stays in it, and `manual` / `dontAsk` are not added. That set only gains `auto` (DB 1). A separate spec handles the reconciliation.

## Acceptance Criteria

- [ ] **AC1**: `make precommit` exits 0 in the worktree root. Evidence: exit code.
- [ ] **AC2**: The roster face lists the new tool. Evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='lists exactly the ten roster tools' -ginkgo.v` exits 0 and reports `Ran 1 of`. The renamed spec asserts `ConsistOf` over the previous nine names plus `set_agent_permission_mode`, and `grep -c 'lists exactly the nine roster tools' pkg/roster_mcp_test.go` returns `0`.
- [ ] **AC3**: Allow-list table test. Evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='set_agent_permission_mode allow-list' -ginkgo.v` exits 0 and prints one line `case=<mode> forwarded=<bool>` per row below. The test asserts the fake supervisor's recorded calls, not a print:

  | `mode` argument | forwarded | tool result |
  |---|---|---|
  | `default` | false | `isError: true`, text contains `acceptEdits, auto` |
  | `acceptEdits` | true | success |
  | `auto` | true | success |
  | `bypassPermissions` | false | `isError: true`, text contains `acceptEdits, auto` |
  | `plan` | false | `isError: true`, same text |
  | `dontAsk` | false | `isError: true`, same text |
  | `manual` | false | `isError: true`, same text |
  | `ACCEPTEDITS` (case variant) | false | `isError: true`, same text |
  | empty string `""` | false | `isError: true`, same text |

  `forwarded=false` means the fake supervisor's permission-mode call count is 0 for that case. Every refused row's text also contains the requested value itself.
- [ ] **AC4**: Eligibility table test. Evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='set_agent_permission_mode eligibility' -ginkgo.v` exits 0 and prints `case=<name> forwarded=<bool>` per row. Every refused row carries `isError: true` and a fake-supervisor call count of 0:

  | case | agent is | forwarded | refusal text contains |
  |---|---|---|---|
  | `headless` | a live session this daemon holds, mode headless | true | n/a |
  | `adopted` | a session adopted after a daemon restart (held in the supervisor's map) | true | n/a |
  | `a2a` | a docker/k8s worker on the A2A manager | false | `local headless` and `shipping` |
  | `tab` | a ledger row with mode `interactive` (roster row `rehydrated: true`) | false | `local headless` and `pane` |
  | `foreign` | a rehydrated ledger row that is not a tab | false | `local headless` and `shipping` |
  | `unknown` | an id present nowhere | false | `local headless` |
  | `exited` | a held session in state `exited` | false | `exited` |
  | `busy` | a held session whose per-session sequence lock is taken | false | `running a sequence` |
  | `missing-id` | `agent_id` empty | false | `agent_id` |
- [ ] **AC5**: The reply carries the child-reported mode, not the request. Evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='reports the mode the child answers with' -ginkgo.v` exits 0. The fake supervisor answers `default` to a request for `acceptEdits`. The test asserts the tool text decodes to `permission_mode: "default"`, `requested: "acceptEdits"` and `previous_mode` equal to the session's mode before the call. A mutation that returns the request (`permission_mode` set from the argument) makes the same command exit non-zero with `[FAILED]`.
- [ ] **AC6**: Each switch appends exactly one audit line, and a refusal appends none. Evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='records each permission mode switch' -ginkgo.v` exits 0. Against a temp-file audit log the test asserts:
  - after one successful switch, the file has 1 line;
  - that line decodes to exactly the keys `ts, agent_id, caller_session_id, from, requested, to`;
  - `caller_session_id` equals the `X-Claude-Code-Session-Id` header of the call;
  - `to` equals the child-reported mode;
  - after a refused `bypassPermissions` call, the file still has 1 line;
  - when the audit write fails, the switch still succeeds and the reply carries `recorded: false`.
- [ ] **AC7**: The shared supervisor check accepts `auto`. Evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='accepts auto as a permission mode' -ginkgo.v` exits 0. The test asserts that a supervisor permission-mode change to `auto` on a held session does not return the invalid-session-change error. `grep -n 'PermissionModeAuto' pkg/mcptools.go` returns at least 1 line.
- [ ] **AC8**: Docs and changelog. Evidence:
  - `awk '/^## /{sec=$0} /set_agent_permission_mode/{print sec; exit}' CHANGELOG.md` prints `## Unreleased`;
  - `grep -n '^## Unreleased' CHANGELOG.md` returns exactly 1 line, above the first `## v` heading;
  - `awk '/^### /{sec=$0} sec ~ /permission-mode/ && /`auto`/{n++} END{print n+0}' README.md` prints a number ≥1. It prints `0` today, and after the change the `POST /api/1.0/sessions/{id}/permission-mode` section lists `auto` among its accepted modes.
- [ ] **AC9**: **Post-Deploy (Rung-2):** A live switch on a real headless worker. Evidence: run the Operator-executable steps 1–5 in Verification against the running daemon, all with header `X-Claude-Code-Session-Id: spec-verify-roster-mode`:
  - (a) the spawned worker reads `"permission_mode":"auto"` in `GET http://localhost:18090/api/1.0/sessions` before the switch;
  - (b) the `set_agent_permission_mode` call with `mode: acceptEdits` returns a result with `isError` absent or false, and the text holds `"permission_mode":"acceptEdits"` and `"previous_mode":"auto"`;
  - (c) `GET /api/1.0/sessions` then shows that worker's row with `"permission_mode":"acceptEdits"`;
  - (d) `tail -1 ~/.local/state/claude-supervisor/permission-mode-changes.jsonl` decodes to `agent_id` = the worker id, `caller_session_id` = `spec-verify-roster-mode`, `from` = `auto`, `to` = `acceptEdits`.

  Freshness check:
  - `deploy_check:` `curl -s -X POST -H 'Content-Type: application/json' -H 'X-Claude-Code-Session-Id: spec-verify-roster-mode' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' http://localhost:18090/supervisor/mcp | grep -o '"set_agent_permission_mode"' | head -1`
  - `deploy_target:` `"set_agent_permission_mode"`
- [ ] **AC10**: **Post-Deploy (Rung-2):** A refused escalation against the same worker. Evidence: Verification step 6 (a `set_agent_permission_mode` call with `mode: bypassPermissions`) returns a result with `"isError":true`. Its text contains `bypassPermissions` and `acceptEdits, auto`. The verifier quotes the full refusal text verbatim in the verification report. After the call, `GET /api/1.0/sessions` still shows that worker at `"permission_mode":"acceptEdits"`, and `wc -l` of the audit file is unchanged from after step 5.
  - `deploy_check:` same command as AC9
  - `deploy_target:` `"set_agent_permission_mode"`

No new scenario: AC9 and AC10 exercise the real child end-to-end, and every other branch is reachable with the fake supervisor.

## Verification

### Container-executable

```
make precommit
go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='set_agent_permission_mode|lists exactly the ten roster tools|reports the mode the child answers with|records each permission mode switch|accepts auto as a permission mode' -ginkgo.v
awk '/^## /{sec=$0} /set_agent_permission_mode/{print sec; exit}' CHANGELOG.md
```

### Operator-executable (host, against the deployed daemon on `localhost:18090`)

Define once: `H=(-s -X POST -H 'Content-Type: application/json' -H 'X-Claude-Code-Session-Id: spec-verify-roster-mode')`, `U=http://localhost:18090/supervisor/mcp`.

1. Spawn: `curl "${H[@]}" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"spawn_agent","arguments":{"target":"local","interactive":false,"cwd":"/tmp","prompt":"Reply with only the word OK."}}}' $U`. Note `agent_id`.
2. Wait until `curl -s http://localhost:18090/api/1.0/sessions | jq '.[] | select(.id=="<agent_id>") | .state'` reads `"idle"` (at most 120 s), then read `.permission_mode`. It must be `"auto"`.
3. Note `wc -l < ~/.local/state/claude-supervisor/permission-mode-changes.jsonl` (0 if the file does not exist).
4. Switch: `curl "${H[@]}" -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"set_agent_permission_mode","arguments":{"agent_id":"<agent_id>","mode":"acceptEdits"}}}' $U`.
5. Re-read `.permission_mode` as in step 2. It must be `"acceptEdits"`. Then run `tail -1` on the audit file.
6. Refusal: the same call as step 4 with `"mode":"bypassPermissions"`. Quote the response text, then re-read the mode and the audit line count.
7. Cleanup: `curl "${H[@]}" -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"stop_agent","arguments":{"agent_id":"<agent_id>"}}}' $U`.

## Desired Behavior

1. **The shared supervisor permission-mode check accepts `auto`.** `auto` is the mode every roster/REST headless child is spawned in (`supervisor.DefaultPermissionMode`). `/mcp` `spawn_session` keeps its own `bypassPermissions` default, which is unchanged. A check that refuses it as unknown means a worker switched away from `auto` can never be switched back. The accepted set becomes `default`, `acceptEdits`, `auto`, `bypassPermissions`, `plan`, so the REST and `/mcp` faces also start accepting `auto`. The README's permission-mode section lists it.
2. **The roster face exposes `set_agent_permission_mode`**, with arguments `agent_id` (required) and `mode` (required), as the tenth roster tool.
   - The `<verb>_agent_<noun>` name matches `send_agent_message` and the roster's agent-centric naming.
   - It avoids reusing `/mcp`'s `set_permission_mode`, which takes `session_id`. A client bound to both faces would otherwise see two tools with the same name and different arguments.
   - The tool description states the two allowed modes and the local-headless-only scope.
3. **The mode is checked against the roster allow-list before anything else is looked up.** The allow-list is exactly `acceptEdits`, `auto`, matched case-sensitively. Any other value is refused with text naming the value and the allowed set `acceptEdits, auto`. `default` is deliberately out: the current CLI no longer lists it (`claude --help` 2026-11-11: `acceptEdits, auto, bypassPermissions, manual, dontAsk, plan`), and a prompt-for-everything mode on a headless worker would only park every tool for the manager. The cap is `acceptEdits`, per the operator's decision 2026-11-11. A refused mode never reaches the supervisor or the child, whatever the agent.
4. **Only a local headless session this daemon holds is switched.** That includes one adopted after a restart, because the daemon holds its stdin. Every other target is refused, its text contains `local headless`, and it names the route that does apply:
   - A2A (docker/k8s) worker: respawn it with `shipping: true`.
   - Tab worker (an `interactive` ledger row): switch it in its own pane.
   - Other rehydrated or foreign row: respawn it headless with `shipping: true`.
   - Unknown id: the id names no worker on this daemon.
   - Exited session: it has exited.
   - Session holding its per-session sequence lock: it is running a sequence. The call fails fast instead of queuing, matching the `/mcp` and REST faces.
5. **The switch travels as the existing SDK control request, and the reply reports the child's own answer.** The reply is JSON with:
   - `agent_id`;
   - `previous_mode`: the session's mode as the daemon reported it just before the call, which may be empty if the child has not emitted `init`;
   - `requested`;
   - `permission_mode`: the mode the child's control response reports;
   - `recorded`: whether the audit line was written.

   A child that refuses the mode, or that does not answer within the existing control-request timeout, produces an error result carrying the child's refusal or the timeout. It never produces a success that echoes `requested`.
6. **Each successful switch appends one line to `~/.local/state/claude-supervisor/permission-mode-changes.jsonl`.**
   - Keys are exactly `ts` (UTC, millisecond RFC3339, the same format as `permissions.jsonl`), `agent_id`, `caller_session_id` (from `X-Claude-Code-Session-Id`), `from` (= `previous_mode`), `requested`, and `to` (= the child-reported mode).
   - The parent directory is created if missing.
   - A failed write does not undo or fail the switch, which has already landed in the child. The daemon log gets one line starting `WARNING: permission mode change record for <agent_id> could not be written`, and the reply carries `recorded: false`.
7. **The switch is visible immediately on the daemon's session listing.** `GET /api/1.0/sessions` reports the child-reported mode as soon as the control response arrives, without waiting for the next turn's `init`. This is existing behaviour of the control-response path. The roster tool must go through that same path and not around it.

## Constraints

- **Why `acceptEdits` is the shipping mode.** `acceptEdits` stops consulting the auto-mode classifier and auto-accepts file edits, but keeps the other gates. Calls listed in `permissions.allow` run, anything else parks as a permission question that policy or `answer_permission` settles, and every PreToolUse hook still runs, so `deny-prod-deploy.sh` keeps refusing prod deploys. Switching to it removes the classifier step and the per-edit prompt; `permissions.allow`, the permission-prompt tool and every hook still apply.
- **Why `bypassPermissions` stays out.** It turns off the permission-prompt path entirely: no park, no policy consult, no `ask` rules, and nothing for a manager or the operator to see. A manager is itself a model session. Letting it lift every approval gate on another model session with one call widens trust further than the operator approved. `DefaultPermissionMode` is `auto` rather than `bypassPermissions` for the same observability reason.
- **Why `plan` and `dontAsk` stay out.** No consumer has asked for them. `plan` stalls a worker that has to ship, and `dontAsk` silently denies everything outside the allow-list.
- `/mcp`'s `set_permission_mode`, the REST `POST /api/1.0/sessions/{id}/permission-mode` route, their reply shape `{"status":"changed","permission_mode":…}`, and their fail-fast lock behaviour are unchanged. The only change on those faces is that `auto` is now accepted (DB 1).
- The nine existing roster tools keep their names, arguments and behaviour.
- The roster face's attribution rule is unchanged: a call without `X-Claude-Code-Session-Id` is refused before dispatch.
- The audit-file path and its six-key line shape are a frozen interface for readers once shipped.
- `permissions.jsonl`'s key set and the spawn-ledger row shape are not touched.
- Assumes the sibling spec `bug-headless-spawn-drops-shipping-permission-mode` makes a headless `spawn_agent` with `shipping: true` start the child in the shipping mode. The refusal text in DB 4 names that route. This spec does not depend on it to pass its own ACs.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Child does not answer the control request (hung, or SDK schema drift on the response envelope) | The existing 30 s control-request timeout fires and the tool returns an error naming the mode. No audit line is written. | Operator or manager re-reads `GET /api/1.0/sessions` for the actual mode. If it is unchanged, retry once. If it changed anyway, the listing is authoritative and the switch is simply unaudited. | Tool `isError: true`. The listing shows the actual mode. |
| Child refuses the mode (SDK gates `auto` or renames `acceptEdits`) | Error result carrying the child's refusal verbatim. The mode is unchanged and no audit line is written. | A new spec revises the allow-list against the CLI's current `--permission-mode` choices. | Tool `isError: true` whose text contains `child refused`. |
| Worker exits between lookup and the control write | Error result naming the exited session. No audit line. | None needed. The worker is gone. | Tool `isError: true` containing `exited`. |
| Daemon crashes after the control write, before the reply | The child holds the new mode. There is no reply and no audit line. After restart, the adopted session replays its events file, and the stored control response restores the mode on the listing. | Operator reads `GET /api/1.0/sessions` for the actual mode. A missing audit line for it is a known gap for this window. | The listing mode differs from the last audit `to` for that agent. |
| Audit file unwritable (disk full, permissions) | The switch succeeds, the reply carries `recorded: false`, and the daemon log gets one `WARNING: permission mode change record …` line. | Operator fixes the path or disk. Later switches record normally. | Reply field `recorded: false` plus the WARNING line. |
| Two managers switch the same worker at once | The first takes the per-session lock. The second is refused with `running a sequence`. | The second caller re-reads the listing and retries if it still needs a different mode. | Tool `isError: true`. |
| Manager loops switching the same worker | Each call is a cheap control request and appends one audit line. There is no rate limit. | Operator reads the audit file, which shows the loop by `caller_session_id`. | Audit line count per `agent_id`. |
| Clock skew between daemon and readers | `ts` is the daemon's own clock, in UTC. Order within the file is append order, which stays authoritative. | None. | n/a |

## Security / Abuse Cases

- **What the caller controls:** `agent_id`, `mode`, and the session header. `mode` is checked against a closed two-value allow-list (`acceptEdits`, `auto`) before any lookup, so a crafted value such as `bypassPermissions`, a case variant or an empty string never reaches the child.
- **Trust boundary:** the face is loopback-only with the existing origin/rebinding guard, and the session header is self-asserted by the calling Claude process. The audit line records that claim, which is enough to attribute a misbehaving manager but is not authentication. That is unchanged from every other roster tool.
- **Widening ceiling:** the highest-trust mode reachable through this tool is `acceptEdits`, and every PreToolUse hook and `permissions.allow` rule still applies there. A compromised or confused manager can widen a headless worker from classifier-gated to allow-list-gated, and no further.
- **Logging hygiene:** the audit line carries ids and mode names only, never tool inputs. The refusal text echoes the requested `mode` back to the caller, which is the caller's own input. Exact refusal wording beyond the required substrings is decided by the agent at implementation time.
- **Hang:** bounded by the existing control-request timeout, and the lock is fail-fast.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Accept `auto` in the shared supervisor check, and update the README mode list | 1 | 7, part of 8 | none |
| 2 | Append-only permission-mode audit log (path, six-key shape, WARNING on failure) | 6 | 6 (log half) | none |
| 3 | Roster tool `set_agent_permission_mode`: allow-list, eligibility, lock, reply, audit call, daemon wiring of lock + audit log, CHANGELOG | 2, 3, 4, 5, 7 | 1–6, 8 | prompts 1, 2 |

Prompts 1 and 2 are independent leaves. Prompt 3 composes them on the roster face and wires them in the daemon. AC9 and AC10 run on the host after merge and release.

## Do-Nothing Option

Headless workers keep stalling on classifier denials for routine shipping steps. Every stall becomes an operator card whose only remedy is a keystroke in a pane that does not exist: 131 Shift+Tab closer lines in one week (2026-11-05..11; the separate 279 `/reload-plugins` lines are not addressed by this spec). The workarounds are respawning the worker in a different mode, which loses the turn's context and needs a working headless `shipping` path, or abandoning the task. The live control-request path already exists and is tested, so not exposing it to managers leaves a solved problem unsolved for the population that hits it.
