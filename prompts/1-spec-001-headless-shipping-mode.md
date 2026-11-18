---
status: draft
spec: [001-bug-headless-spawn-drops-shipping-permission-mode]
created: "2026-11-18T18:45:00Z"
branch: dark-factory/bug-headless-spawn-drops-shipping-permission-mode
---

# Honour `shipping` on the headless spawn path

<summary>
- A headless `spawn_agent` call carrying `shipping: true` starts its child in `acceptEdits` instead of the `auto` default.
- A headless call without `shipping`, or with `shipping: false`, starts exactly as it does today.
- Every successful headless spawn reply now names the permission mode requested for the child and what decided it.
- The reply names `shipping` as the source when the flag decided the mode, and `default` otherwise, so a future dropped flag is visible to the caller.
- The `spawn_agent` tool schema documents `shipping` and states its per-path behaviour: honoured headless, refused for a tab, refused for an A2A target.
- `bypassPermissions` stays unreachable: no new argument decodes a mode, and the flag is the only input.
- The tab and A2A refusals are untouched, byte for byte.
- A CHANGELOG entry records the fix under `## Unreleased`.
- Existing spawn, resume, model and permission-mode behaviour is unchanged.
</summary>

<objective>
Make the local headless `spawn_agent` path honour its already-decoded `shipping` flag. `shipping: true` must hand the supervisor `SpawnOptions.PermissionMode = "acceptEdits"`; otherwise the mode stays `auto` through the supervisor default. Report the requested mode and its source in the spawn reply, and document the flag in the tool schema. Nothing else on the spawn surface changes.
</objective>

<context>
Read these before changing anything:

- `pkg/roster_mcp.go` — `spawnAgent`, `spawnLocalTarget`, `spawnLocalAgent`, `spawnAgentArgs`, `spawnToolDefinitions`, and the existing `set_agent_permission_mode` constants (`ShippingPermissionMode` may or may not already exist in the tree; see the verify-or-create note below).
- `pkg/supervisor.go` — `SpawnOptions` (note `PermissionMode string \`json:"permission_mode"\``), `DefaultPermissionMode` (the constant `"auto"`), and `buildArgs` (an empty `PermissionMode` falls back to `DefaultPermissionMode`).
- `pkg/mcptools.go` — `PermissionMode`, `PermissionModeAcceptEdits`, `boolProp`, `toolDef`, and `stringProp`. `boolProp(description string) map[string]any` is the helper the schema uses for boolean properties.
- `pkg/tab.go` — `reservedEnvNames`; `PERMISSION_MODE` stays reserved and must not gain a second source.
- `pkg/a2a/target.go` — `RefuseSpawnArguments`; the `shipping` refusal there is frozen.
- `pkg/roster_resume_test.go` — the pattern for driving `spawnAgent` with raw JSON arguments and reading `toolSupervisor.spawned`.
- `pkg/supervisor_test.go` — the existing spec `"still passes the model and permission mode"` must pass unedited.
- `CHANGELOG.md` — the preamble ends at the `## v0.38.5` heading; there is currently no `## Unreleased` section.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`

Verify-or-create: if a declaration or reply key described below already exists in the tree and matches this prompt, keep it and do not duplicate it. The tree may already carry a partial or complete version of this fix; the requirements describe the end state, not a mandate to rewrite working code.
</context>

<requirements>
1. **Add the shipping-mode declarations to `pkg/roster_mcp.go`.** Place them immediately BEFORE the `// spawnLocalAgent creates a wrapper child ...` doc comment, so that doc comment stays attached to its function. Use exactly this shape:

   ```go
   // ShippingPermissionMode is the mode a headless worker runs in when the caller marks
   // it `shipping`.
   //
   // `acceptEdits` and not the bypass mode: it auto-accepts edits and stops the
   // auto-mode classifier from gating a routine commit or push, while `permissions.allow`
   // and every PreToolUse hook still apply.
   const ShippingPermissionMode = "acceptEdits"

   // The two values `permission_mode_source` reports, so a caller can tell a mode the flag
   // decided from the daemon's default. A dropped flag is then visible in the reply.
   const (
       PermissionModeSourceShipping = "shipping"
       PermissionModeSourceDefault  = "default"
   )

   // headlessPermissionMode maps `shipping` to the mode a headless child starts in.
   //
   // This is the ONLY source of the mode on the headless path: no argument, env var or
   // config key can name another, so the bypass mode is unreachable through `spawn_agent`.
   func headlessPermissionMode(shipping bool) (mode, source string) {
       if shipping {
           return ShippingPermissionMode, PermissionModeSourceShipping
       }
       return DefaultPermissionMode, PermissionModeSourceDefault
   }
   ```

   `DefaultPermissionMode` is already declared in `pkg/supervisor.go` in the same package; do not redeclare it.

2. **Use the mapping in `spawnLocalAgent`.** Immediately before the `s.Supervisor.Spawn(...)` call, compute:

   ```go
   mode, modeSource := headlessPermissionMode(body.Shipping)
   ```

   Then add `PermissionMode: mode,` to the `SpawnOptions` literal. The resulting call must be:

   ```go
   mode, modeSource := headlessPermissionMode(body.Shipping)
   session, err := s.Supervisor.Spawn(ctx, SpawnOptions{
       Cwd:            cwd,
       Model:          s.headlessModel(ctx, body, cwd),
       PermissionMode: mode,
       Label:          body.Label,
       Role:           body.Role,
       Policy:         body.Policy,
       Resume:         body.Resume,
       MCPConfigs:     s.headlessMCPConfigs(ctx, body, cwd),
   })
   ```

   Do not change any other field, the error wrapping (`errors.Wrapf(ctx, err, "spawn agent failed")`), or the opening-turn delivery and stop-on-failure block below it.

3. **Add the reply keys in `spawnLocalAgent`.** Extend the `marshalJSON` map with the two additive keys, keeping the existing keys (`agent_id`, `session_id`, `state`, `cwd`, `mode`, `mode_source`) unchanged:

   ```go
   return marshalJSON(ctx, map[string]string{
       "agent_id":               session.ID,
       "session_id":             session.ID,
       "state":                  string(session.State),
       "cwd":                    session.Cwd,
       "mode":                   SpawnModeHeadless,
       "mode_source":            resolution.Source,
       "permission_mode":        mode,
       "permission_mode_source": modeSource,
   })
   ```

   Add a short comment above the two new keys explaining that the flag is otherwise unobservable: a caller that passed `shipping: true` could not tell it had been dropped, and the pre-fix measurement was 119 of 120 live children in `auto`, none in `acceptEdits`.

4. **Document `shipping` in the `spawn_agent` tool schema.** In `spawnToolDefinitions`, add `"shipping": shippingProp(),` to the `spawn_agent` property map (alphabetically near the other headless-only properties is fine). Define the helper:

   ```go
   // shippingProp is the `shipping` argument's schema.
   func shippingProp() map[string]any {
       return boolProp("headless only: run the worker under `acceptEdits` so its edits and " +
           "commits do not stop on the auto-mode classifier; `permissions.allow` and every " +
           "hook still apply. A tab or A2A target refuses it")
   }
   ```

   The description must state that headless starts the worker in `acceptEdits` and that a tab or A2A target refuses the field. It must NOT name the bypass mode; the word must not appear in the schema description.

5. **Do not add a `permission_mode` argument.** `spawnAgentArgs` must not gain any field whose JSON tag is `permission_mode`. The per-call `shipping` boolean is the only input that selects the mode. A stray `permission_mode` value in the raw arguments is ignored by `encoding/json` and must remain so.

6. **Leave the tab and A2A refusals untouched.** In `spawnTabAgent`, the `if body.Shipping { ... }` refusal and its message text (`"`shipping` needs the server to set the worker's permission mode, which this slice does not do for a tab — omit it"`) stay byte-identical. In `pkg/a2a/target.go`, `RefuseSpawnArguments` and its `shipping is not supported with target ...` message stay byte-identical. Do not edit either file for this prompt.

7. **Add the CHANGELOG entry.** Insert a new `## Unreleased` section immediately after the preamble (after the `and this project adheres to [Semantic Versioning]...` line) and before the existing `## v0.38.5` heading. Add exactly one bullet that starts with `- fix:` and names `acceptEdits`:

   ```markdown
   - fix: **a headless `spawn_agent` with `shipping: true` now starts the worker in `acceptEdits` instead of `auto`.** The roster tool decoded the flag and dropped it, and the reply never named the mode, so a caller could not tell. The reply gains `permission_mode` and `permission_mode_source` (`shipping` / `default`), and the tool schema documents `shipping`. The mode has exactly one source — the flag — so the bypass mode stays unreachable through this tool; the tab and A2A refusals are unchanged. Spec `specs/in-progress/001-bug-headless-spawn-drops-shipping-permission-mode.md`.
   ```

   Do not move, delete, or insert content inside the frozen preamble above `## Unreleased`.

8. **Failure paths stay as they are.** Do not add retries, fallbacks, or new error paths:
   - A `Spawn` error still returns `errors.Wrapf(ctx, err, "spawn agent failed")`.
   - A failed opening-turn `Send` still stops the child with `s.Supervisor.Stop(context.Background(), session.ID)` and returns `errors.Wrapf(ctx, err, "deliver opening turn failed")`.
   - `shipping: true` on a tab still returns the existing refusal before any spawn.
   - `shipping: true` with an A2A target still returns the existing A2A argument error before any controller contact.
   - A child that ignores `--permission-mode acceptEdits` is not this path's problem: the reply reports what was requested (`acceptEdits`), and the child's own init report is the runtime truth.
   - A child that never emits init is handled by the unchanged startup deadline.

9. Run `make precommit` and the verification below before finishing.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. In particular `pkg/supervisor_test.go`'s `"still passes the model and permission mode"` must pass without edits, and `pkg/a2a/target_test.go`'s `"shipping is not supported"` entry must pass unchanged.
- **One source for the mode:** the per-call `shipping` boolean. Do NOT add a config field, env var, flag, argument, or ledger value that can name the mode.
- **Never select the bypass mode.** `ShippingPermissionMode` is `acceptEdits` and nothing else. Do not add a branch that can produce `bypassPermissions` on a spawn.
- Do NOT change `DefaultPermissionMode` (`auto`), `buildArgs`'s empty-mode fallback, or the supervisor's `SpawnOptions` shape.
- Do NOT change the tab path. Its `shipping` refusal stays byte-identical. Carrying a mode to a tab through `PERMISSION_MODE` and the launcher is separate work.
- Do NOT change the A2A path. `shipping` with `target: docker|k8s|k8s:<cluster>` stays refused by the A2A argument check.
- Do NOT change the other MCP surface (`spawn_session` in the wrapper's own tool server) or its `bypassPermissions` phase-1 default.
- Do NOT record `shipping` on the spawn-ledger row. No reader needs it.
- Do NOT fix the separate finding that the headless path also drops `env`.
- Do NOT reconcile `shipping` with a per-spawn `policy` overlay. Under `acceptEdits` the CLI auto-accepts file edits without consulting the permission-prompt tool; that is intended.
- Repo conventions: `github.com/bborbe/errors` wrapping (no `fmt.Errorf`), GoDoc on every exported item, and no deliberation prose in code comments.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then confirm the argument surface and schema with these checks:

```
grep -n 'json:"permission_mode' pkg/roster_mcp.go     # must print 0 lines
grep -n '"shipping":' pkg/roster_mcp.go               # must print at least 1 line inside the spawn_agent property map
awk '/^## /{sec=$0} /headless.*shipping|shipping.*headless/{print "sits under: " sec}' CHANGELOG.md
```

The `awk` command must print `sits under: ## Unreleased` at least once. The CHANGELOG bullet must start with `- fix:` and name `acceptEdits`.

Do not add the regression spec here; it is the next prompt. `make precommit` still runs every existing spec.
</verification>

<!--
OPEN QUESTION FOR THE HUMAN REVIEWER (not an instruction to the agent):

1. AC6's second evidence command in the spec is `grep -n 'BypassPermissions\|bypassPermissions' pkg/roster_mcp.go` must return 0 lines. The current file already contains the word in explanatory comments that predate this change (the `set_agent_permission_mode` arm and `DefaultPermissionMode` docs), so the literal command cannot return 0 without deleting unrelated documentation. This prompt therefore treats AC6 as its intent — no `permission_mode` argument field decodes, and no code path selects the bypass mode — and uses the `json:"permission_mode"` grep as the hard check. If the literal grep is required, the reviewer must decide whether the unrelated comments may be reworded.

2. The spec's AC2 fourth case passes raw arguments without a `prompt`. Since 2026-11-16 `refusePromptlessResume` (`pkg/resume_prompt.go`) refuses a promptless headless resume, so that exact raw-argument line can no longer reach `Spawn`. The regression prompt adds `"prompt":"continue"` to the resume case so it exercises the spawn path; the assertion still pins `--resume` plus `--permission-mode acceptEdits`.
-->
