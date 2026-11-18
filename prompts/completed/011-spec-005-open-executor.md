---
status: completed
spec: [005-open-endpoint]
summary: Composed the pure open decision and tab opener with the existing headless spawn into OpenExecutor (OpenRequest with probe/pane activation, OpenTick without either), added readOpenPage and the shared startHeadless spawn, covered every required AC and failure mode with tests, recorded the AC17 mutation run, and updated CHANGELOG.md.
execution_id: 9881bf24-5a56-5157-8303-c8eca57d0834-exec-011-spec-005-open-executor
dark-factory-version: dev
created: "2026-11-18T18:59:25Z"
queued: "2026-11-18T19:12:27Z"
started: "2026-11-18T19:33:54Z"
completed: "2026-11-18T19:45:46Z"
---

# Open executor: request and tick caller modes over the open decision

<summary>
- One executor opens a list of rows in order and returns exactly one verdict per row; one row's refusal never stops the rows after it.
- For each row it reads the vault page, the live session registry and, for human and manager rows, the pane a live session sits in, then lets the pure decision choose.
- Agent rows always start or resume headless through the same in-process spawn `spawn_agent` uses — launcher model and servers, caps, start-rate, finished-task guard, opening turn, ledger row — and the role map is never read for them.
- Before any headless spawn requested by a session, the answer-path probe runs for that session; any non-pass refuses with the probe's reason and the remedy "arm this session's manager loop and re-run".
- Human and manager rows read the role map on every call, refuse a missing key or the Hold window, then open through the tab opener; a live row with a pane is focused instead.
- A defaulted role or an absent or corrected mode is written back with `vault-cli`; a failed write is a warning and never changes the verdict.
- A second concurrent open of the same row is refused as in-flight, and every row writes one log line naming role, mode, branch, path, window and reason.
- A separate tick entry point, reachable only from code, runs the same decision without the probe and without focusing panes, for the flagged reopen tick to adopt later.
- A replay of the 2026-11-18 batch (13 agent rows, 5 stored interactive, one human, one goal) proves 13 headless resumes and exactly 2 tabs, none in Hold.
</summary>

<objective>
Compose prompt 1's `DecideOpen` / `ResolveOpenWindow` / `FormatOpenLogLine` and prompt 2's `OpenTabOpener` with the existing headless spawn into an `OpenExecutor` with two entry points — `OpenRequest` (REST and MCP, with the answer-path probe and pane activation) and `OpenTick` (no probe, no activation) — implementing `docs/open-endpoint.md` § Decision order items 1–8, § Answer-path probe, § Write-back and § Agent rows. Prompt 4 exposes `OpenRequest` over HTTP and MCP; spec `open-tick-reroute` will call `OpenTick`. The caller mode is chosen by the entry point and never by anything in the request.
</objective>

<context>
Read the repo `README.md` for project conventions (there is no `CLAUDE.md` in this repo).

Read these files fully before changing anything:

- `docs/open-endpoint.md` — § Decision order (all eight items), § Answer-path probe, § Write-back, § Agent rows, § Known limitations.
- `specs/in-progress/005-open-endpoint.md` — DBs 1, 3, 5, 6, 7, 8; ACs 6, 8, 9, 10, 12, 14, 15, 16, 17, 18, 19, 20, 24; the Failure Modes table (every row must map to behaviour here or in prompts 1–2).
- `pkg/open_decision.go` (prompt 1) — `OpenRow`, `OpenVerdict` and its `refuse` method, `OpenRefusal`, `OpenPage`, `OpenLiveEntry`, `OpenRowFacts{Row, Page, Live, LiveErr, PaneFor}`, `OpenDecision{Verdict, Resume, Shipping, OperatorNamed, WriteRole, WriteMode}`, `DecideOpen`, `ResolveOpenWindow(role, vault, entries, readErr) (OpenWindow, *OpenRefusal)`, `FormatOpenLogLine`, the `OpenKind*` / `OpenBranch*` / `OpenPath*` / `OpenReason*` constants, `OpenAnswerPathRemedy`, `openRemedies`.
- `pkg/open_tab.go` (prompt 2) — `OpenTabRequest`, `OpenTabResult{PaneID, SessionID, TabName, Launcher, Note}`, `OpenTabOpener`, `OpenModeSource`; `pkg/script_runner.go` — `ScriptRunner`, `ScriptResult{Stdout, Stderr, ExitCode}`, `ExecScriptRunner`; `pkg/spawn_guards.go` — `enforceSpawnCaps`, `refuseTerminalTask`.
- `pkg/roster_mcp.go` — `(s *RosterToolServer) spawnLocalAgent(ctx, body spawnAgentArgs, sessionID string, resolution SpawnModeResolution) (string, error)`: the `Interactive` refusal, then `s.Supervisor == nil`, `headlessCwd`, `refuseTerminalResume`, `refusePromptlessResume`, `enforceCaps(ctx, body, SpawnModeHeadless)`, `headlessPermissionMode(body.Shipping)`, `Supervisor.Spawn(SpawnOptions{Cwd, Model: s.headlessModel(...), PermissionMode, Label, Role, Policy, Resume, MCPConfigs: s.headlessMCPConfigs(...)})`, `Supervisor.Send` with `Stop(context.Background(), id)` on failure, `recordSpawn`, then the JSON response. `spawnAgentArgs` fields `Task`, `Vault`, `Label`, `Prompt`, `Resume`, `Role`, `Shipping`, `OperatorNamed`. `SpawnModeResolution{Mode, Source}`. `SessionHeader = "X-Claude-Code-Session-Id"`. `ShippingPermissionMode = "acceptEdits"`. The file must stay under 2000 lines.
- `pkg/session_registry.go` — `SessionRegistry.Live(ctx) ([]RegistryEntry, error)` (an unreadable directory is an error, never an empty slice).
- `pkg/pane_join.go` — `PaneJoiner.PanesForPIDs(ctx, pids []int) (map[int]string, error)`.
- `pkg/tab.go` — `TabClient.Activate(ctx, paneID) error`.
- `pkg/role_map.go` — `RoleMapReader.Read(ctx)`, `RoleHuman`, `RoleManager`, `RoleAgent`.
- `pkg/terminal_task.go` — `readFrontmatter(ctx, path) (map[string]string, error)`.
- `pkg/spawn_cwd.go` — `taskFolders` (`25 Tasks`, `24 Tasks`), `hasDotDotSegment`, `Vault`, `VaultLister`, `findVault`; `pkg/manager_kind.go` — `goalDirs` (`24 Goals`, `23 Goals`), `topicDirs` (`23 Topics`).
- `pkg/flagged_rows_source.go` — `CommandRunner.Run(ctx, name, args...) ([]byte, error)`, `ExecCommandRunner`.
- `pkg/flagged_reopen.go` — the headless reopen recipe this executor's agent path matches (`reopenHeadless`), for reference only; do not change it.
- Test fixtures (package `pkg`): `fakeTabClient` (`pkg/tab_spawn_test.go`), `fakeRegistry` and `fakeRoleMap` (`pkg/spawn_cwd_test.go`), `fakeVaults`, `fakeLedger`, `steppingClock()`, `fakeSpawnCaps` (`pkg/spawn_limits_test.go`), `headlessSupervisor` (`pkg/spawn_mode_test.go`, embeds `rosterSupervisor`, records `spawned []SpawnOptions`), `fakeJoiner{panes, err}` (`pkg/fleet_pusher_test.go`), `fakeScriptRunner` (`pkg/open_tab_test.go`, prompt 2).
- `.golangci.yml` — `funlen` 80/50, `gocognit` 20, `nestif` 4, `dupl`, file length 2000.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
</context>

<requirements>
1. **Headless spawn shared with `spawn_agent`, in a new file `pkg/open_headless.go`.** Move the part of `spawnLocalAgent` from the `if s.Supervisor == nil` check through `s.recordSpawn(...)` — explicitly INCLUDING the `cwd, err := s.headlessCwd(ctx, body)` call, so the open path resolves the vault directory exactly as spawn_agent does — into

   ```go
   // startHeadless runs every guard of the in-process headless spawn — the
   // terminal-task resume guard, the promptless-resume refusal, the caps and
   // start rate — then spawns with the launcher's model and MCP configs, sends
   // the opening turn (stopping the child if that fails) and files the ledger
   // row. spawn_agent and the open endpoint both call it, so neither carries a
   // second copy of a guard.
   func (s *RosterToolServer) startHeadless(
   	ctx context.Context,
   	body spawnAgentArgs,
   	sessionID string,
   	resolution SpawnModeResolution,
   ) (*Session, string, string, error)
   ```

   returning the session, the permission mode and its source. `spawnLocalAgent` keeps its `Interactive` refusal, calls `startHeadless`, and builds the same JSON response as today from the three values. Every refusal and error text is unchanged; all existing `spawn_agent` tests pass unmodified.

   In the same file add:

   ```go
   // OpenHeadlessRequest is one agent row the open executor starts or resumes.
   type OpenHeadlessRequest struct {
   	Vault  string
   	Task   string
   	Resume string
   	// Prompt is the opening turn: `<first_step>\n\n/vault-cli:work-on-task "<task>"`.
   	Prompt          string
   	Shipping        bool
   	OperatorNamed   bool
   	CallerSessionID string
   }

   // OpenHeadlessSpawner starts or resumes an agent row headless.
   type OpenHeadlessSpawner interface {
   	SpawnOpenHeadless(ctx context.Context, request OpenHeadlessRequest) (*Session, error)
   }

   // SpawnOpenHeadless is the open endpoint's agent path: spawn_agent's own
   // headless spawn with role agent, the task as label, and mode_source open.
   func (s *RosterToolServer) SpawnOpenHeadless(
   	ctx context.Context,
   	request OpenHeadlessRequest,
   ) (*Session, error)
   ```

   `SpawnOpenHeadless` calls `s.startHeadless(ctx, spawnAgentArgs{Vault: request.Vault, Task: request.Task, Label: request.Task, Role: RoleAgent, Resume: request.Resume, Prompt: request.Prompt, Shipping: request.Shipping, OperatorNamed: request.OperatorNamed}, request.CallerSessionID, SpawnModeResolution{Mode: SpawnModeHeadless, Source: OpenModeSource})` and returns the session or the error unchanged (never wrapped — the cap, rate and terminal-task messages pass through verbatim).

2. **Page read, in a new file `pkg/open_page.go`:** `func readOpenPage(ctx context.Context, vault Vault, kind, name string) OpenPage`.
   - Folders by kind: task → `taskFolders`, goal → `goalDirs`, topic → `topicDirs`.
   - Defensive name guard (the surfaces validate first): a name containing `/`, `\` or a `..` segment, or an empty name → `OpenPage{}` (not found).
   - `strings.TrimSpace(vault.Path) == ""` → `OpenPage{ReadErr: errors.Errorf(ctx, "vault %q has no path", vault.Name)}`.
   - For each folder in order: `path := filepath.Join(vault.Path, folder, name+".md")`; `os.Stat` → not-exist → next folder; any other stat error → `ReadErr`; found → `readFrontmatter(ctx, path)` → error → `OpenPage{Found: true, Path: path, ReadErr: err}`; success → `OpenPage{Found: true, Path: path, Fields: fields}`.
   - No folder holds it → `OpenPage{}`.

3. **The executor, in a new file `pkg/open_executor.go`.**

   ```go
   // OpenExecutor opens rows through the open decision, one verdict per row in
   // request order.
   //
   // The two methods ARE the caller modes, and nothing in a row selects one:
   // OpenRequest serves POST /api/1.0/open and open_rows and runs the
   // answer-path probe and pane activation; OpenTick serves the flagged reopen
   // tick, which is the daemon rather than a session, so it runs no probe and
   // focuses no pane (docs/open-endpoint.md § Decision order item 6,
   // § Answer-path probe). Rows are validated by the caller (OpenSurface,
   // prompt 4) before either is reached.
   type OpenExecutor interface {
   	OpenRequest(ctx context.Context, rows []OpenRow, callerSessionID string) []OpenVerdict
   	OpenTick(ctx context.Context, rows []OpenRow) []OpenVerdict
   }

   // OpenExecutorDeps are the executor's collaborators. Every one is required
   // in the daemon; a test supplies fakes.
   type OpenExecutorDeps struct {
   	Vaults   VaultLister
   	Registry SessionRegistry
   	Panes    PaneJoiner
   	RoleMap  RoleMapReader
   	// Tabs activates a live pane on a request-mode jump.
   	Tabs      TabClient
   	TabOpener OpenTabOpener
   	Headless  OpenHeadlessSpawner
   	// Runner runs the vault-cli write-back.
   	Runner CommandRunner
   	// Scripts runs the answer-path probe.
   	Scripts          ScriptRunner
   	AnswerPathScript string
   	// Logf receives the per-row log line and every WARNING; nil means log.Printf.
   	Logf func(format string, args ...any)
   }

   // NewOpenExecutor creates the open executor.
   func NewOpenExecutor(deps OpenExecutorDeps) OpenExecutor
   ```

   The unexported `openExecutor` holds the deps plus `mu sync.Mutex` and `inFlight map[string]struct{}`. The caller mode is an unexported type (e.g. `openCallerMode` with `openModeRequest` / `openModeTick`) set only by the two methods.

4. **Batch loop** (shared by both methods): list the vaults once (`Vaults.List`); then for each row in order:
   - `ctx.Err() != nil` before a row → stop and return the verdicts so far. Rows not reached are not opened and write no log line (Failure Modes: client disconnects mid-batch).
   - Open the row (requirement 5) and append its verdict. A refusal never ends the loop (AC18).
   - Each row's side-effect phase (write-back, pane activation, spawn, bookkeeping) runs on `sideCtx := context.WithoutCancel(ctx)`, so a row already being spawned finishes its bookkeeping when the client disconnects; the reads before it (page, registry, panes, role map, probe) use the request `ctx`.
   - A vault-list error → every row is refused `row-unreadable` with message `vault list unreadable: <err>`; a row whose vault is not in the list → `row-unreadable` naming the vault.

5. **Per row**, in this order (first refusal wins; nothing with a side effect runs for a row whose refusal is already decidable):

   1. **In-flight guard:** key `vault + "/" + kind + "/" + name`. Already held → refused `in-flight`, message `another open of <key> is in progress in this daemon`, `Path none`. Otherwise claim it, and release it (defer) when this row's verdict is final. Per-daemon and in-memory only (`docs/open-endpoint.md` § Known limitations).
   2. **Facts:** `page := readOpenPage(ctx, vault, row.Kind, row.Name)`; `entries, liveErr := Registry.Live(ctx)` mapped to `[]OpenLiveEntry{PID, SessionID, Name}`; `PaneFor` is a closure that, on its first call, runs `Panes.PanesForPIDs(ctx, <every live pid>)` once and caches the map — an error logs `WARNING: open: pane join failed, live rows read as pane-less: <err>` and every pid answers `""`. `decision := DecideOpen(OpenRowFacts{...})`.
   3. **Terminal branches:** `refused` or `yours` → finish (no write). `live` → write back (requirement 6) and finish; nothing is opened.
   4. **`jump`:** write back. Request mode → `Tabs.Activate(sideCtx, PaneID)`; an error refuses `spawn-refused` with the message `activate pane <pane id>: <err.Error()>` (Failure Modes: "spawn-refused naming the pane"; nothing is spawned). Tick mode → no activation; the verdict stays `jump`.
   5. **`resume` / `create`, agent (`Path headless`):**
      - Request mode → the answer-path probe (requirement 7); a refusal stops here.
      - Write back (requirement 6).
      - `session, err := Headless.SpawnOpenHeadless(sideCtx, OpenHeadlessRequest{Vault: row.Vault, Task: row.Name, Resume: decision.Resume, Prompt: workCommand(row), Shipping: decision.Shipping, OperatorNamed: decision.OperatorNamed, CallerSessionID: <request: callerSessionID; tick: "">})`.
      - Error → refused `spawn-refused` with `err.Error()` verbatim as the message (AC19). Success → `SessionID = session.ID`.
      - `RoleMap` is never read for this row (AC6).
   6. **`resume` / `create`, human or manager (`Path tab`):**
      - `entries, readErr := RoleMap.Read(ctx)` — read here, per row, never cached.
      - `window, refusal := ResolveOpenWindow(decision.Verdict.Role, row.Vault, entries, readErr)`; set `WindowKey` from `rolePurposes[role]` before resolving so a refusal still names the key; a refusal → `refuse` (window_id stays null).
      - Write back (requirement 6).
      - `result, err := TabOpener.Open(sideCtx, OpenTabRequest{Kind: row.Kind, Role: role, Subject: row.Name, PagePath: page.Path, Vault: vault, WindowID: window.ID, Chip: window.Chip, Resume: decision.Resume, FirstStep: row.FirstStep, TaskStatus: page.Fields["status"], OperatorNamed: decision.OperatorNamed, SpawnerSessionID: <request: callerSessionID; tick: "">})`.
      - Error → `spawn-refused` with `err.Error()` verbatim. Success → `WindowID = &window.ID`, `SessionID`, `PaneID` from the result, `Message = result.Note`.
   7. **Log:** `Logf("%s", FormatOpenLogLine(verdict))` exactly once per row, for every verdict including refusals and in-flight (AC16).

   `workCommand(row)` is `/vault-cli:work-on-task "<name>"`, prefixed by `row.FirstStep + "\n\n"` when `row.FirstStep != ""` (AC20; `first_step` is composer text only and never reaches an argv or a shell).

   Write the row handling as several small functions (facts, terminal branches, headless open, tab open) so each stays within `funlen` / `gocognit` / `nestif`.

6. **Write-back** — task rows only, only when `decision.WriteRole` / `WriteMode` is non-empty, after every refusal check and before the open action. Each write is `Runner.Run(writeCtx, "vault-cli", "--vault", row.Vault, "task", "set", row.Name, "role", decision.WriteRole)` (then the same with `"mode", decision.WriteMode`): one argv, no shell, the name as one element. `writeCtx` is `context.WithTimeout(sideCtx, 30*time.Second)` (constant `openWriteBackTimeout`). A failure logs `WARNING: open: write-back <field>=<value> for <vault>/<name> failed: <err>` and the open proceeds with the verdict unchanged (AC14; the next call re-derives the same value).

7. **Answer-path probe** (request mode, headless `resume`/`create` only — never for `jump`, `live`, `yours` or tab rows, never in tick mode):
   - `callerSessionID == ""` → refused `answer-path`, message `no X-Claude-Code-Session-Id header, so the answer-path probe has no session to check`; the probe is not run.
   - Otherwise `Scripts.RunScript(ctx, "python3", AnswerPathScript, "--session-id", callerSessionID)`.
   - Run error (missing script, exec failure, timeout) → refused `answer-path` with `err.Error()` as the message — never treated as a pass.
   - Exit code non-zero → refused `answer-path`, message = trimmed stdout, else trimmed stderr, else `answer-path-check.py exited <code>`.
   - Exit 0 → pass.
   - The remedy is always `OpenAnswerPathRemedy` (via `openRemedies`); it never contains `interactive`.

8. **Test-fixture extensions** (in the existing `_test.go` files; existing specs must pass unmodified):
   - `fakeTabClient` (`pkg/tab_spawn_test.go`): add `activated []string` and `activateErr error`; `Activate` appends the pane id and returns `activateErr`.
   - `fakeRegistry` (`pkg/spawn_cwd_test.go`): add `liveErr error`; `Live` returns `f.live, f.liveErr` (`Entries` unchanged).
   - `fakeRoleMap` (`pkg/spawn_cwd_test.go`): add `reads int`, incremented in `Read`.
   - New in `pkg/open_executor_test.go`: `fakeHeadlessOpener` (records `[]OpenHeadlessRequest`, optional `err`, optional `block chan struct{}` + `entered chan struct{}` for AC15), `fakeTabOpener` (records `[]OpenTabRequest`, returns a fixed `OpenTabResult` or `err`), `recordingCommandRunner` (records every argv; optional `err`), and a log capture (`Logf` appending formatted lines to a slice).

9. **Tests** — `pkg/open_executor_test.go` (package `pkg`, Ginkgo/Gomega with prefixes). Build a vault under `ginkgo.GinkgoT().TempDir()` with `25 Tasks/<name>.md`, `24 Goals/<name>.md` and `23 Topics/<name>.md` pages written by a helper taking frontmatter lines. Use full UUIDs for recorded ids. One `It` per bullet:

   a. **AC6 — agent never gets a tab:** a `role: agent`, `mode: interactive` row with a recorded non-live id → `fakeHeadlessOpener` has 1 request whose `Resume` equals the id; verdict `path headless`, `mode headless`, `mode_note "normalised interactive->headless"`; `fakeTabOpener` 0 calls; `fakeRoleMap.reads == 0`. With no recorded id → `Resume ""`, branch `create`.

   b. **AC10 — the probe gates every request-mode headless spawn:** probe exit 1 with stdout `no manager loop armed for this session\n` → `refused`, `reason answer-path`, message `no manager loop armed for this session`, remedy equals `OpenAnswerPathRemedy` and does not contain `interactive`; probe run error → refused with the error text; `callerSessionID ""` → refused, probe 0 calls. Each: headless opener 0 calls. The probe argv is `python3 <AnswerPathScript> --session-id <caller>`. Probe call counts are 0 for a `jump` row, a `live` agent row, a `yours` row and a human `create` (tab) row.

   c. **AC8 / AC9 at the executor:** for a human row with the role map missing `Direct` → `role-map-key-missing`, message names `Direct`, `fakeTabOpener` 0 calls, `window_key "Direct"`, `window_id` nil; an agent row in the same request still makes 1 headless request. For `Direct` equal to `Hold` → `hold-window`, tab opener 0 calls.

   d. **AC12 at the executor:** one row per arm (`missing-row`, `row-unreadable` (a page that is a directory or unreadable file), `truncated-id`, `liveness-unreadable` (`fakeRegistry.liveErr`), `goal-loop-unsupported`, `target-unsupported`) → headless opener 0 calls, tab opener 0 calls, probe 0 calls, runner 0 calls.

   e. **Shipping reaches the headless spawn with the shipping permission mode (AC12):** use a real `*RosterToolServer{Supervisor: &headlessSupervisor{}, Vaults: <fakeVaults with the temp vault>, Ledger: &fakeLedger{}}` as `Headless`; a `shipping: true` agent row → `supervisor.spawned[0].PermissionMode == ShippingPermissionMode`, `Role == RoleAgent`, `Label` = task name; the ledger row's `ModeSource == OpenModeSource`.

   f. **AC13 at the executor:** a human row whose recorded id is live in `fakeRegistry.live` with `fakeJoiner{panes: {pid: "p9"}}` → `jump`, `fakeTabClient.activated == ["p9"]`, nothing spawned; a goal with no recorded id and a live entry `"<goal> Manager"` with a pane → `jump`; with `fakeTabClient.activateErr = stderrors.New("no such pane")` → `spawn-refused`, `message == "activate pane p9: no such pane"`, nothing spawned.

   g. **AC14 — write-back:** a row with absent role and `mode: interactive` → the runner's argvs are exactly `[vault-cli --vault v1 task set T role agent]` then `[vault-cli --vault v1 task set T mode headless]`, both recorded before the headless request (record a shared sequence counter); with the runner returning an error and a recorded non-live id → one `WARNING` line captured and the verdict is still `resume`; a goal row and a topic row → 0 runner calls.

   h. **AC15 — concurrent open of one row:** `fakeHeadlessOpener` blocks on `block` after signalling `entered`; start `OpenRequest` for row T in a goroutine, wait on `entered`, call `OpenRequest` for the same row synchronously → `refused`, `reason in-flight`; close `block`; the first returns `create`; headless request count 1.

   i. **AC16 — one log line per row:** capture `Logf`; an agent row produces exactly one line matching `open: row=v1/task/A role=agent mode=headless branch=create path=headless window=none reason=-`; a human row with Direct window 12 produces exactly one `… path=tab window=12 reason=-` line.

   j. **AC18 — batch continuation:** rows [agent A, missing B, agent C] → 3 verdicts in order (`create`, `refused missing-row`, `create`); headless requests for A and C only.

   k. **AC19 — spawn-refused passes the component's message through,** each with a real component behind the executor:
      - caps: real `*RosterToolServer` as `Headless` with `Caps: &fakeSpawnCaps{workerErr: stderrors.New("the worker fleet is FULL (51 of 50)")}` → `reason spawn-refused`, `message == "the worker fleet is FULL (51 of 50)"`, `headlessSupervisor.spawned` empty;
      - terminal task: real `*RosterToolServer` as `Headless`, a task page `status: completed` with a recorded non-live UUID → `spawn-refused`, the message contains `refused` and `completed`, `headlessSupervisor.spawned` empty;
      - manager-launcher: a goal row with the real opener from prompt 2 (`&openTabOpener{...}` with `fakeTabClient` and a `fakeScriptRunner` returning exit 1, stderr `unsafe launcher: x`) → `message == "unsafe launcher: x"`, `fakeTabClient.spawned` empty.

   l. **AC20 — first_step:** `first_step: "X"` → the headless request's `Prompt == "X\n\n/vault-cli:work-on-task \"A\""`; a human row through the real opener sends `"X\n\n/vault-cli:work-on-task \"H\""` to the pane; without `first_step` each equals the bare command.

   m. **AC24 — tick caller mode:** rows [the AC13 human jump row, an agent `resume` row]:
      - `OpenTick` → the human row `branch jump` with `fakeTabClient.activated` empty; the agent row makes 1 headless request; probe 0 calls (no caller session at all).
      - The same rows through `OpenRequest(..., "caller-1")` → 1 activation and 1 probe call.
      - `OpenTick` still writes back and logs one line per row.

10. **AC17 — the 2026-11-18 batch replay**, in a new file `pkg/open_replay_test.go`:
    - One `OpenRequest` with 15 rows: 13 `role: agent` task rows, each with a recorded non-live UUID, 5 of them stored `mode: interactive` and the rest `mode: headless`; 1 `role: human` task; 1 goal.
    - Role map (`fakeRoleMap`): `Direct {window 12, chip cyan}`, `Managers {window 14, chip orange}`, `Hold {window 2}`, and NO `Agents` key.
    - `Headless` is `fakeHeadlessOpener`. `TabOpener` is the real prompt-2 opener over a `fakeTabClient`, with a `fakeScriptRunner` answering manager-launcher with `/scripts/cc-mgr\tmanager-default` and the probe with exit 0.
    - Assert: 13 headless requests, every one with a non-empty `Resume`; `fakeTabClient.spawned` has exactly 2 entries; none of them is for an agent row (their `TabName`s are the human task and `<goal> Manager`); no spawned `WindowID` equals 2; the human tab's window is 12 and the goal's 14.
    - **Mutation run (required, recorded once):** temporarily change `openPathFor` in `pkg/open_decision.go` (prompt 1) and its one call site so it also receives the row's stored `mode` and returns `OpenPathTab` whenever that stored mode is `interactive` — a router that selects the path by stored mode. Expected effect on this fixture: the 5 stored-`interactive` agent rows take the tab branch, `ResolveOpenWindow` refuses them `spawn-refused` (an agent has no role-map key), so the replay sees 8 headless requests against the expected 13 (and still 2 tab spawns); the first failing assertion should be the headless-count `Expect` reporting 8 vs 13 — if your output shows different counts, the fixture or the mutation is wrong, fix it before recording. Run `go test ./pkg/ -count=1 -ginkgo.focus='2026-11-18 batch replay'`, copy its failing output (the assertion that failed and the counts) into the completion report under a heading `AC17 mutation run`, then revert the mutation and re-run to green. The final tree must contain no trace of the mutation.

11. **Self-check:** before finishing, re-run `<verification>`; walk ACs 6, 8, 9, 10, 12, 14, 15, 16, 17, 18, 19, 20, 24 and each Failure Modes row (vault read fails, write-back fails, probe missing/crashing/timing out, registry unreadable, launcher/ceiling refuses, cap/rate/terminal refusal mid-batch, headless opening turn fails — the child is stopped by `startHeadless`, pane activation fails, concurrent open, client disconnect) against the code, and name the covering `It` for each in the completion report.
</requirements>

<constraints>
Copied from the spec:

- Agent rows go headless, always, and are probed first: an agent `resume` or `create` uses the in-process headless spawn with the worker launcher's model and MCP configs, one opening work turn, and a spawn-ledger row. In request mode the answer-path probe runs first, keyed on `X-Claude-Code-Session-Id`. Any non-pass refuses with the probe's reason and the agent remedy, which never says "open interactive". The role map is never read for an agent row.
- The executor has two caller modes, and the mode is set by the entry point, never by the request. Tick mode runs no answer-path probe and activates no pane; everything else (role/mode resolution, write-back, window rules, refusals, spawns, the log line) is identical. Neither HTTP nor MCP can select tick mode.
- Write-back mirrors `/supervisor:open` Step 0.6: `vault-cli --vault <v> task set "<task>" role|mode <value>`, run as an argv. A write failure is logged and never changes the verdict.
- Branch from the wrapper's own registry. A truncated id, or an unreadable registry, is refused rather than treated as "not live". This operation reads the session registry directly and never consumes `GET /api/1.0/liveness`.
- Every refusal is decided before the first side effect. Refusals from spawn caps, the start-rate limiter, the terminal-task guard and the launcher or ceiling scripts are reported as `spawn-refused` with their own message. A concurrent open of one row is refused `in-flight`. One log line per row lets the operator quote role, mode, path and window id from the wrapper log alone.
- Existing guards apply to every spawn this operation makes, with none bypassed or duplicated: spawn caps, the start-rate limiter, the terminal-task resume guard, spawn-ledger filing, and the tab client's pane hygiene.
- The role map is read on every call and never cached.
- One implementation per external check: `answer-path-check.py`, `manager-launcher.py` and `topic-spawn.py` are invoked, never re-derived, from their daemon-flag paths.
- `first_step` is composer text only; it is typed into a worker's composer and never placed on a command line.
- Recorded session ids are UUID-validated (`truncated-id`) before reaching any spawn argument.
- Probe, scripts and spawns stay bounded; the operation adds no retry loop.
- Known limitation, accepted: the duplicate-create guard is per-daemon and in-memory. Do not add a persistent guard.
- Existing routes are unchanged, and so are `spawn_agent`, `spawn_session` and the flagged reopen tick (`pkg/flagged_reopen.go`, `pkg/flagged_tick.go` are not edited — rerouting the tick is spec `open-tick-reroute`).
- Do NOT add a fallback window, an "open interactive anyway" flag for agent rows, or a probe-skip flag.

General:

- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Anchor edits by identifier names, never line numbers. Keep `pkg/roster_mcp.go` under 2000 lines.
</constraints>

<verification>
Run in the repo root:

```
make precommit
go test ./pkg/ -count=1 -ginkgo.focus='open executor|OpenExecutor|2026-11-18 batch replay|spawnLocalTarget|spawn caps|resume' -ginkgo.v
wc -l pkg/roster_mcp.go
! grep -rn 'openModeTick\|OpenTick' pkg/handler
```

`make precommit` must exit 0. The focused run must include the AC17 replay and the existing `spawn_agent` specs, all passing. `wc -l` must print a number below 2000. The completion report must contain the `AC17 mutation run` section with the failing output.
</verification>
