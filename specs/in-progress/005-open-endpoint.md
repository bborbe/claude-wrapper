---
status: prompted
approved: "2026-11-18T18:48:09Z"
generating: "2026-11-18T19:12:27Z"
prompted: "2026-11-18T19:12:27Z"
branch: dark-factory/open-endpoint
---

Task: bee8464c-9e24-4f63-9c67-0f0bd218f91e

## Summary

- Opening a task, goal, topic or the day's flagged batch is currently a long set of instructions that each session carries out by hand, differently every time.
- On 2026-11-18 one hand-run batch skipped the role check and the answer-path probe, and opened 13 agent rows as terminal tabs in the Hold window. Agent rows must never become tabs.
- The wrapper gets one open operation, reachable over REST and as an MCP tool. Its code reads each row's role, decides the mode, runs the answer-path probe, and opens the row the only way its role allows.
- Agent rows always resume or start headless and never get a window. Human rows open in the Direct window. Goal and topic managers open in the Managers window. Nothing opens in Hold.
- When the role map has no window for the role a row needs, the wrapper refuses and names the missing key. It never substitutes another window.

## Problem

`/supervisor:open` is an 834-line instruction set. A model reads it and re-implements it as shell calls: resolve role, write mode, run the probe, pick jump, resume or create, pick a window, then build a `wezterm cli spawn … --resume` line. Nothing at runtime checks the result.

On 2026-11-18 between 09:14 and 09:19 a `--flagged` batch ran as a hand-rolled `wezterm cli spawn --window-id 2 … --resume <sid>` loop that never read `role:` or `mode:`. Thirteen `role: agent` rows (panes 111, 113–121, 129–131) landed as tabs in the Hold window, and the answer-path probe never ran. The role × mode matrix forbids this: Rule 2 says agents are headless always, and Rule 4 says humans open in Direct. The rule is enforced by prose, not by a call, and a tab in Hold holds a spawn-cap slot while doing no work.

## Goal

The wrapper exposes one deterministic open operation, over `POST /api/1.0/open` and the MCP tool `open_rows`. It takes a list of rows (one for a single open, many for a flagged batch) and returns exactly one verdict per row, computed by code.

For each row the wrapper:
- resolves role and mode per `claude-supervisor/docs/role-mode-matrix.md`;
- decides jump / live / resume / create from its own session registry;
- runs the answer-path probe before any headless spawn it makes for a caller;
- performs the open.

Where each row opens:
- agent rows open headless only;
- human rows open as tabs in the role map's `Direct` window;
- goal and topic managers open in its `Managers` window.

A row the operation cannot open correctly is refused with a named reason before any side effect. The verdict fields and the decision order are specified in `docs/open-endpoint.md` (§ Verdict, § Decision order), which this spec treats as the contract.

## Non-goals

- **The other two specs.** Removing the wrapper's `agent → "Agents"` role-table entry and refusing agent tabs in the tab spawner is spec `agents-key-removal`. Rerouting the flagged reopen tick is spec `open-tick-reroute`. This spec only provides the executor's tick entry point (DB8, AC24) for it to call.
- **The `claude-supervisor` side.** The thin `/supervisor:open` caller, the PreToolUse hook, the regression replay in that repo, and the plugin's own `Agents` mappings are separate work.
- **Liveness classification.** Task "Wrapper Owns Fleet Liveness and Sweep Classification" owns `GET /api/1.0/liveness`. This operation reads the session registry directly and never consumes that verdict.
- **Flagged-row eligibility and ordering.** The caller sends rows already selected and ordered.
- **The caller's gates.** Gate 1 (approval), the goal/topic `phase: todo` gate and Gate 2 (the readiness audit, a model call) run before the call, unchanged.
- **Classifying a missing `role` from the task body** (matrix Rule 1 is a judgement). A missing role takes the default.
- **The goal branch's fallback to member tasks** when no manager can be opened. The wrapper refuses and the caller decides.
- **Porting `goal_loop: true` rows and non-local `target:` rows.** Both are refused by name.
- **Re-opening the 13 panes** from the 2026-11-18 batch.
- **Known limitation, accepted:** the duplicate-create guard is per-daemon and in-memory. A `create` whose session has not yet registered can be opened again by a later call. Closing that gap is a separate spec.
- **No knobs.** Do NOT add a fallback window, an "open interactive anyway" flag for agent rows, or a probe-skip flag. These invariants are the point. If a future consumer needs variation, that is a separate spec.
- **The real-fleet exercise** is the parent task's Definition of Done and is routed to the Fleet Manager.

## Assumptions

- **Role map:** `~/.cache/wezterm-role-map.json` carries keys `Managers`, `Direct` and `Hold`, each with `window_id` and `chip` (read 2026-11-18). The Agents window and key were removed by `~/.config/wezterm` commit `8acaf37` (2026-11-12).
- **Behaviour baseline:** `claude-supervisor` `commands/open.md` at plugin version 0.341.2 (Steps 0.6, 2A, 2B, 2C, 3). `docs/open-endpoint.md` ports its manager-create and manager-resume recipe.
- **Probe contract:** `claude-supervisor` `scripts/answer-path-check.py --session-id <id>` exits 0 on pass, and exits 1 with the reason on stdout to refuse.
- **Sequencing:** spec `agents-key-removal` lands first. This spec depends on it for file overlap only (both touch the tab spawner), not for behaviour.

## Desired Behavior

1. **One decision, two surfaces.** `POST /api/1.0/open` and `open_rows` take `{"rows": [{"kind", "vault", "name", "first_step"}]}`, delegate to the same code, and return one verdict per row in request order. One row's refusal never aborts the rows after it. A row's `first_step`, when present, is typed in front of its work command, separated by a blank line.
2. **Role and mode come from the matrix, in code,** in the order `docs/open-endpoint.md` § Decision order gives:
   - task `role`: absent or blank defaults to `agent`; an unrecognised value is refused; `manager` yields `yours`;
   - goals and topics are `manager` by kind;
   - mode is forced by role, and a stale agent `mode: interactive` is normalised and reported;
   - a goal or topic page declaring `mode: headless` is refused;
   - `goal_loop: true` and a non-local `target:` are refused; `shipping: true` selects the shipping permission mode.
3. **Write-back mirrors `/supervisor:open` Step 0.6.** A defaulted, absent or normalised task value is written with `vault-cli --vault <v> task set "<task>" role|mode <value>`, run as an argv. A write failure is logged and never changes the verdict.
4. **Branch from the wrapper's own registry.** The branches are `jump` (a human or manager row with a live pane: activate it), `live` (nothing opened), `resume` and `create`. A truncated id, or an unreadable registry, is refused rather than treated as "not live".
5. **Agent rows go headless, always, and are probed first.** An agent `resume` or `create` uses the in-process headless spawn, with the worker launcher's model and MCP configs, one opening work turn, and a spawn-ledger row. Before it, in request mode (DB8), the answer-path probe runs, keyed on `X-Claude-Code-Session-Id`. Any non-pass refuses with the probe's reason and the agent remedy, which never says "open interactive". The role map is never read for an agent row.
6. **Human and manager rows open as tabs only in their mapped window.**
   - Human rows go to `Direct`, and goal and topic managers to `Managers`.
   - A missing or unparseable file, a missing key, or a null `window_id` refuses with `role-map-key-missing`, naming the key.
   - A window equal to `Hold.window_id` refuses with `hold-window`.
   - A `resume` tab continues the recorded conversation.
   - Human tabs carry the work command. Manager tabs follow `docs/open-endpoint.md` § Manager rows.
7. **Every refusal is decided before the first side effect, and every verdict is observable.**
   - No spawn, pane activation, write or probe runs for a row whose refusal is already decidable.
   - Refusals from spawn caps, the start-rate limiter, the terminal-task guard and the launcher or ceiling scripts are reported as `spawn-refused` with their own message.
   - A concurrent open of one row is refused `in-flight`.
   - One log line per row lets the operator quote role, mode, path and window id from the wrapper log alone.
   - The three external script paths are daemon flags.
8. **The executor has two caller modes, and the mode is set by the entry point, never by the request.**
   - The **request** mode serves `POST /api/1.0/open` and `open_rows`. It runs everything above.
   - The **tick** mode exists for spec `open-tick-reroute`, so the flagged reopen tick can open rows through the same decision.
   - In tick mode the executor runs no answer-path probe: the tick is the daemon, not a session with a manager registration. It also activates no pane, so a row the decision classifies `jump` or `live` comes back with that branch and nothing opened.
   - Everything else is identical in both modes: role/mode resolution, write-back, window rules, refusals, spawns and the log line.
   - Neither HTTP nor MCP can select tick mode.
   - `docs/open-endpoint.md` § Decision order item 6 and § Answer-path probe record the same rule.

## Acceptance Criteria

The verdict fields, `branch`, `path` and `reason` tokens, the route path, the MCP tool name and the daemon flag names are the contract with the future thin caller and are frozen. `docs/open-endpoint.md` is their home.

**Surfaces**

- [ ] **AC1** — `make precommit` exits 0 in the worktree. Evidence: exit code.
- [ ] **AC2 — the REST route.** `POST /api/1.0/open` is registered on the daemon mux. A well-formed request returns `200` with one verdict per row, in request order. Each of these returns `400` and opens nothing:
  - empty `rows`;
  - unknown `kind`;
  - empty `name` or `vault`;
  - a `vault` that is not a configured vault;
  - a `name` containing `/`, `..` or a control character;
  - a non-JSON body;
  - a body over 1 MiB.

  A non-POST method returns `405`. Evidence: handler test output asserting each status, plus `grep -rn '/api/1.0/open' main.go pkg/handler` returning ≥ 1 line.
- [ ] **AC3 — the MCP tool.** The supervisor MCP face lists a tool named `open_rows` taking the same `rows` argument. Evidence: unit test output in which the MCP `tools/list` response contains `open_rows`.
- [ ] **AC4 — REST/MCP parity.** The same rows, sent through `POST /api/1.0/open` and through `open_rows` against identical fakes and the same calling session id, produce byte-identical verdict JSON. They also produce the same recorded spawner, tab and probe calls. The cases are one `resume` agent row, one `create` human row and one refused row. Evidence: parity test output.
- [ ] **AC5 — verdict shape.** Every verdict carries every field in `docs/open-endpoint.md` § Verdict, and only the token values listed there. Evidence: a handler test decodes a response and asserts each field name and token set from that table.

**Routing invariants**

- [ ] **AC6 — agent never gets a tab.** A `role: agent` task row goes through the headless spawn path, even when its stored `mode` is `interactive`. `Resume` equals the recorded `claude_session_id`, or is empty when none is recorded. The verdict reads `path: headless` and `mode: headless`, and in the stale case `mode_note: normalised interactive->headless`. The tab-spawner fake records 0 calls and the role-map reader fake is not consulted. Evidence: unit test output with both call counts.
- [ ] **AC7 — human and manager open in their mapped window.**
  - A `role: human` row opens a tab in `Direct.window_id`.
  - A goal or topic row opens a tab in `Managers.window_id`, named `<subject> Manager`, with chip `orange`.
  - On `resume` the tab starts with the recorded session id. On `create` it starts without one.
  - Goal `create` sends `/vault-cli:work-on-goal "<goal>"` and then `/supervisor:goal-loop "<goal>"`. Topic `create` sends `/supervisor:goal-loop "<topic>"`. Both are ready-gated, per `docs/open-endpoint.md` § Manager rows.

  Evidence: unit test asserting the window id, the resume id and the sent texts, in order.
- [ ] **AC8 — role-map key missing refuses, naming the key.** For a row needing `Direct` or `Managers`, each of these yields `branch: refused`, `reason: role-map-key-missing` and a message naming the key:
  - the map file is absent;
  - the key is absent;
  - `window_id` is null;
  - the file does not parse (the message also carries the parse error text).

  The tab fake records 0 calls. Under the same map, an agent row still opens headless. Evidence: unit test output, one case per condition.
- [ ] **AC9 — never Hold.** When the resolved `Direct` or `Managers` window id equals `Hold.window_id`, the row is refused with `reason: hold-window`, naming both keys. The tab fake records 0 calls. Evidence: unit test output.
- [ ] **AC10 — the answer-path probe gates every request-mode headless spawn.** Before a headless `resume` or `create` from the request entry point, the probe runs with the `X-Claude-Code-Session-Id` value. Each of these yields `branch: refused`, `reason: answer-path`, the probe's stdout as the message, and the remedy "arm this session's manager loop and re-run" (with no "interactive" in it):
  - the probe exits 1;
  - the probe fails to execute;
  - the header is absent.

  The headless spawner fake records 0 calls. The probe is not run for `jump`, `live`, `yours` or `tab` rows. Evidence: unit test output with the probe fake's call count per branch.

**Decision arms**

- [ ] **AC11 — role arms.**
  - An absent or blank task `role` gives `agent` with `role_source: defaulted`.
  - An unrecognised value gives `reason: undecidable-role`, naming the value.
  - `role: manager` on a task gives `branch: yours` and opens nothing.
  - A goal or topic gives `manager` with `role_source: kind`, and a page declaring `mode: headless` gives `reason: manager-mode-headless`.

  Evidence: unit test output, one case per arm.
- [ ] **AC12 — row, session and unported-field arms.** Each of these refuses with 0 spawner calls and 0 probe calls:
  - `missing-row`: no page of that kind in that vault;
  - `row-unreadable`: the page read failed;
  - `truncated-id`: the recorded id is not a full 36-character UUID;
  - `liveness-unreadable`: the registry is unreadable;
  - `goal-loop-unsupported`: task `goal_loop: true`;
  - `target-unsupported`: task `target:` other than absent or `local`.

  `shipping: true` reaches the headless spawn with the shipping permission mode. Evidence: unit test output, one case per arm.
- [ ] **AC13 — branch selection.** Against a registry fake:
  - live with a pane, on a human or manager row → `jump`, the pane is activated, nothing spawns;
  - live with no pane, or any live agent row → `live`, nothing spawns;
  - recorded but not live → `resume`;
  - nothing recorded → `create`;
  - a goal or topic with no recorded id but a live entry named `<name> Manager` → `jump`.

  Evidence: unit test output, one case per bullet.
- [ ] **AC14 — write-back via vault-cli.** When a task's `role` was defaulted, or its `mode` was absent or normalised, the command-runner fake receives the argv `vault-cli --vault <v> task set "<task>" role <r>` and/or `… mode <m>` before the spawn. When the runner errors, a `WARNING` is logged and the verdict is unchanged (`resume` in the test). Goal and topic rows trigger no write. Evidence: unit test asserting the argv and the failure case.
- [ ] **AC15 — concurrent open of one row.** Two concurrent requests for the same `vault/kind/name` in one daemon produce one spawn, and the second is refused `in-flight`. Evidence: unit test with a blocking spawner fake, spawner call count 1.
- [ ] **AC16 — one log line per row.** Every verdict writes exactly one line in the form given in `docs/open-endpoint.md` § Verdict. Evidence: unit test capturing the logger for an agent row (`path=headless window=none`) and a human row (`path=tab window=<Direct id>`).
- [ ] **AC17 — the 2026-11-18 batch replay.** One request carries 15 rows: 13 `role: agent` task rows (at least 5 with stored `mode: interactive`, all with a recorded non-live session), 1 `role: human` row and 1 goal row. The role map has `Direct`, `Managers` and `Hold`, and no `Agents` key. The test asserts:
  - 13 headless resumes;
  - 0 tab spawns for agent rows;
  - 2 tab spawns in total;
  - 0 tab spawns whose window id equals `Hold.window_id`.

  The test fails against a router that selects the path by stored `mode`, and the prompt records that mutation run's failing output once. Evidence: test output from both runs.
- [ ] **AC18 — batch continuation.** In a 3-row request whose middle row is refused (`missing-row`), rows 1 and 3 are opened and verdicts 1–3 are returned in order. The spawner fakes record calls for rows 1 and 3 only. Evidence: unit test output.
- [ ] **AC19 — spawn-refused passes the component's message through.** Each of these yields `branch: refused`, `reason: spawn-refused` and the refusing component's own message text unchanged:
  - the spawn-cap fake refuses;
  - the terminal-task resume guard refuses, on a task already `completed`;
  - `manager-launcher.py` exits non-zero.

  No tab and no headless child exists afterwards. Evidence: unit test output, one case each.
- [ ] **AC20 — `first_step`.** With `first_step: "X"`, the agent headless opening turn and the human tab's work command each equal `X\n\n/vault-cli:work-on-task "<task>"`. Without it, each equals the bare command. Evidence: unit test asserting the sent text.
- [ ] **AC21 — daemon flags for the script paths.** `grep -nE 'answer-path-check-script|manager-launcher-script|topic-spawn-script' main.go` returns ≥ 3 lines, and each flag's default is the marketplace-clone path. Evidence: the grep output, plus unit test output asserting each flag's default value.

**Docs**

- [ ] **AC22** — `README.md` gains a `### POST /api/1.0/open` section linking to `docs/open-endpoint.md`, and an `open_rows` row in the MCP tools table. `docs/open-endpoint.md` gains a `## Daemon flags` section naming the three flags. Evidence: `grep -n '^### POST /api/1.0/open' README.md`, `grep -n 'open_rows' README.md` and `grep -n '^## Daemon flags' docs/open-endpoint.md` each return ≥ 1 line. All three return 0 lines before the work.
- [ ] **AC23** — `CHANGELOG.md` has a bullet under `## Unreleased` naming the open endpoint. Evidence: `awk '/^## /{s=$0} /open endpoint|\/api\/1.0\/open/{print s}' CHANGELOG.md` prints `## Unreleased`.

**Caller mode**

- [ ] **AC24 — tick caller mode.** The executor's tick entry point is fed the same rows as the AC13 jump case and an agent `resume` row. The jump row returns `branch: jump` with 0 pane activations. The agent row makes one headless resume with 0 probe calls, even with no calling session id. The same rows through the request entry point make 1 pane activation and 1 probe call. No request field (REST body or `open_rows` argument) changes the mode: a request whose row carries an extra `"mode": "tick"` field behaves exactly as request mode (1 pane activation, 1 probe call). Evidence: unit test output with the pane-activation and probe call counts for both entry points.

Scenario coverage: none. Every behaviour is reachable with fakes for the tab client, supervisor, registry, probe, command runner and role map. The real-fleet exercise is the parent task's manager-run DoD.

## Verification

### Container-executable

```
make precommit
go test ./pkg/... -run Open -v
grep -rn '/api/1.0/open' main.go pkg/handler
grep -nE 'answer-path-check-script|manager-launcher-script|topic-spawn-script' main.go
grep -n '^### POST /api/1.0/open' README.md
grep -n 'open_rows' README.md
grep -n '^## Daemon flags' docs/open-endpoint.md
awk '/^## /{s=$0} /open endpoint|\/api\/1.0\/open/{print s}' CHANGELOG.md
```

## Constraints

- **The rules are ported, never restated differently.** They come from `claude-supervisor/docs/role-mode-matrix.md` (Rules 1, 2, 4 and § Answer-path remedy) and the `commands/open.md` 0.341.2 baseline. "Undecidable role" means an unrecognised value; absent or blank takes the default, per the matrix.
- **`docs/open-endpoint.md` is the single home** of the verdict table, the token sets, the decision order and the manager recipe. This spec references it rather than duplicating it. A change to any of them is a change to that page.
- **The role map is read on every call and never cached.** Which window each key names is the operator's content and is not re-decided here.
- **One implementation per external check.** `answer-path-check.py`, `manager-launcher.py` and `topic-spawn.py` are invoked, never re-derived, from their daemon-flag paths, matching `--flagged-rows-script`.
- **The calling session id** travels in the existing `X-Claude-Code-Session-Id` header on both surfaces.
- **Existing guards apply to every spawn this operation makes,** with none bypassed or duplicated: spawn caps, the start-rate limiter, the terminal-task resume guard, spawn-ledger filing, and the tab client's pane hygiene (exit recorder, entrypoint, inherited-session unset).
- **Existing routes are unchanged,** and so are `spawn_agent`, `spawn_session` and the flagged reopen tick. The tab resume added here is reachable only from the open operation.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| Role map absent, unparseable, or `Direct`/`Managers` key missing | Tab-bound row refused `role-map-key-missing`, naming the key (plus the parse error when unparseable); agent rows in the batch still open | Operator fixes the map; re-run | Verdict; log `reason=role-map-key-missing` |
| Map gives `Direct`/`Managers` Hold's window id | Refused `hold-window` | Map bug: operator fixes the map | Log line |
| Probe script missing, crashing or timing out | Headless row refused `answer-path`; never treated as pass | Arm the caller's manager loop; re-run | Log `reason=answer-path` |
| Session registry unreadable | Refused `liveness-unreadable`; no resume or create | Re-run once readable | Log line |
| Vault read fails | `row-unreadable`; other rows proceed | Fix the vault/CLI; re-run | Verdict |
| `vault-cli task set` fails | `WARNING` logged; open proceeds; the next call re-derives the same value | None needed | `WARNING` line |
| `manager-launcher.py` or `topic-spawn.py` refuses | `spawn-refused` with the script's own line; no tab | The caller decides (fallback to tasks stays the caller's) | Verdict |
| Cap, start-rate or terminal-task refusal mid-batch | That row `spawn-refused`; later rows still attempted | Wait for a slot / nothing (terminal) | Verdict |
| Headless opening turn fails | Child stopped, not orphaned; `spawn-refused` | Re-run | Log line |
| Pane activation fails on `jump` | `spawn-refused` naming the pane; nothing spawned | Focus by hand | Verdict |
| Concurrent open of one row, same daemon | Second refused `in-flight` | Re-run | Verdict |
| Duplicate create across calls before the first session registers (accepted limitation) | A second session may open for the row | Operator closes the extra | Two ledger rows for one label |
| **Best-effort:** a manager start command never reaches a ready composer | Tab stays open; verdict stays `create`/`resume` with the timeout in the message; later commands not sent. No retry | Operator sends the command in the tab | Log line |
| **Best-effort:** the client disconnects mid-batch | Rows already spawned finish their bookkeeping on a detached context; rows not yet reached are not opened. No resumption | Re-run; opened rows now read `jump`/`live` | Log lines stop at the last row |

## Security / Abuse Cases

- **Both surfaces share the loopback listener's structural guards** the README documents for `/mcp`: loopback `Host` and socket, `application/json`, a 1 MiB body cap.
- **Caller-controlled `vault`, `kind` and `name` are validated.** `kind` is a closed set. `vault` must be a configured vault. `name` reaches `vault-cli`, `manager-launcher.py` and `topic-spawn.py` as one argv element, never through a shell. A name with `/`, `..` or a control character is refused `400`.
- **`first_step` is composer text only.** It is typed into a worker's composer, bounded by the body cap, and never placed on a command line.
- **Recorded session ids are UUID-validated** (`truncated-id`) before reaching any spawn argument.
- **Probe, scripts and spawns stay bounded.** Each runs under its existing timeout, and the operation adds no retry loop.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Pure open decision: row facts (role, stored mode, kind, unported fields, recorded id, registry state, role map) → verdict incl. window resolution and every refusal reason, per `docs/open-endpoint.md` § Decision order; no side effects | 2, 4, 6 (window rules), 7 (ordering) | 5, 8, 9, 11, 12, 13 | — |
| 2 | Tab resume capability plus the manager create/resume recipe from `docs/open-endpoint.md` § Manager rows (launcher and ceiling scripts, ready-gated start commands) | 6 | 7 | spec `agents-key-removal`, for file overlap only (both edit the tab spawner); no behavioural dependency |
| 3 | Open executor (request and tick caller modes): vault row read, `vault-cli task set` write-back, probe invocation, routing to headless spawn, tab spawn or pane activation, `first_step`, in-flight guard, spawn-refused pass-through, log line, batch continuation, 15-row replay with mutation run | 1, 3, 5, 6, 7, 8 | 6, 10, 14, 15, 16, 17, 18, 19, 20, 24 | prompts 1, 2 |
| 4 | Surfaces: REST route with body and vault validation, `open_rows` MCP tool, parity test, daemon wiring and the three script-path flags, README section, `## Daemon flags` doc section, CHANGELOG | 1, 7 | 1, 2, 3, 4, 21, 22, 23 | prompt 3 |

Rationale: the decision function is the correctness surface and has no side effects, so it lands first. The tab resume is an independent capability. The executor composes both with the existing spawn paths. The surfaces only expose the executor.

## Do-Nothing Option

Every open stays a model re-implementing 834 lines of instructions. The 2026-11-18 failure (13 agent tabs in Hold, probe skipped) recurs whenever a session takes a shortcut, because no runtime check exists. Each recurrence holds spawn-cap slots doing no work and parks headless gates where nobody answers them. The thin caller, the hook and the regression replay in `claude-supervisor` have no endpoint to point at. Not acceptable.
