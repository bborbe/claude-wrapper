# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
