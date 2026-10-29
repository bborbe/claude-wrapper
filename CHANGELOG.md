# Changelog

## Unreleased

- Let a spawn hand the child its own environment, resume an existing conversation, and load extra
  MCP config files. Each closes a silent divergence: without `env` the child inherits the wrapper's
  environment instead of the one its caller resolved; without `resume` a conversation cannot be
  continued through the wrapper at all; without `mcp_configs` a launcher's own `--mcp-config` file
  — a CLI flag, so no setting source can reach it — is absent and the child runs with fewer servers
  than its hand-started twin.

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
