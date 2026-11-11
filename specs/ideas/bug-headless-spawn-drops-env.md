---
status: idea
kind: bug
---

## Summary

The roster MCP `spawn_agent` decodes `env` for a local headless worker but never passes it to the child, so a caller's per-worker environment is silently dropped. Same class as `bug-headless-spawn-drops-shipping-permission-mode`: a flag accepted and quietly ignored.

## Reproduction (static, unconfirmed live)

On `origin/master` `abfd501`, `spawnLocalAgent` (`pkg/roster_mcp.go`) builds:

```go
SpawnOptions{
	Cwd:    cwd,
	Model:  s.headlessModel(ctx, body, cwd),
	Label:  body.Label,
	Role:   body.Role,
	Policy: body.Policy,
	Resume: body.Resume,
}
```

No `Env`. The tab path validates `env` (`ValidateTabEnv`) and the A2A path refuses it, so only the headless path drops it without a word.

Live reproduction still needed: spawn headless with `env: {"PROBE_X":"1"}` and have the worker run `echo $PROBE_X`.

## Expected vs Actual

- Expected (per `claude-supervisor` `docs/fleet-surface.md` § Spawn a worker, `env` honoured on both local paths): the child sees `PROBE_X=1`.
- Actual (static reading): the child's environment has no `PROBE_X`.

## Open Questions

- Should the headless path honour `env`, or refuse it the way A2A does? Either is fine; silently dropping it is not.

Found 2026-11-11 by the spec-creator and spec-auditor passes on the shipping bug.
