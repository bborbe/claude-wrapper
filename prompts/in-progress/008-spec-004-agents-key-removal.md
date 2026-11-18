---
status: approved
spec: [004-agents-key-removal]
created: "2026-11-18T17:34:32Z"
queued: "2026-11-18T17:37:54Z"
---

# Remove the stale `Agents` role-map key and refuse every agent tab

<summary>
- The wrapper no longer maps the agent role to any terminal window, so a stale window name can never steer a tab into whichever window has focus.
- Every path that opens a terminal tab refuses an agent outright, before anything is opened, coloured, reserved or recorded.
- The refusal names matrix Rule 2 and tells the caller to spawn headless with `interactive=false`.
- An agent is refused whether it asked for a tab explicitly, left `role` empty, or the tab came from the operator's configured spawn mode.
- The flagged reopen tick that meets an agent row stored as interactive now has that reopen refused and counted as refused instead of opening a tab.
- Headless agent spawns, human tabs and manager tabs behave exactly as before.
- A role map that still carries the old key is simply ignored.
- The changelog gets an Unreleased entry describing the refusal.
</summary>

<objective>
Delete the `agent → "Agents"` entry from the wrapper's role table and make the tab spawner, and the `spawn_agent` tab path in front of it, refuse any request whose role resolves to `agent` (explicit or defaulted from an empty `role`) before the first side effect, with an error naming matrix Rule 2 and `interactive=false`. Headless spawns and human/manager tabs stay unchanged.
</objective>

<context>
Read the repo `README.md` for project conventions (there is no `CLAUDE.md` in this repo).

Read these files fully before changing anything:

- `pkg/role_map.go` — `RoleManager` / `RoleAgent` / `RoleHuman`, `DefaultRole`, `rolePurposes`, `RoleResolution`, `ResolveRole(ctx context.Context, reader RoleMapReader, role string) (*RoleResolution, error)`, `ChipForRole`.
- `pkg/errors.go` — the sentinel block (`ErrUnknownRole = stderrors.New("unknown role")` and neighbours) and its comment style.
- `pkg/tab_spawn.go` — `(*tabSpawner).Spawn(ctx context.Context, request TabSpawnRequest) (*TabSpawnResult, error)`; note its doc comment: "every refusal is decided BEFORE the first side effect".
- `pkg/roster_mcp.go` — `spawnAgentArgs`, `spawnAgent`, `spawnLocalTarget` (contains the current argument-keyed `role: agent` guard and its comment about the "documented debug hatch"), `spawnTabAgent` (cheap argument refusals, then `enforceCaps`, then `s.Spawner.Spawn`), `enforceCaps`, `callTool`, `spawnToolDefinitions` (the `ToolSpawnAgent` description and the `"role"` property text `"manager, agent or human; defaults to agent"`).
- `pkg/flagged_reopen.go` — `flaggedReopener.Reopen`, `isHeadlessRow`; the tab branch calls `r.Spawner.Spawn(ctx, TabSpawnRequest{... Role: row.Role ...})` and wraps an error as `"spawn tab"`.
- `pkg/flagged_tick.go` — in `tickFromRows` a `Reopener.Reopen` error is appended to `result.Refused` as `row.Key()+": spawn failed: "+err.Error()`.
- `pkg/mcptools.go` — `toolResult(text string, isError bool) map[string]any` returns `{"content": [{"type":"text","text": text}], "isError": isError}`.
- Test fixtures to reuse (all package `pkg`, Ginkgo v2 + Gomega):
  - `pkg/tab_spawn_test.go` — `fakeTabClient` (`spawned []TabSpawnOptions`, `texts []string`, `onSpawn`), `fakeVaults`, `fakeLedger` (`posts []LedgerRow`), `fakeSpawner` (`requests []TabSpawnRequest`), `steppingClock()`, the `tabSpawner.Spawn` and `RosterToolServer.spawnLocalTarget` Describes.
  - `pkg/spawn_cwd_test.go` — `fakeRoleMap{entries map[string]RoleMapEntry}`, the `ResolveRole` Describe.
  - `pkg/spawn_mode_test.go` — `boolPtr`, `configMode`, `fixedModeSources{sources: SpawnModeSources{...}}` (fields `Env`, `Config`, `ConfigPath`), `headlessSupervisor{spawned []SpawnOptions}`, the `RosterToolServer.spawnLocalTarget mode resolution` Describe and its `server(sources)` helper.
  - `pkg/spawn_limits_test.go` — `fakeSpawnCaps` (`workerCalls`, `managerCalls`, `startCalls`), the `RosterToolServer spawn caps` and `spawn caps after review` Describes.
  - `pkg/roster_role_test.go`, `pkg/roster_resume_test.go`, `pkg/roster_shipping_mode_test.go`.
- `CHANGELOG.md` — top of file; there is currently NO `## Unreleased` section (the first section is `## v0.38.4`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Error library is `github.com/bborbe/errors` v1.7.0: `errors.Wrap(ctx context.Context, err error, message string) error`, `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`.
</context>

<requirements>
0. **Baseline evidence for AC2 (before any edit).** Run
   `grep -rnw --exclude='*_test.go' 'Agents' pkg cmd main.go`
   and keep its output for the completion report. Expected on the unchanged tree: exactly 2 lines, `pkg/role_map.go:30` (the comment) and `pkg/role_map.go:33` (`RoleAgent:   "Agents",`).

1. **Sentinel error** — in `pkg/errors.go`, add next to `ErrUnknownRole`:

   ```go
   // ErrAgentTab is returned when a tab spawn's role resolves to agent.
   //
   // Role-mode matrix Rule 2: an agent is headless always. The refusal is a
   // refusal, never a silent conversion to headless — a caller that still asks
   // for agent tabs must see the mistake rather than have it hidden.
   ErrAgentTab = stderrors.New("agent tab refused")
   ```

2. **Role table** — in `pkg/role_map.go`:
   - Delete the `RoleAgent:   "Agents",` line from `rolePurposes`. The map keeps only `RoleManager: "Managers"` and `RoleHuman: "Direct"`.
   - Rewrite the `rolePurposes` doc comment so it no longer contains the word `Agents` (AC2 greps with `-w 'Agents'` over non-test files, comments included). Say instead that only manager and human roles carry a window: an agent never opens a tab (matrix Rule 2), so it has no role-map key, and a stale key of that name in the role map file is never read.
   - Keep `RoleAgent` and `DefaultRole = RoleAgent` unchanged — `agent` stays a valid role for headless spawns, ledger rows and the caps.
   - Add a refusal text constant and a pure helper (no I/O):

     ```go
     // agentTabRefusal is the text every agent tab refusal carries. It names
     // the rule and the call that works, so the caller can fix the call
     // without reading the source.
     const agentTabRefusal = "an agent never opens a tab — role-mode matrix Rule 2 " +
     	"(agent → headless always). Spawn it headless with interactive=false, or " +
     	"omit interactive under a headless spawn.mode; use role: human or " +
     	"role: manager only if this really is the operator's own session or a manager"

     // RefuseAgentTab returns ErrAgentTab when role resolves to agent, explicitly
     // or by defaulting an empty role to DefaultRole, and nil otherwise.
     func RefuseAgentTab(ctx context.Context, role string) error {
     	trimmed := strings.TrimSpace(role)
     	if trimmed == "" {
     		trimmed = DefaultRole
     	}
     	if trimmed != RoleAgent {
     		return nil
     	}
     	return errors.Wrap(ctx, ErrAgentTab, agentTabRefusal)
     }
     ```

     The resulting `err.Error()` MUST contain both the literal `Rule 2` and the literal `interactive=false` (AC3 asserts both substrings). Do not put the word `Agents` in the text.
   - In `ResolveRole`, after the empty → `DefaultRole` defaulting and BEFORE the `rolePurposes` lookup, call `RefuseAgentTab(ctx, trimmed)` and return `nil, err` when it is non-nil. This keeps an agent from ever falling through to the `ErrUnknownRole` "is not a role" text (which would be false — agent is a role). Update `ResolveRole`'s doc comment to say an agent role is refused (it never resolves to a window or chip) and an unknown role is refused with `ErrUnknownRole`. Leave the `ErrUnknownRole` message's "Valid values: manager, agent, human" list as it is.
   - `ChipForRole` and `vaultFamily` are unchanged.

3. **Tab spawner** — in `pkg/tab_spawn.go`, make the agent refusal the FIRST statement of `(*tabSpawner).Spawn`, before the empty-prompt check:

   ```go
   if err := RefuseAgentTab(ctx, request.Role); err != nil {
   	return nil, err
   }
   ```

   No other ordering in `Spawn` changes. Extend the method's doc comment with one sentence: an agent role is refused first, so an agent never reaches the vault list, the role map, a tab name reservation, a pane, a colour or a ledger row. The `ResolveRole` call stays where it is (its own agent check is defence in depth for any future caller).

4. **`spawn_agent` tab path** — in `pkg/roster_mcp.go`:
   - In `spawnLocalTarget`, DELETE the whole existing guard block that begins with the comment `// A `role: agent` row is headless always — the 2026-11-12 operator ruling ...` and ends with the `if strings.TrimSpace(body.Role) == RoleAgent && body.Interactive != nil && *body.Interactive { return "", errors.Errorf(...) }` block. Its "keyed on the caller's argument so an absent role still opens a tab" rationale is now wrong: an absent role is an agent and must be refused.
   - Make the agent refusal the FIRST statement of `spawnTabAgent`, before the `s.Spawner == nil` check:

     ```go
     // Matrix Rule 2: an agent is headless always. Checked on the RESOLVED role
     // (an empty role is DefaultRole, i.e. agent) and here, after the mode has
     // resolved to a tab, so it fires whatever chose the tab — the argument,
     // SUPERVISOR_SPAWN_MODE, spawn.mode or the built-in default — and before
     // enforceCaps, so a refused call never reserves a start slot.
     if err := RefuseAgentTab(ctx, body.Role); err != nil {
     	return "", err
     }
     ```

     `spawnTabAgent` is reached only when `resolution.Mode != SpawnModeHeadless`, so headless agent spawns are untouched.
   - In `spawnToolDefinitions`, `ToolSpawnAgent`: append to the description string one sentence: `"An agent role (the default when `role` is omitted) is headless only: a tab spawn of an agent is refused (role-mode matrix Rule 2), so a tab requires `role: human` or `role: manager`."` — keep backtick style consistent with the surrounding string. Change the `"role"` property text to `"manager, agent or human; defaults to agent. An agent is headless only — a tab needs human or manager"`.
   - Do NOT touch the A2A branch of `spawnAgent` (`a2a.SpawnArgs{... Interactive: body.Interactive, Role: body.Role ...}`): that worker is created on a controller, not in a wrapper tab, and is out of scope for this spec.

5. **Flagged reopen tick** — NO production change in `pkg/flagged_reopen.go` or `pkg/flagged_tick.go`. `isHeadlessRow` and the routing stay as they are (rerouting is spec `open-tick-reroute`, a non-goal). The tab spawner's refusal surfaces as the `"spawn tab"`-wrapped error from `Reopen`, which `tickFromRows` already counts in `Refused` as `spawn failed: ...`. Requirement 7 adds the test that proves it.

6. **Rewrite the existing tests that relied on an agent tab.** Each of the 22 specs below currently opens a tab with an empty or `agent` role and fails after requirements 1–4 (verified by a dry run). Fix each by giving the call a non-agent role that preserves the spec's intent — `Role: RoleHuman` in Go literals, `"role":"human"` in JSON arguments — unless a different role is named below. Do not weaken any existing assertion.

   `pkg/spawn_cwd_test.go`, Describe `ResolveRole`:
   - BeforeEach fixture: remove the `"Agents": {Chip: new("pink"), WindowID: new(16)}` entry.
   - `defaults an empty role to agent` → replace with `refuses an empty role, which defaults to agent`: `ResolveRole(ctx, reader, "")` returns a nil resolution and an error satisfying `errors.Is(err, ErrAgentTab)` (use stdlib `errors.Is` or `github.com/bborbe/errors`'s `Is`) whose text contains `Rule 2`.
   - `returns an unresolved resolution when the role map lacks the purpose` → call it with `"human"` instead of `"agent"`; assertions unchanged.
   - Add `resolves manager to the Managers window and chip` (orange / 14) and `resolves human to the Direct window` (12) if not already covered.

   `pkg/tab_spawn_test.go`, Describe `tabSpawner.Spawn`:
   - BeforeEach: change the role-map fixture key from `"Agents"` to `"Managers"` (keep `Chip: new("pink"), WindowID: new(16)`).
   - `refuses an empty prompt before opening a tab`, `opens one tab under the vault's launcher and files a row carrying the agent id`, `sends the role colour as its own submit`, `does not adopt a session that already held the tab name`, `returns an empty identity and files no row when the tab never registers`, `suffixes a tab name that is already held`: add `Role: RoleManager` to every `TabSpawnRequest` (manager, so `sends the role colour` still sees `/color pink` from the `Managers` fixture). The empty-prompt spec is not in the failing list but would now pass for the wrong reason — give it the role too.

   `pkg/tab_spawn_test.go`, Describe `RosterToolServer.spawnLocalTarget`:
   - `serves a local spawn with no target`, `refuses resume, which the tab launcher would drop`, `refuses when no spawner is wired`, `refuses a tab spawn with no prompt, naming the headless escape`: add `Role: RoleHuman`.
   - `refuses a policy the tab worker would never consult`, `refuses an env override that would widen the worker`: not failing, but now pass only because of the agent refusal — add `Role: RoleHuman` so they test their own refusal.

   `pkg/spawn_mode_test.go`, Describe `RosterToolServer.spawnLocalTarget mode resolution`:
   - `SC1: spawn.mode interactive + no argument opens a tab`, `SC2: interactive:true opens a tab under spawn.mode headless`, `falls to the interactive default when nothing is configured`: add `Role: RoleHuman`.

   `pkg/spawn_limits_test.go`, Describe `RosterToolServer spawn caps`:
   - `refuses a tab spawn at the cap without opening a pane`: add `Role: RoleHuman`.
   - `passes operator_named through from the wire to the worker cap`: JSON becomes `{"prompt":"x","role":"human","operator_named":true}`.
   - `consults the manager cap only for a manager spawn`: the second call changes from `Role: RoleAgent` to `Role: RoleHuman` (its point is "a non-manager tab does not consult the manager cap").
   - `enforces a scratch config's cap on the tab path through spawn_agent`: both JSON payloads gain `"role":"human"`.
   - `refuses a bad argument before consulting the caps`, `does not open a pane when the start rate refuses`: not failing, but now pass for the wrong reason — add `Role: RoleHuman`.

   `pkg/spawn_limits_test.go`, Describe `spawn caps after review`:
   - `delivers the response of a start paced past the server's WriteTimeout`: the JSON-RPC `arguments` become `{"prompt":"x","role":"human"}`.

   `pkg/roster_shipping_mode_test.go`:
   - `keeps the tab refusal for shipping`: JSON gains `"role":"human"`.

   `pkg/roster_resume_test.go`:
   - `still refuses a resume on the tab path`: JSON gains `"role":"human"`.

   `pkg/roster_role_test.go`, Describe `RosterToolServer.spawnLocalTarget role refusal`:
   - `refuses an explicit role: agent spawn that asks for a tab`: change the substring assertion from `"role: agent"` to `"Rule 2"` and add `"interactive=false"`; keep `spawner.requests` empty.
   - `still opens when the role is absent` → replace with `refuses an absent role, which defaults to agent, when it asks for a tab`: `Interactive: &interactive` (true), no `Role` → error containing `Rule 2` and `interactive=false`, `spawner.requests` empty. Delete the comment above it about argument-vs-resolved-role keying.
   - `still opens for an explicit role: human tab spawn`: unchanged.

   After these edits, run `go test ./pkg/... -count=1` and confirm there are NO failures. If any other spec fails because it opened a tab with an empty/agent role, apply the same fix (non-agent role preserving intent) and list it in the completion report alongside the ones above.

7. **New tests for the new behaviour** (Ginkgo v2 `ginkgo.It` / Gomega, package `pkg`, existing fakes only):

   a. **AC3 — tab spawner refuses an agent** (`pkg/tab_spawn_test.go`, inside `tabSpawner.Spawn`): a table or loop over `Role: "agent"`, `Role: ""` and `Role: "  agent "`, each with `Vault: "v1", Prompt: "do it"`. Each returns a nil result and an error with `errors.Is(err, ErrAgentTab)`, text containing `Rule 2` and `interactive=false`; afterwards `tabs.spawned` is empty, `tabs.texts` is empty and `ledger.posts` is empty.

   b. **AC6 — stale key ignored** (`pkg/tab_spawn_test.go`, inside `tabSpawner.Spawn`): build a `tabSpawner` whose `roleMap` is `&fakeRoleMap{entries: map[string]RoleMapEntry{"Agents": {Chip: new("pink"), WindowID: new(16)}, "Managers": {Chip: new("orange"), WindowID: new(14)}}}`. An agent request (`Role: "agent"`) is refused as in 7a, and `tabs.spawned` is empty (so nothing opened in window 16). A manager request on the same spawner opens one tab with `*tabs.spawned[0].WindowID == 14`.

   c. **AC5 — human and manager windows** (`pkg/tab_spawn_test.go`): with role map `{"Managers": {Chip: new("orange"), WindowID: new(14)}, "Direct": {Chip: new("cyan"), WindowID: new(12)}}`, a `Role: "human"` spawn opens one tab with `WindowID` 12 and a `Role: "manager"` spawn one tab with `WindowID` 14. Skip if an existing spec already pins both; say which in the report.

   d. **AC4 — `spawn_agent` refuses an agent tab whatever the mode source** (`pkg/spawn_mode_test.go`, inside `RosterToolServer.spawnLocalTarget mode resolution`, using the `server(sources)` helper, `Vault: "v1", Prompt: "go"`):
      - `interactive: true` (`boolPtr(true)`) + `Role: RoleAgent` under `SpawnModeSources{}` → error containing `Rule 2` and `interactive=false`; `spawner.requests` and `supervisor.spawned` empty.
      - `interactive: true` + no role → same.
      - no `Interactive`, `Role: RoleAgent`, `SpawnModeSources{Env: "interactive"}` → same refusal; additionally assert `Expect(errors.Is(err, ErrAgentTab)).To(BeTrue())` (the resolved source was `env`; it was refused by the role rule, not by mode resolution).
      - no `Interactive`, no role, `SpawnModeSources{Config: configMode("interactive")}` → same refusal.
      - `interactive: false` + `Role: RoleAgent` under `SpawnModeSources{Config: configMode("interactive")}` → no error, `supervisor.spawned` has length 1, `spawner.requests` empty.
      - no `Interactive`, `Role: RoleAgent`, `SpawnModeSources{Config: configMode("headless")}` → no error, headless (`supervisor.spawned` length 1).

   e. **Refusal precedes caps** (`pkg/spawn_limits_test.go`, inside `RosterToolServer spawn caps`): `server := &RosterToolServer{Spawner: &fakeSpawner{}, Caps: &fakeSpawnCaps{}}`, `spawnLocalTarget(ctx, spawnAgentArgs{Prompt: "x", Role: RoleAgent, Interactive: boolPtr(true)}, "spawner")` → error containing `Rule 2`; `caps.workerCalls` empty, `caps.managerCalls` empty, `caps.startCalls == 0`.

   f. **Tool-error boundary** (`pkg/roster_role_test.go`): call `server.callTool(ctx, json.RawMessage(`{"name":"spawn_agent","arguments":{"interactive":true,"role":"agent","prompt":"x","cwd":"/tmp"}}`), "caller")`. Assert the `*rpcError` is nil, the returned value is a `map[string]any` with `"isError" == true`, and its `content[0]["text"]` contains `Rule 2` and `interactive=false`; `spawner.requests` empty. Repeat with `"arguments":{"interactive":true,"prompt":"x","cwd":"/tmp"}` (no role). Read `callTool` / `invoke` first and wire whatever minimal `RosterToolServer` fields the dispatch needs (e.g. `Spawner`); if `spawn_agent` requires a session id, pass `"caller"` as shown.

   g. **Flagged reopen of an interactive agent row is refused** (`pkg/flagged_reopen_test.go`): construct `NewFlaggedReopener(supervisor, spawner, vaults, &fakeLedger{}, steppingClock())` (a separate `&fakeLedger{}` from the tab spawner's) where `spawner` is a real `&tabSpawner{tabs: tabs, registry: &fakeRegistry{names: map[string][]string{}}, roleMap: &fakeRoleMap{entries: map[string]RoleMapEntry{"Agents": {Chip: new("pink"), WindowID: new(16)}}}, vaults: vaults, ledger: &fakeLedger{}, now: steppingClock(), sleep: func(time.Duration) {}}` (tabs = `&fakeTabClient{}`), `vaults` a `&fakeVaults{vaults: []Vault{{Name: "v1", Path: <GinkgoT().TempDir()>, ClaudeScript: "/scripts/cc-v1", WorkerLauncher: "/scripts/cc-v1"}}}`, and `supervisor` a `&recordingSupervisor{}` from `pkg/flagged_tick_test.go` (it implements `Spawn`, `Send` and `Stop` and records `spawns []SpawnOptions`; `headlessSupervisor` has no `Stop` and does not satisfy `FlaggedReopenSupervisor`). `Reopen(ctx, FlaggedRow{Kind: "task", Vault: "v1", Name: "T", Role: RoleAgent, Mode: SpawnModeInteractive})` returns an error containing `Rule 2`; `tabs.spawned` empty; `supervisor.spawns` is empty. A second case with `Role: RoleHuman, Mode: SpawnModeInteractive` reaches the tab client (`tabs.spawned` length 1) — proving the refusal is role-specific. Change no production code for this test.

8. **CHANGELOG** — insert a `## Unreleased` section directly above `## v0.38.4` (blank line before and after the heading, matching the file's spacing), with one bullet:

   ```
   - fix: **the wrapper refuses an agent tab instead of opening it in whatever window has focus.** The role table no longer carries an agent → window entry (the operator removed that WezTerm window on 2026-11-12, so an agent tab resolved no window and landed in the focused one). The tab spawner and the `spawn_agent` tab path now refuse any request whose role resolves to `agent` — explicit, or defaulted from an empty `role` — before any pane, colour, cap reservation or ledger row, with an error naming role-mode matrix Rule 2 and `interactive=false`. This holds whatever chose the tab (argument, `SUPERVISOR_SPAWN_MODE`, `spawn.mode` or the default), and the flagged reopen tick now counts an interactive agent row as refused. Headless spawns and human and manager tabs are unchanged.
   ```

   The bullet must contain the phrase `agent tab` so `awk '/^## /{s=$0} /agent tab|Agents/{print s}' CHANGELOG.md` prints `## Unreleased` (no existing line in the file matches that pattern today, so `## Unreleased` must be the only output). Do not edit any released section.

9. Do not change `docs/open-endpoint.md` or any other doc beyond the tool description strings in requirement 4.
</requirements>

<constraints>
Copied from the spec:

- Headless `spawn_agent`, `spawn_session`, `POST /api/1.0/sessions`, the spawn caps and the ledger schema are unchanged.
- Human tabs keep their vault-family chip, and manager tabs keep the `Managers` window and chip.
- The tab spawner's existing order holds: every refusal is decided before the first side effect.
- `agent` stays a valid role for headless spawns, ledger rows and the spawn caps; it just never resolves to a window or a role-map chip.
- A role map that still carries a stale agent key changes nothing — the key is never read.
- Do NOT auto-convert an agent tab request into a headless spawn. A refusal keeps the mistake visible.
- Do NOT change how the spawn mode is resolved (argument, then `SUPERVISOR_SPAWN_MODE`, then `spawn.mode`, then the built-in default) — `ResolveSpawnMode` and `resolveSpawnMode` are untouched.
- Do NOT add a flag, environment variable or config key that re-enables agent tabs. The invariant is the point.
- Out of scope: the open endpoint (spec `open-endpoint`), the tick reroute (spec `open-tick-reroute`), the A2A `spawnAgent` branch, and the `claude-supervisor` plugin's own agent mappings.

General:

- Do NOT commit — dark-factory handles git.
- Existing tests must still pass (after the role rewrites listed in requirement 6, which change no assertion's meaning).
- Anchor edits by function/identifier names; the line numbers quoted above are hints only.
</constraints>

<verification>
Run each, in the repo root:

```
make precommit
go test ./pkg/... -count=1 -run 'TabSpawn|RoleMap|Roster' -v
go test ./pkg/ -count=1 -ginkgo.focus='tabSpawner|ResolveRole|spawnLocalTarget|spawn caps|flagged reopen|role refusal'
grep -rnw --exclude='*_test.go' 'Agents' pkg cmd main.go      # expect no output
awk '/^## /{s=$0} /agent tab|Agents/{print s}' CHANGELOG.md     # expect exactly: ## Unreleased
grep -n 'Rule 2' pkg/role_map.go pkg/roster_mcp.go
```

`make precommit` must exit 0. The `Agents` grep must print nothing (record it next to the 2-line baseline from requirement 0 in the completion report). The completion report must list every test spec rewritten under requirement 6 (and any extra ones found) and every new spec added under requirement 7.
</verification>
