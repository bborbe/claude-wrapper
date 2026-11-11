---
status: approved
approved: "2026-11-11T11:03:12Z"
branch: dark-factory/bug-headless-spawn-drops-shipping-permission-mode
---

## Summary

- The supervisor-facing `spawn_agent` tool accepts `shipping: true` for a local headless worker, then starts that worker in the default `auto` mode anyway.
- `shipping: true` exists so that a worker whose job ends in a commit, push or merge never stops on an operator-only mode switch. A dropped flag brings that stop back.
- Nothing tells the caller. The spawn reply carries no permission mode, so the caller believes the request was honoured. The bug-workflow names this class directly: a flag silently ignored.
- The fix: a headless local spawn with `shipping: true` starts its child in `acceptEdits`. Without it, nothing changes. The reply reports the mode and what decided it, and `bypassPermissions` stays unreachable on this path.

## Problem

`claude-wrapper` serves the roster MCP tool `spawn_agent` on `/supervisor/mcp`. Its argument struct decodes `shipping`. The A2A path refuses the field daemon-side, in `RefuseSpawnArguments` (`pkg/a2a/target.go`), before any controller contact. The tab path refuses it by name. The headless path builds the child's spawn options from cwd, model, label, role, policy and resume only, with no permission mode, so the supervisor falls back to its default `auto`. The caller gets a successful spawn reply that does not mention the mode. A manager that opens a shipping row headless gets a worker that reaches its first gated `git commit` / `git push` and parks on a gate the flag was supposed to remove. Nothing on the caller's side shows that the request was ignored.

## Goal

On the local headless path, `shipping: true` is either honoured or the call fails. A headless local spawn with `shipping: true` runs a child that reports `acceptEdits` for itself. A spawn without it runs exactly as it does today. Every headless spawn reply states the permission mode it requested and where that mode came from, so a future regression of this kind shows up in the reply.

## Non-goals

- Do NOT port the node server's `shippingSettings` allow-list (explicit `git add` / `commit` / `push` grants). This spec ships the mode only. In `acceptEdits` a Bash git command still consults the ambient `permissions.allow` and the permission-prompt tool. See Failure Modes.
- Do NOT change the tab path. Its `shipping` refusal stays byte-identical. Carrying the mode to a tab through `PERMISSION_MODE` and the launcher is separate work.
- Do NOT change the A2A path. `shipping` with `target: docker|k8s|k8s:<cluster>` stays refused by the A2A argument check.
- Do NOT add a per-call `permission_mode` argument, a config key, an env var or any other source for the mode. The per-call boolean is the only input. If a future consumer needs another mode, that is a separate spec.
- Do NOT change the other MCP surface (`spawn_session` in the wrapper's own tool server) or its `bypassPermissions` phase-1 default.
- Do NOT change `DefaultPermissionMode` (`auto`).
- Do NOT record `shipping` on the spawn-ledger row. No reader needs it.
- Do NOT fix the separate finding that the headless path also drops `env`. That is its own bug spec if wanted.
- Do NOT reconcile `shipping` with a per-spawn `policy` overlay. Under `acceptEdits` the CLI auto-accepts file edits without consulting the permission-prompt tool, so a policy rule on `Edit`/`Write` is not consulted for a shipping worker. That is intended: `shipping` means the worker's edits are not gated, and the node server's contract (`fleet-surface.md` item 9) says the same.

## Acceptance Criteria

- [ ] **AC1**: `make precommit` exits 0. Evidence: exit code.
- [ ] **AC2**: The headless spawn hands the supervisor the mode that matches `shipping`. Evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='headless spawn permission mode' -ginkgo.v` exits 0 and its `grep '^case='` output equals exactly these four lines (`-ginkgo.focus` on a string that matches nothing exits 0 with `Ran 0 of N Specs`, so exit 0 alone is not evidence):

  ```
  case=shipping requested=acceptEdits source=shipping
  case=no-shipping requested=auto source=default
  case=shipping-with-stray-permission-mode requested=acceptEdits source=shipping
  case=resume-shipping requested=acceptEdits source=shipping
  ```

  `requested` is read from the `SpawnOptions.PermissionMode` that a recording fake supervisor captured, not from the reply and not from a constant in the test. `source` is read from the reply's `permission_mode_source`. The fourth case passes raw arguments `{"resume":"<uuid>","shipping":true,"interactive":false,"cwd":"/tmp"}` and additionally asserts that `buildArgs` on the captured options holds both `--resume <uuid>` and `--permission-mode acceptEdits` (pattern: `pkg/roster_resume_test.go`). In the third case the raw arguments also carry `"permission_mode":"bypassPermissions"`, which must have no effect. The `no-shipping` case asserts the captured value is `auto`, or empty with the supervisor's default applied, and the printed `requested=auto` reflects whichever of the two the implementation does. **Mutation half:** if the `shipping` → mode mapping in the headless spawn is replaced by the pre-fix literal (no `PermissionMode` set), the same command exits non-zero with `[FAILED]` in the report. The verifier applies the mutation, runs the test, and restores the file with `git checkout -- pkg/roster_mcp.go`, on a clean, committed tree only. A compile failure does not count as a pass for this half.
- [ ] **AC3**: (folded) The child's command line already carries a given mode: the existing spec `"still passes the model and permission mode"` (`pkg/supervisor_test.go`) asserts it, and must pass unedited; AC2's fourth case covers the `acceptEdits` + `--resume` combination.
- [ ] **AC4**: The spawn reply names the mode and its source. Evidence: in the AC2 test, the decoded reply JSON has `"permission_mode":"acceptEdits","permission_mode_source":"shipping"` for the `shipping` case and `"permission_mode":"auto","permission_mode_source":"default"` for the `no-shipping` case. The existing reply keys (`agent_id`, `session_id`, `state`, `cwd`, `mode`, `mode_source`) are still present.
- [ ] **AC5**: The tab and A2A refusals are unchanged. Evidence: `git diff origin/master -- pkg/roster_mcp.go | grep -E '^[-+].*shipping. needs the server to set the worker'` returns 0 lines. `git diff origin/master -- pkg/a2a/target.go` is empty. The existing `pkg/a2a/target_test.go` entry `"shipping is not supported"` passes under `make test`.
- [ ] **AC6**: `bypassPermissions` cannot be reached through `spawn_agent`. Evidence: `grep -n 'json:"permission_mode' pkg/roster_mcp.go` returns 0 lines, so no argument field decodes a mode, and AC2's third case shows a stray `permission_mode` argument is ignored in favour of the `shipping`-derived value. `grep -n 'BypassPermissions\|bypassPermissions' pkg/roster_mcp.go` returns 0 lines.
- [ ] **AC7**: The tool schema documents the field. Evidence: `grep -n '"shipping":' pkg/roster_mcp.go` returns ≥1 line inside the `spawn_agent` property map. Its description states that headless starts the worker in `acceptEdits` and that a tab or A2A target refuses it. The description does not name `bypassPermissions` (AC6's grep would catch it).
- [ ] **AC8**: CHANGELOG entry. Evidence: `awk '/^## /{sec=$0} /headless.*shipping|shipping.*headless/{print "sits under: " sec}' CHANGELOG.md` prints `sits under: ## Unreleased` at least once. The bullet starts with `- fix:` and names `acceptEdits`. (The repo has no `CLAUDE.md`; this AC follows the repo's own convention: every release section carries `fix:` / `feat:` bullets, and the commit flow validates the CHANGELOG.)
- [ ] **Post-Deploy (Rung-2):** (AC9) The reproduction no longer reproduces, and the control run is unchanged. Evidence: run against the installed and restarted daemon on `127.0.0.1:18090`. Key every lookup on `id`; `session_id` and `label` are `null` on these rows.
  1. `spawn_agent(interactive:false, shipping:true, cwd:"/tmp", label:"verify-shipping-mode", prompt:"reply OK and stop")`. The reply contains `"permission_mode":"acceptEdits"` and `"permission_mode_source":"shipping"`.
  2. Poll every 2 s for up to 60 s until the row's `state` is no longer `starting`, then run `curl -s http://127.0.0.1:18090/api/1.0/sessions | jq -r '.[] | select(.id=="<agent_id from step 1>") | .permission_mode'`. It prints `acceptEdits`. This is the child's own `system/init` statement, not a value the wrapper remembered.
  3. Control: the same call without `shipping`. The reply carries `"permission_mode":"auto","permission_mode_source":"default"`, and the same `jq` probe on its id prints `auto`.
  4. Stray-argument check: step 1 with `"permission_mode":"bypassPermissions"` added. The probe prints `acceptEdits`, never `bypassPermissions`.
  5. `stop_agent` on all three ids returns `"state":"stopped"`.

  - `deploy_check:` `go version -m ~/.local/bin/claude-wrapper | sed -n 's/.*vcs\.revision=\(.\{7\}\).*/\1/p'`
  - `deploy_target:` `$(git rev-parse --short=7 HEAD)`, run from the synced `master` checkout after the merge (`make install` runs there too). The repo merges with merge commits, so the feature-branch tip is not the deployed revision.

No new scenario. The runtime check is the post-deploy replay above. The unit layer reaches the decision, and the child's own init report is the only runtime fact, which AC9 already reads.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit`: tidy, format, lint and test all exit 0
- `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='headless spawn permission mode' -ginkgo.v`: prints the four `case=` lines from AC2
- `grep -n 'json:"permission_mode' pkg/roster_mcp.go`: 0 lines
- `grep -n '"shipping":' pkg/roster_mcp.go`: ≥1 line

### Operator-executable (runs on the host after PR merge)

- `make install`: builds, codesigns, installs to `~/.local/bin/claude-wrapper`
- `launchctl kickstart -k gui/$UID/de.bborbe.claude-wrapper`: the daemon restarts on the new binary
- The `Post-Deploy (Rung-2)` AC9 replay above, from a session that has the roster MCP `/supervisor/mcp` wired

## Desired Behavior

1. A local headless `spawn_agent` call with `shipping: true` starts its child with `--permission-mode acceptEdits`. The child's own init then reports `acceptEdits` on `/api/1.0/sessions`.
2. A local headless call without `shipping`, or with `shipping: false`, starts its child exactly as today: `--permission-mode auto`, via the supervisor default.
3. Every successful headless spawn reply carries `permission_mode` (the mode requested for the child) and `permission_mode_source`, whose value is `shipping` when the field decided the mode and `default` otherwise. The key names are fixed so AC4 and AC9 can assert them.
4. The mode on this path comes from a closed two-value mapping: `shipping: true` → `acceptEdits`, otherwise → `auto`. No argument, env var, config key or ledger value can put any other mode, `bypassPermissions` included, on a child **at spawn** through `spawn_agent`. (A running child's mode can still be changed later through `/mcp`'s `set_permission_mode`; that surface is out of scope here.)
5. `resume` combined with `shipping: true` resumes the conversation in `acceptEdits`. `resume` without `shipping` resumes in `auto`. A resumed worker does not inherit its earlier spawn's mode, because the per-call field is the only source.
6. The `spawn_agent` tool schema lists `shipping` with its per-path behaviour: honoured headless, refused for a tab, refused for an A2A target.

## Constraints

- **The tab-path refusal is frozen.** The `shipping` refusal in the tab spawn and its message text stay exactly as on `origin/master` `abfd501`, because this fix does not reach a tab's permission mode. Likewise the A2A-target refusal in `pkg/a2a/target.go`.
- **One source for the mode:** the per-call `shipping` boolean. No new config field, env var, flag or argument. `reservedEnvNames` keeps `PERMISSION_MODE` reserved.
- **Never `bypassPermissions`.** `acceptEdits` is the shipping mode because, per the contract below, it still consults `permissions.allow` and every `PreToolUse` hook, so the operator's prod-deploy deny hook still fires.
- The supervisor's `DefaultPermissionMode` (`auto`) and `buildArgs`'s empty-mode fallback are unchanged. The existing `buildArgs` specs pass without edits.
- The pre-existing reply keys (`agent_id`, `session_id`, `state`, `cwd`, `mode`, `mode_source`) keep their names and values. The two new keys are additive.
- The spawn-ledger row is unchanged.
- Contract cited: `claude-supervisor` `docs/fleet-surface.md` § Spawn a worker, item 9. Local read-only copy: `~/.claude/plugins/cache/claude-supervisor/supervisor/0.276.1/docs/fleet-surface.md`. It is a reference, not to be modified.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| `shipping: true` on a tab spawn | Refused with the existing message, as today | Caller passes `interactive: false` or drops `shipping` |
| `shipping: true` with an A2A target | Refused by the A2A argument check, as today | Caller uses a local target |
| Shipping worker in `acceptEdits` runs `git push` and the ambient `permissions.allow` lacks it | The child routes the call to the permission-prompt tool and it parks with the spawner. The gate moves from the mode switch to a Bash approval, which is the known residual of the mode-only port (Non-goals) | Spawner answers with `answer_permission`. The allow-list port is a separate spec |
| Child ignores `--permission-mode acceptEdits` (CLI drift) and reports another mode in init | `/api/1.0/sessions` shows the child's own value, so the drift is visible. The reply still says `acceptEdits` because it reports what was requested | Detection: AC9 step 2 mismatch. File against the CLI version, not this path |
| Child never emits init | `permission_mode` stays empty and the existing startup deadline kills it after `startup.maxStartingSeconds` | Unchanged startup-deadline path |
| Caller passes a stray `permission_mode` argument | Ignored, as today. The mode is still derived from `shipping` alone (AC2 case 3, AC9 step 4) | None needed |
| Resume of a worker originally spawned with `shipping` but resumed without it | Resumes in `auto`, and the reply says `permission_mode_source: default`, so the drop is visible | Caller re-passes `shipping: true` on the resume |

## Security / Abuse Cases

- The caller controls `shipping`, a boolean. The worst it can do is select `acceptEdits`, which still runs `permissions.allow` and every `PreToolUse` hook. It cannot select `bypassPermissions` (DB 4, AC6).
- No new trust boundary is crossed. The mode is computed in the daemon and passed as a fixed CLI token, never interpolated from caller text.

## Reproduction

Observed 2026-11-11 against the daemon on `127.0.0.1:18090`, built from `origin/master` `abfd501` (`release v0.17.1`).

**Static:** `spawnLocalAgent` in `pkg/roster_mcp.go` on `abfd501` builds the child's options with no permission mode, though `body.Shipping` was decoded:

```go
	session, err := s.Supervisor.Spawn(ctx, SpawnOptions{
		Cwd:    cwd,
		Model:  s.headlessModel(ctx, body, cwd),
		Label:  body.Label,
		Role:   body.Role,
		Policy: body.Policy,
		Resume: body.Resume,
	})
```

`buildArgs` in `pkg/supervisor.go` then falls back to `DefaultPermissionMode` (`auto`) for an empty `PermissionMode`.

**Live fleet, 2026-11-11:** `GET http://127.0.0.1:18090/api/1.0/sessions` returned 120 children with `permission_mode` distribution `{auto: 119, ?: 1}`. Zero were `acceptEdits`.

**Replay, 2026-11-11** (run by the Development Pipeline Throughput Manager session):

1. `spawn_agent(interactive:false, shipping:true, cwd:"/tmp", label:"repro-shipping-mode", prompt:"reply OK and stop")` returned:

   ```json
   {"agent_id":"071460c6-fbf8-46eb-af2e-23e73a38e885","cwd":"/tmp","mode":"headless","mode_source":"argument","session_id":"071460c6-fbf8-46eb-af2e-23e73a38e885","state":"starting"}
   ```

2. `GET http://127.0.0.1:18090/api/1.0/sessions`, matched on `id`:

   ```json
   {"id":"071460c6-fbf8-46eb-af2e-23e73a38e885","session_id":null,"label":null,"permission_mode":"auto","state":"idle","mode":"headless"}
   ```

3. `stop_agent` returned `{"agent_id":"071460c6-fbf8-46eb-af2e-23e73a38e885","state":"stopped"}`.

**Expected:** `acceptEdits`. **Actual:** `auto`. The child was already `idle` about 4 s after the spawn. The reply in step 1 has no `permission_mode`, so the caller could not see the drop. `session_id` and `label` are `null` on the sessions row, so every assertion keys on `id`.

## Expected vs Actual

| | Behavior |
|---|---|
| **Expected** | Per `claude-supervisor` `docs/fleet-surface.md` § Spawn a worker, item 9: *"`shipping: true` is opt-in and decides the worker's PERMISSION MODE … a shipping worker runs in `acceptEdits` from its first turn instead of parking mid-task on `you run: Shift+Tab to accept-edits mode`."* The reply states the mode it applied. |
| **Actual** | The child starts in `auto`, and the reply is silent on mode. |

## Why this is a bug

- **Documented behaviour does not match reality.** Item 9 of the fleet-surface contract says a headless shipping worker runs in `acceptEdits`. This daemon is the headless spawner the supervisor now routes to, and it does not do that.
- **The flag is accepted and then ignored,** which the bug-workflow lists by name. The same contract item calls *"the exact accepted-and-quietly-ignored class this repo refuses"* out as the failure the design exists to prevent. This repo's own tab path already follows that rule ("Arguments the tab path cannot honour are REFUSED, never dropped"), so the headless path is the one place the rule is broken.
- **The cost lands on the expensive side.** A shipping row is opened headless precisely because it runs unattended to a merge. Its first gated step parks with the spawner, and a manager that cannot see the dropped flag in the reply diagnoses the wrong cause.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Headless spawn maps `shipping` → `PermissionMode`, the reply gains `permission_mode` / `permission_mode_source`, the schema lists `shipping`, and the CHANGELOG bullet | 1, 2, 3, 4, 5, 6 | 1, 4, 6, 7, 8 | — |
| 2 | Regression locks: table-driven headless-mode spec with mutation check, `buildArgs` `acceptEdits` case, untouched-refusal check | 1, 2, 4 | 2, 3, 5 | prompt 1 |

Rationale: the change is confined to the roster spawn path. Prompt 2 pins the mapping in both directions, so an inverted or dropped mapping fails a test instead of reaching a shipping worker. AC9 is the operator's post-deploy replay and is not a prompt deliverable.
