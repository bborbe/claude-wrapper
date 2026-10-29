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

Spawns a child. Body is optional: `{"cwd": "...", "model": "...",
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

### DELETE /api/1.0/sessions/{id}

Kills the child. `204` on success.

## Failure modes

| Status | Meaning |
|---|---|
| `400` | invalid spawn options — a malformed `env` key or value, a flag-shaped or empty `mcp_configs` entry, or a non-UUID `resume`. Nothing was spawned and no temp file was left behind |
| `404` | unknown session — the id was never spawned here, or has been forgotten |
| `409` | answered-already, or the session holds no open question to answer |
| `410` | child exited — it is gone and accepts no control |
| `503` | child unreachable — the process exists but its stream is not responding |

## Not in phase 1

Wrapping TUI sessions; multi-host supervision; auth beyond the localhost bind;
any control beyond `answer`.
