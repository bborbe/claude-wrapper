# Changelog

## Unreleased

- Initial cut: a standalone Go service that owns a `claude` child process and exposes it over REST.
  Spawn, list, inspect and stop sessions; `state` is derived from the child's own event stream
  (`generating` while an assistant turn is open, `idle` when it closes) rather than from the age of
  its last event, which is what made a worker generating for forty minutes read as idle.
