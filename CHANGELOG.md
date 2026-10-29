# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.4.1

- fix: **the event stream no longer dies at the server's 60 s `WriteTimeout`.** `GET
  …/sessions/{id}/events` is an SSE response that can legitimately write nothing for
  minutes — a child parked on a permission prompt says nothing until it is answered — but
  `main.go` sets `WriteTimeout: 60s`, and Go applies that as a connection deadline at the
  moment the request is read. A deadline closes nothing: it makes the **next write** fail,
  so the stream looks healthy right up to the first event after the 60 s mark, and that
  write then ends the response. A subscriber sees a body cut mid-stream, which a Node
  client reports as `TypeError: terminated` — how a healthy child gets recorded as a dead
  worker. `handleEvents` now clears the deadline, exactly as the macro route and the MCP
  tool already did. Measured before the fix: a stream opened at `t0`, a turn sent after the
  deadline, `curl: (18) transfer closed with outstanding read data remaining` at
  `time_total=73.5 s` with `size` unchanged since the replay. Also: the response headers are
  now flushed before the first frame, so a subscriber to a child with an empty replay can
  tell "connected and quiet" from "not connected yet".

## v0.4.0

- feat: `pkg.Supervisor` gains `ForgetQuestion`, and `DELETE …/question/{request_id}` exposes it.
  This is an addition to an exported interface, so an out-of-tree implementer must add the method.
- fix: a request whose hold expired is now dropped (`DELETE …/question/{request_id}`), instead of
  being reported as pending for the life of the child. Without it every later `get_pending` said
  "parked" and every later `send_message` returned that stale request instead of awaiting its own
  turn.
- fix: an `AskUserQuestion` that expires unanswered now returns a deny naming the expiry, rather
  than the permission default of allow — which let the child proceed with no answers at all,
  silently.
- fix: a registration carrying an out-of-set `kind` is rejected with `400` instead of creating a
  request no answer shape can ever match.
- feat: `get_pending` and `answer_pending` join the client-facing MCP face, so a caller can see
  what a session is parked on — including an `AskUserQuestion`'s options — and answer it without a
  second round-trip or a REST call.
- fix: `send_message` returns as soon as the child registers a request, instead of waiting for the
  turn to close. The child's permission hold is 90 s, so a caller that only learned of the question
  at turn-close could never answer inside it — the recorded-but-never-delivered failure this
  release exists to remove.
- fix: every child was pointed at `http://localhost:18090` regardless of the daemon's own
  `--listen`. A wrapper on any other port handed its children the URL of a *different* process, so
  registrations failed silently and the gate defaulted to allow. The URL is now derived from
  `--listen` via `pkg.SelfURL`, which also maps a bare port (`:18090`) and a wildcard host
  (`0.0.0.0`, `::`) to loopback — both are valid listen targets whose naive prefix the child's
  HTTP client rejects.
- fix: the permission tool's `updatedInput` for an `AskUserQuestion` must satisfy the tool's own
  input schema, so it now echoes the original `questions` back beside `answers`. Returning
  `answers` alone made the child reject the answer with `The required parameter "questions" is
  missing`.
- feat: the pending registry carries a kind-discriminated payload instead of a bare allow/deny
  string. A permission request keeps its tool name and input; an `AskUserQuestion` keeps its
  `questions[]` list — each prompt with its own `options[]`, `header` and `multiSelect` — so a
  selection can be recorded rather than coerced into a verdict. Both shapes arrive through the same
  child-facing `permission_prompt` tool; the tool name is what tells them apart.
- feat: `POST /api/1.0/sessions/{id}/answer` accepts `{"request_id","answers":{…}}` for a question
  alongside the existing `{"request_id","behavior"}` for a permission, and
  `GET …/question/{request_id}` returns the full payload with the recorded answer.
- feat: an answer whose shape does not match the pending request returns **422 Unprocessable
  Entity** through a new `ErrAnswerShapeMismatch`, distinct from the existing 409 for an unknown or
  already-answered request, and leaves the request unanswered.

## v0.3.0

- feat: serve a client-facing MCP face at `/mcp` on the daemon's own mux, over streamable HTTP, so an
  MCP client (Codex) can drive a Claude session instead of only REST. Five tools —
  `spawn_session`, `send_message`, `list_sessions`, `get_session`, `kill_session`. This is the
  opposite direction from `--mcp-stdio`, which is child-facing: that one gives a child a permission
  tool, this one lets a client drive a child.
- feat: `send_message` returns the assistant text that closed the turn, taken from the child's
  `result` event. The REST `POST …/message` route answers `202 {"status":"sent"}` and carries no
  answer, so this is the one tool that cannot delegate to it.
- feat: a turn that never closes returns a tool error naming the timeout (`--mcp-turn-timeout`,
  default 120s) rather than a success. The write deadline is cleared for the duration of a call,
  because the turn deadline deliberately outlasts the server's 60s `WriteTimeout` and a response
  written after it would never reach the client.
- fix: `/mcp` rejects a non-loopback `Host` or `Origin`, a socket whose own local address is not
  loopback, and a body that is not declared `application/json`. A localhost HTTP server is
  otherwise reachable by any browser page through DNS rebinding, which with `spawn_session` on
  the other end is remote code execution as the daemon user. An oversized body answers `413`
  rather than being truncated into a parse error.
- fix: `send_message` refuses a session whose child already has a turn open. The session lock
  excludes other writers, but the timeout path releases it while the child is still generating —
  writing then would open a second turn on top of the first and make the two results
  indistinguishable.

## v0.2.0

- feat: run a named macro — a fixed, ordered list of user turns — against one child, writing each step
  only after the previous turn's `result` event arrives. Until now the wrapper could send one turn
  and watch, so a sequence like `/vault-cli:prepare-compact` → `/compact` →
  `/vault-cli:post-compact` had to be driven by hand: a timer cannot do it, because a compaction's
  duration is unpredictable and a delay short enough to be safe fires the next command mid-turn.
  The endpoint takes a macro name rather than a command list (`compact`, `terminal`) because it can
  drive `/exit`, so what it is able to run stays reviewable as a diff. Both macros contain a
  production-touching step and are refused with 409 without `confirm`; the call blocks and answers
  with every step, naming the ones a stopped run never reached.

## v0.1.0

- feat: let a spawn hand the child its own environment, resume an existing conversation, and load
  extra MCP config files. Each closes a silent divergence: without `env` the child inherits the
  wrapper's environment instead of the one its caller resolved; without `resume` a conversation
  cannot be continued through the wrapper at all; without `mcp_configs` a launcher's own
  `--mcp-config` file — a CLI flag, so no setting source can reach it — is absent and the child
  runs with fewer servers than its hand-started twin. A malformed `env` key or value, a
  flag-shaped MCP config entry and a non-UUID `resume` are refused with 400 rather than reaching
  the child's argv: `--mcp-config` is variadic, so an entry beginning with `-` would be parsed as
  a flag and silently change the child instead of being rejected.

- Stream a session's events over `GET /api/1.0/sessions/{id}/events` (server-sent events),
  replaying what the child has already emitted before the live frames. Until now the wrapper
  folded every event into `state` and discarded it, so a supervisor could read a child's state
  but never the turn that produced it — which is why replacing an in-process `query()` with a
  wrapper-backed spawn left the supervisor blind to the child it supervises.

- Expose `last_event_age_seconds` per session, so a caller can tell a genuinely idle child
  from one that is busy inside a tool call. State is never derived from it — that inversion is
  the defect the field makes visible.
- Conform to the house style the first cut skipped: inject the clock instead of calling
  `time.Now()`, return an interface from `NewPending`, use Ginkgo for the handler tests, and
  build the sentinel errors without a package-level `context.Background()`.
- Initial cut: a standalone Go service that owns a `claude` child process and exposes it over REST.
  Spawn, list, inspect and stop sessions; `state` is derived from the child's own event stream
  (`generating` while an assistant turn is open, `idle` when it closes) rather than from the age of
  its last event, which is what made a worker generating for forty minutes read as idle.
