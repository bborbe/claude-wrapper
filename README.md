# claude-wrapper

Owns a `claude` child process and puts two faces on it: REST, and an MCP server
a Codex conversation can drive.

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

## MCP

`/mcp` is the client-facing MCP face: an MCP client drives a session through it
instead of speaking REST. It is served on the daemon's own mux and listener —
streamable HTTP, no second port and no second process.

⚠️ **Two MCP servers point in opposite directions.** `--mcp-stdio` is
*child-facing*: a stdio server the wrapper gives to its own child so the child
can ask for a permission verdict. `/mcp` is *client-facing*: a Codex
conversation driving a child. Different direction, different process boundary.

### Pointing a client at it

The config block below is the shape the spike used, verbatim. Any MCP client
that speaks streamable HTTP takes the same two lines:

```toml
[mcp_servers.claude-wrapper]
url = "http://127.0.0.1:18090/mcp"
```

For Codex, `codex mcp add` writes that block:

```bash
codex mcp add claude-wrapper --url http://127.0.0.1:18090/mcp
codex mcp list          # confirm: transport streamable_http, status enabled
```

Codex namespaces the tools it exposes as `mcp__<server_name>__<tool_name>`, so
the five below appear as `mcp__claude_wrapper__spawn_session` and so on. The
server name is chosen at `mcp add` time, not here.

### Tools

| tool | what it does |
|---|---|
| `spawn_session` | start one child; `cwd`, `model`, `permission_mode` are optional |
| `send_message` | write one user turn and return the assistant text that closed it — or, if the turn parks, the request it parked on |
| `list_sessions` | every session this wrapper owns |
| `get_session` | one session's state, model and pending question |
| `kill_session` | stop one child |
| `get_pending` | what a session is parked on, with the options its question carries; `null` when it is not waiting |
| `answer_pending` | answer that request: `behavior` for a permission, `answers` for a question |

`send_message` is the one tool that cannot delegate to REST: `POST
…/message` answers `202 {"status":"sent"}` and carries no answer, so the tool
waits for the child's `result` event and returns that event's `result` field. A
turn that never closes returns a tool error naming the timeout
(`--mcp-turn-timeout`, default 120s) rather than a success.

**A parked turn returns early.** If the child registers a request mid-turn, the
tool returns `parked: {…}` with the request inline instead of waiting for the
turn to close — the child's permission hold is 90 s, so a caller that only
learned of the question at turn-close could never answer inside it. Answer it
with `answer_pending`; the child then continues the same turn.

### Who may call it

`/mcp` is unauthenticated, so it is guarded structurally instead:

- **`Host` must be loopback** (`localhost`, `127.0.0.1`, `::1`), and a present
  `Origin` must be loopback too. This is not decoration — a localhost HTTP server
  is reachable by any page in the operator's browser through **DNS rebinding**:
  the attacker's hostname re-resolves to `127.0.0.1`, the request becomes
  same-origin, and the page can then read the replies. With `spawn_session` on
  the other end that is remote code execution as the daemon user. An absent
  `Origin` is allowed, because a non-browser client (Codex, curl) sends none.
- **The socket's own local address must be loopback too.** The `Host` header is
  client-supplied, so on a non-loopback bind (`--listen 0.0.0.0:18090`) any
  remote client could send `Host: localhost` and pass the header check. The
  local address is what the listener actually bound, so the guard follows it.
- **The body must be `application/json`.** A `text/plain` POST is a CORS "simple
  request" and skips preflight entirely, so the declared type is checked rather
  than assumed. An absent `Content-Type` is rejected for the same reason, and a
  body over 1 MiB is refused with `413` rather than truncated into a parse error.
- **`permission_mode` must be one of** `default`, `acceptEdits`,
  `bypassPermissions`, `plan`. An unrecognised value is refused rather than
  handed to the child.

This bounds the blast radius to a local caller. It is not authentication, and
"auth beyond the localhost bind" stays out of phase-1 scope.

### The permissive-spawn crutch

`spawn_session` defaults `permission_mode` to `bypassPermissions`. That is a
deliberate phase-1 choice, not a safe default: a real session hits a gate on its
first file write, and this phase exists to prove plain turns. It is removed in
**phase 2**, where the client sees the prompt and answers it instead of skipping
it.

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

Answers the request the session is parked on. The answer is written to the same
child that raised the request — the prompt is not reachable from anywhere else,
so answering a store would answer nothing.

A child is parked on one of **two shapes**, and they are not interchangeable.
Both arrive through the same child-facing `permission_prompt` tool; the tool
name is what tells them apart.

**Permission** — a tool call the gate refused. The answer is a verdict.

```
POST /api/1.0/sessions/8f2c/answer
{"request_id": "dmhm90gfxysg", "behavior": "deny"}

→ 200 {"status": "answered"}
```

The child then receives the verdict as its own tool result:

```
{"content": "denied by claude-wrapper", "is_error": true, "type": "tool_result"}
```

**Question** — an `AskUserQuestion`. The answer is a selection among the options
the question carries, keyed by the question text.

```
POST /api/1.0/sessions/8f2c/answer
{"request_id": "dmhmc9mc11z4", "answers": {"Which nonce?": "QUESTION-NONCE-9T2K"}}

→ 200 {"status": "answered"}
```

The child then receives:

```
{"content": "Your questions have been answered: \"Which nonce?\"=\"QUESTION-NONCE-9T2K\". You can now continue with these answers in mind.", "type": "tool_result"}
```

`GET /api/1.0/sessions/{id}/question/{request_id}` reads the pending request —
its `kind`, the payload the child sent, and the recorded answer once one exists:

```
{"answered": false, "kind": "", "behavior": "", "answers": null,
 "question": {"request_id": "dmhmc9mc11z4", "kind": "question",
              "tool_name": "AskUserQuestion",
              "questions": [{"question": "Which nonce?", "header": "Nonce",
                             "options": [{"label": "QUESTION-NONCE-9T2K",
                                          "description": "Select the token QUESTION-NONCE-9T2K."},
                                         {"label": "other",
                                          "description": "Provide a different nonce."}],
                             "multiSelect": false}]}}
```

`input.questions` is a **list**: one call can carry several questions, each with
its own `options[]`, `header` and `multiSelect`. A payload whose shape does not
match the pending request is refused with `422` and leaves the request
unanswered — see *Failure modes*.

### DELETE /api/1.0/sessions/{id}/question/{request_id}

Drops a request nothing is waiting on any more — the child-facing tool calls this
when its hold expires unanswered. `204` on success, `400` without a
`request_id`.

Without it a session keeps reporting an abandoned request: every later
`get_pending` says "parked" and every later `send_message` returns that stale
request instead of awaiting its own turn.

### POST /api/1.0/sessions/{id}/commands

Runs a named macro — a fixed, ordered list of user turns — against one child,
writing each step only after the previous turn has closed.

Body: `{"macro": "compact", "confirm": "<any non-empty value>"}`.

The endpoint takes a **macro name, never a command list**. What it can run is
therefore reviewable as a diff rather than supplied at call time, which matters
because these macros can rewrite a context or end a child.

| Macro | Steps | Confirm |
|---|---|---|
| `compact` | `/vault-cli:prepare-compact` → `/compact` → `/vault-cli:post-compact` | required |
| `terminal` | `/vault-cli:sync-progress` → `/vault-cli:session-close` → `/exit` | required |
| `probe` | `/cost` → `/cost` → `/cost` | not needed |
| `probe-slow` | `/cost` → *a long-generation prompt* → `/cost` | not needed |

`probe` and `probe-slow` are the endpoint's own smoke test: no step is
production-touching, so a sequence can be exercised against a real child
without spending a session. `probe-slow` exists to make a timer-driven
implementation distinguishable — its middle turn takes half a minute, and a
correct run still advances within milliseconds of that turn closing.

**Confirmation is mandatory for a macro that can change or end a child.** A
macro containing a production-touching step (`/compact`, `/clear`, `/exit`) is
refused with `409` unless `confirm` is supplied. Those macros are a remote kill
switch and are never fire-and-forget; the probe macros need no confirmation.

**The child must be idle.** A run started mid-turn is refused with `409`: the
in-flight turn's `result` would be read as step 1's close, so every later step
would fire one turn early.

**Writes are serialized per session.** The run and `POST …/message` share one
lock, so a plain message cannot land between two steps and be consumed as the
current step's close.

**A step that raises a permission question cannot be answered by the macro.** A
pending question does not change the child's state, so the step waits until
`stepTimeout` and the run aborts. That is deliberate — the sequence has no
authority to answer a gate — but it means a macro containing a tool that can
prompt should be run on a child whose permissions are already settled.

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
| `400` | invalid spawn options — a malformed `env` key or value, a flag-shaped or empty `mcp_configs` entry, or a non-UUID `resume`. Nothing was spawned and no temp file was left behind. Also an unknown macro name on `…/commands`, an out-of-set `kind` on a question registration, and a `DELETE …/question` with no `request_id` |
| `404` | unknown session — the id was never spawned here, or has been forgotten |
| `409` | answered-already, or the session holds no open question to answer, or a macro that needs confirmation was sent without `confirm`, or a sequence was started on a session that is not idle |
| `410` | child exited — it is gone and accepts no control |
| `422` | the answer's shape does not match the pending request — `answers` aimed at a permission request, or `behavior` aimed at a question. Nothing was recorded and the request is still unanswered |
| `503` | child unreachable — the process exists but its stream is not responding |

## Not in phase 1

Wrapping TUI sessions; multi-host supervision; auth beyond the localhost bind;
caller-supplied command lists; and any sequencing beyond a linear list — no
branches, loops or retries between steps.

The MCP face carries the same limits, with one exception since phase 2: a client
**can** now answer the permission prompt or `AskUserQuestion` a child raises —
`get_pending` reads the request with the options it carries, `answer_pending`
answers it, and `send_message` returns a parked turn early so the answer lands
inside the child's 90 s hold. What it still cannot do is change a running
session's mode or model (phase 3).

`spawn_session` still defaults to a permissive mode so gates never fire, and it
stays a default rather than a requirement: pass `permission_mode: "default"` to
let the gates fire and drive them through `get_pending` / `answer_pending`.

⚠️ An answer is only delivered if the **child** consumes it. A `200`, an
`ANSWERED` and a cleared board card all coexist with a session still parked —
measured 2026-10-29, when an answer was recorded and never delivered and a worker
sat on its modal for three hours. See
[`docs/regression-2026-10-29-answer-recorded-not-delivered.md`](docs/regression-2026-10-29-answer-recorded-not-delivered.md).

## License

BSD-style, see [LICENSE](LICENSE). Copyright (c) Benjamin Borbe.
