# claude-wrapper

Owns a `claude` child process and puts a REST face on it.

A store that observes sessions someone else started can *report* a question. Only
the process holding the child can *answer* one. That is the whole reason this
service exists: it spawns the child, so it can act on it.

**Phase 1 wraps only the children it spawns.** A TUI session has no duplex
channel — `--input-format` and `--output-format stream-json` are `--print`-only —
so a tab session can be watched, not controlled. Joining
`attention-controller`'s stores is phase 2 and lives in a separate task.

## Run

```bash
make run            # localhost:18090
make test
make precommit      # the gate
```

## REST

Every business route is under `/api/1.0/`. `/healthz` touches no child.

### GET /api/1.0/sessions

Lists every child this process spawned. Each row carries `id`, `state`, `model`,
`model_source`, `question`, `controls`, `cwd`, `started_at`, `exit_code`.

`state` is derived from the child's own event stream — `generating` while an
assistant turn is open, `idle` when it closes, `exited` when the child leaves.
It is never derived from the age of the last event: that reads a session which
has been generating for forty minutes as idle, because its last *completed*
message is forty minutes old.

`model` is the model that actually answered, taken from the `assistant` event's
`message.model`, and `model_source` names where it came from. The `system`/`init`
event also carries a `model`, but that is the launcher's configuration and can
differ from what served the request — measured 2026-10-28: init said
`claude-opus-5-5` while the assistant event said `deepseek-v4-flash-max`.

`controls` declares what a caller may do to that session. A wrapper that reports
every session as answerable invites callers to attempt an answer that cannot
land.

### GET /api/1.0/sessions/{id}

One session. `404` when the id is unknown.

### POST /api/1.0/sessions

Spawns a child. Every field in the body is optional, but the body itself must be
present — an empty request is rejected as `400 decode body failed`:
`{"cwd": "...", "model": "...",
"permission_mode": "...", "permission_prompt": "...", "env": {...},
"resume": "<session-id>", "mcp_configs": ["..."]}`. Returns `201` with the
session.

- `env` is **merged over** this process's environment, never substituted for it:
  the child keeps `PATH` and `HOME`, and an override replaces its inherited key
  rather than appearing twice.
- `resume` continues an existing conversation. The child's claude session id is
  then the resumed one, not the id this endpoint returns — read it from the
  child's own `init` event. It must be a UUID.
- `mcp_configs` are extra MCP config files, loaded alongside the generated one.
  This is how a launcher's own `--mcp-config` file reaches the child: it arrives
  as a CLI flag, not as a setting, so no setting source can reach it.

### POST /api/1.0/sessions/{id}/answer

Answers the permission request the session is parked on.
Body: `{"request_id": "...", "behavior": "allow"}`. The answer is written to the
same child that raised the request — the prompt is not reachable from anywhere
else, so answering a store would answer nothing.

### POST /api/1.0/sessions/{id}/commands

Runs a named macro — a fixed, ordered list of user turns — against one child,
writing each step only after the previous turn has closed.

Body: `{"macro": "compact", "confirm": "<any non-empty value>"}`.

The endpoint takes a **macro name, never a command list**. What it can run is
therefore reviewable as a diff rather than supplied at call time, which matters
because these macros can rewrite a context or end a child.

| Macro | Steps |
|---|---|
| `compact` | `/vault-cli:prepare-compact` → `/compact` → `/vault-cli:post-compact` |
| `terminal` | `/vault-cli:sync-progress` → `/vault-cli:session-close` → `/exit` |

**Confirmation is mandatory.** Every macro contains a production-touching step
(`/compact`, `/exit`), so a call without `confirm` is refused with `409` rather
than started. This endpoint is a remote kill switch; it is never
fire-and-forget.

**The child must be idle.** A run started mid-turn is refused with `409`: the
in-flight turn's `result` would be read as step 1's close, so every later step
would fire one turn early. Runs are also serialized per session, so two
concurrent calls cannot consume each other's turns.

**The call blocks until the run finishes** and answers with every step, so the
response is the only report:

```json
{"macro": "compact", "aborted": false, "steps": [
  {"index": 1, "command": "/vault-cli:prepare-compact", "status": "done",
   "wrote_at": "2026-10-29T08:12:01Z", "closed_at": "2026-10-29T08:12:44Z"},
  {"index": 2, "command": "/compact", "status": "done",
   "wrote_at": "2026-10-29T08:12:44Z", "closed_at": "2026-10-29T08:15:10Z"},
  {"index": 3, "command": "/vault-cli:post-compact", "status": "done",
   "wrote_at": "2026-10-29T08:15:10Z", "closed_at": "2026-10-29T08:15:52Z"}
]}
```

Note the gaps: each step's `wrote_at` lands within milliseconds of the previous
step's `closed_at`, while the turns themselves take 43 s and 146 s. That is the
advance rule visible in the response.

`status` is `done`, `running`, `not_run` or `aborted`. A run that stops early
sets `aborted` with a `reason`, and the steps it never reached stay `not_run`.

**Advance is driven by the child, not a timer.** Step *N+1* is written when the
child's `result` event for turn *N* arrives, never on an interval. The gap
between a step's `closed_at` and the next step's `wrote_at` therefore stays near
zero however long a turn takes — which is exactly what a sleep-driven sequence
cannot produce.

### DELETE /api/1.0/sessions/{id}

Kills the child. `204` on success.

## Failure modes

| Status | Meaning |
|---|---|
| `400` | invalid spawn options — a malformed `env` key or value, a flag-shaped or empty `mcp_configs` entry, or a non-UUID `resume`. Nothing was spawned and no temp file was left behind. Also an unknown macro name on `…/commands` |
| `404` | unknown session — the id was never spawned here, or has been forgotten |
| `409` | answered-already, or the session holds no open question to answer, or a macro that needs confirmation was sent without `confirm`, or a sequence was started on a session that is not idle |
| `410` | child exited — it is gone and accepts no control |
| `503` | child unreachable — the process exists but its stream is not responding |

## Not in phase 1

Wrapping TUI sessions; multi-host supervision; auth beyond the localhost bind;
caller-supplied command lists; and any sequencing beyond a linear list — no
branches, loops or retries between steps.
