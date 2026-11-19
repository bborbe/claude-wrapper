# The open endpoint

`POST /api/1.0/open` and the MCP tool `open_rows` open a task, goal or topic the way its role
requires, and return one verdict per row. The flagged reopen tick routes its rows through the
same decision (spec `open-tick-reroute`). This page is the contract with the caller
(`claude-supervisor`'s `/supervisor:open`) and holds the recipes the decision executes. The specs
are `specs/open-endpoint.md`, `specs/agents-key-removal.md` and `specs/open-tick-reroute.md`.

The rules are not owned here. Role × mode comes from `claude-supervisor/docs/role-mode-matrix.md`
(Rules 1, 2, 4 and § Answer-path remedy). The open branches come from `claude-supervisor`
`commands/open.md` (plugin 0.341.2), Steps 0.6, 2A, 2B, 2C and 3. This page records how the
wrapper executes those rules. It does not re-decide them.

## Request

```json
{"rows": [{"kind": "task", "vault": "private-personal", "name": "<title>", "first_step": "<optional text>"}]}
```

- `kind` is `task`, `goal` or `topic`.
- `vault` is a configured vault name.
- `name` is the page title. It must contain no path separator and no control character.
- `first_step` is typed in front of the work command, separated by a blank line. It mirrors
  `/supervisor:open --first-step`.
- One row is a single open. Many rows are a flagged batch, processed in request order.
- The caller's own session id travels in `X-Claude-Code-Session-Id`. It is what the answer-path
  probe checks.

## Verdict

| Field | Values |
|---|---|
| `kind`, `vault`, `name` | echoed from the request |
| `role` | `agent` · `human` · `manager` |
| `role_source` | `on-disk` · `defaulted` · `kind` (goal/topic) |
| `mode` | `headless` · `interactive` |
| `mode_note` | empty, or `normalised interactive->headless` |
| `branch` | `jump` · `live` · `resume` · `create` · `yours` · `refused` |
| `path` | `headless` · `tab` · `none` |
| `window_key`, `window_id` | role-map key and id; set only when `path` is `tab` |
| `session_id`, `pane_id` | set when known |
| `reason`, `remedy` | set only when `branch` is `refused` |
| `message` | free text: the refusal's detail, or a best-effort start-command note on an opened tab |

A field that is not decided for a row (a row refused at the page read has no role yet) is the empty string; `window_id` is null unless the row opened a tab.

`reason` tokens: `missing-row`, `row-unreadable`, `undecidable-role`, `manager-mode-headless`,
`truncated-id`, `liveness-unreadable`, `answer-path`, `role-map-key-missing`, `hold-window`,
`goal-loop-unsupported`, `target-unsupported`, `in-flight`, `spawn-refused`.

Each verdict also writes one log line:

```
open: row=<vault>/<kind>/<name> role=<r> mode=<m> branch=<b> path=<p> window=<id|none> reason=<reason|->
```

## Response

```json
{"verdicts": [<verdict>, ...]}
```

One verdict per request row, in request order. A malformed request is `400` on REST and `isError`
on `open_rows`, with nothing opened.

## Decision order

The first match wins. Every refusal is decided before the first side effect.

1. Read the page. No page → `missing-row`. Read error → `row-unreadable`.
2. Role:
   - task, absent or blank `role` → `agent`, `defaulted`;
   - task, unrecognised value → `undecidable-role`;
   - task, `role: manager` → `yours`;
   - goal or topic → `manager`, `kind`.
3. Mode by Rule 2:
   - agent → `headless`, and a stored `interactive` is normalised;
   - human → `interactive`;
   - manager → `interactive`, and a goal or topic page declaring `mode: headless` → `manager-mode-headless`.
4. Unported fields: task `goal_loop: true` → `goal-loop-unsupported`. Task `target:` other than
   absent or `local` → `target-unsupported`.
5. Recorded `claude_session_id` that is not a full 36-character UUID → `truncated-id`. Registry
   unreadable → `liveness-unreadable`.
6. Branch:
   - live with a pane, on a human or manager row → `jump`;
   - live otherwise, including any live agent row → `live`;
   - recorded but not live → `resume`;
   - nothing recorded → `create`.

   A goal or topic with no recorded id but a live registry entry named `<name> Manager` →
   `jump`. The tick path never jumps; it records `jump` and `live` as skipped.
7. Headless rows (agent `resume` or `create`) run the answer-path probe first, except on the tick
   path. Tab rows resolve their window:
   - `Direct` for human, `Managers` for manager;
   - file, key or `window_id` absent → `role-map-key-missing`, naming the key;
   - window id equal to `Hold.window_id` → `hold-window`.
8. Write back a defaulted `role` or an absent/normalised `mode` on task rows, then spawn. Spawn
   caps, start-rate, the terminal-task guard and any launcher/ceiling script refusal →
   `spawn-refused`, with the refusing component's own message.

## Answer-path probe

```
<python3> <answer-path-check.py> --session-id <X-Claude-Code-Session-Id>
```

- Exit 0 passes. Exit 1 refuses, with the reason on stdout.
- An exec error, a timeout, or an absent header also refuses.
- The remedy is always: *arm this session's manager loop and re-run*. It is never "open
  interactive", because that is the tab Rule 2 forbids for an agent.
- The script path is a daemon flag that defaults to the marketplace clone, matching
  `--flagged-rows-script`.

The flagged reopen tick runs no probe. It is the daemon itself, not a session with a manager
registration, so the probe has no session to check. This matches the tick's behaviour before
this endpoint.

## Write-back

```
vault-cli --vault <vault> task set "<task>" role <agent|human|manager>
vault-cli --vault <vault> task set "<task>" mode <headless|interactive>
```

- Run as an argv with no shell, only for task rows, only when the value was defaulted, absent or
  normalised. This mirrors `/supervisor:open` Step 0.6.
- A failure is logged as `WARNING` and never blocks the verdict. The next call derives the same
  value again.

## Agent rows: headless resume and create

Agent rows use the same path the flagged reopen uses:

- the in-process supervisor spawn with the vault worker launcher's model and MCP configs;
- `Resume` set to the recorded session id, or empty on `create`;
- the shipping permission mode when the task's `shipping:` reads `true`;
- one opening turn, `<first_step>\n\n/vault-cli:work-on-task "<task>"`;
- a spawn-ledger row;
- a child whose opening turn fails is stopped, not orphaned.

The role map is never read.

## Human rows: Direct tab

- Window: `Direct.window_id`.
- Chip: the vault family's chip, as `ChipForRole` resolves it.
- Launcher per matrix Rule 4: the task's own `launcher:`, else the `-claude` sibling of
  `claude_script`, else `manager_launcher`, else `claude_script`.
- `create`: the work command `<first_step>\n\n/vault-cli:work-on-task "<task>"`, sent
  ready-gated after `/color <chip>`.
- `resume`: the tab starts the launcher with `--resume <session_id> -n "<task>" "/color <chip>"`
  and then sends the same work command, ready-gated.

## Manager rows: goal and topic recipe

Ported from `commands/open.md` Steps 2B item 4 and 2C item 4. Window `Managers.window_id`, chip
`orange`, tab name `<subject> Manager`.

**Launcher.**

```
<python3> manager-launcher.py "<manager_launcher absolute path>" "<goals_or_topics_dir>/<subject>.md" --default-source manager-default
```

- It prints `<launcher>\t<source>`.
- A non-zero exit → `spawn-refused` with its output. The wrapper does not fall back to the
  goal's member tasks; that choice stays with the caller.
- `<subject>` reaches argv as one element, never through a shell. A subject containing `/` or
  `..` was already refused at request validation.

**Topic ceiling (topic `create` only).**

```
<python3> topic-spawn.py --vault "<vault root>" --topic "<topic>" --topic-open --hand-run
```

- Exit 0 with `open-ok: <topic>` → spawn.
- Anything else (`manager-cap`, `topic-unflagged`, …) → `spawn-refused` with its second line.

**Spawn.** The tab starts `<launcher> -n "<subject> Manager" "/color orange"`, with the existing
pane hygiene the tab client applies: exit recorder, `CLAUDE_CODE_ENTRYPOINT=cli`, and the
inherited-session variables unset. On `resume` it starts
`<launcher> --resume <session_id> -n "<subject> Manager" "/color orange"`.

**Start commands.** Every send waits on the wrapper's pane readiness verdict, the same gate
`SendWhenReady` uses. It is never a fixed sleep. A send that never reaches a ready composer is
not made.

| Subject | Commands, in order | Ready wait before each |
|---|---|---|
| goal | `/vault-cli:work-on-goal "<goal>"`, then `/supervisor:goal-loop "<goal>"` | 60 s, then 300 s |
| topic | `/supervisor:goal-loop "<topic>"` | 60 s |

- A start-command timeout is logged and reported in the verdict message. The tab is open, so the
  branch stays `create`/`resume` rather than `refused`.
- `resume` sends the same commands: a resumed manager with no turn is idle, and its loop must
  re-arm.

## Daemon flags

Each defaults to the deployed marketplace clone the running fleet uses, matching
`--flagged-rows-script`.

| Flag | Default | Script it locates |
|---|---|---|
| `--answer-path-check-script` | `~/.claude/plugins/marketplaces/claude-supervisor/scripts/answer-path-check.py` | the answer-path probe |
| `--manager-launcher-script` | `~/.claude/plugins/marketplaces/claude-supervisor/scripts/manager-launcher.py` | resolves a goal or topic manager's launcher |
| `--topic-spawn-script` | `~/.claude/plugins/marketplaces/claude-supervisor/scripts/topic-spawn.py` | the topic manager ceiling checked before a topic create |

## Known limitations

- **Duplicate create across calls.** The `in-flight` guard is per-daemon and in-memory. A
  `create` whose session has not yet registered can be opened a second time by a later call. The
  flagged tick's recently-spawned ledger guard covers the tick path only.
- **`goal_loop: true` and non-local `target:`** are refused rather than ported.
- **The probe exemption on the tick path** (above).
