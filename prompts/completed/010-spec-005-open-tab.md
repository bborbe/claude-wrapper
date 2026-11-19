---
status: completed
spec: [005-open-endpoint]
summary: Added the open endpoint's tab half — OpenTabOpener (human Direct / manager Managers, create and resume), ScriptRunner, shared spawn guards, tab resume and pane hygiene, and ready-gated sends — plus specs where AC7 is covered by the `AC7 ...` Its under `openTabOpener` and AC19 by `AC19 passes a refusing manager-launcher.py line through and spawns nothing`, `AC19 passes a cap refusal through and spawns nothing`, `AC19 refuses a resume of a completed task and spawns nothing`, and `AC19 passes a refusing topic-spawn.py second line through and spawns nothing`.
execution_id: 9881bf24-5a56-5157-8303-c8eca57d0834-exec-010-spec-005-open-tab
dark-factory-version: dev
created: "2026-11-18T18:59:25Z"
queued: "2026-11-18T19:12:27Z"
started: "2026-11-18T19:23:01Z"
completed: "2026-11-18T19:33:52Z"
---

# Tab resume and the manager open recipe for the open endpoint

<summary>
- A terminal tab can now continue a recorded conversation instead of always starting a new one; only the open endpoint uses this.
- Human rows open as a tab under the vault's Claude launcher, coloured by the vault family, and receive their work command once the composer is ready.
- Goal and topic managers open as a `<subject> Manager` tab coloured orange, on the launcher the supervisor's own launcher script resolves; a topic first passes the supervisor's manager-ceiling script.
- A goal manager receives its two start commands and a topic manager its one, each only after the pane reports a ready composer, with waits of one and five minutes; a command that never finds a ready composer is reported, not sent, and the tab stays open.
- A launcher or ceiling script refusal, a spawn-cap or start-rate refusal, and a resume of an already-finished task all refuse before any tab opens, carrying the refusing component's own words.
- Every tab this path opens clears the spawning session's identity variables and marks itself a CLI session, as the supervisor's own recipe does, and files a spawn-ledger row.
- The existing cap check and finished-task check are shared rather than copied, and existing spawns behave exactly as before.
</summary>

<objective>
Add the tab half of the open endpoint: an `OpenTabOpener` that opens a human row in its Direct window or a goal/topic manager in its Managers window, `create` or `resume`, following `docs/open-endpoint.md` § Human rows and § Manager rows, behind every existing guard (spawn caps, start rate, terminal-task resume guard, spawn ledger, pane hygiene). The executor in prompt 3 calls it with a window already resolved by prompt 1's `ResolveOpenWindow`; existing tab spawns (`spawn_agent`, the flagged tick) are untouched.
</objective>

<context>
Read the repo `README.md` for project conventions (there is no `CLAUDE.md` in this repo).

Read these files fully before changing anything:

- `docs/open-endpoint.md` — § Human rows: Direct tab, § Manager rows: goal and topic recipe (launcher script argv, topic ceiling argv, spawn line, start-command table with 60 s / 300 s waits, timeout semantics). This prompt implements both sections; the argv shapes there are the contract.
- `specs/in-progress/005-open-endpoint.md` — DB6, AC7, AC19, Failure Modes rows for the launcher/ceiling scripts and the start-command timeout.
- `pkg/open_decision.go` — depends on prompt 1 for the `OpenKind*` constants (`OpenKindTask`, `OpenKindGoal`, `OpenKindTopic`) this prompt compares `OpenTabRequest.Kind` against.
- `pkg/tab.go` — `TabClient` (`Spawn`, `Activate`, `SendText`, `Text`), `TabSpawnOptions{Cwd, Launcher, TabName, Prompt, Env, WindowID *int, ExitRecord}`, `BuildTabInner(options TabSpawnOptions) string` (its three load-bearing details in the doc comment), `shellQuote`, `SendWhenReady`, `SendWhenReadyWith(ctx, tabs, paneID, text, now, ready, confirmed)`, `waitComposerWith(ctx, tabs, paneID, timeout, now, ready)`, `confirmSubmissionWith`, `composerDeadline` (8 s), `ComposerReady`, `SubmissionConfirmed`.
- `pkg/tab_spawn.go` — `tabSpawner.Spawn` (the order: refusals, then pane, then a tail on `context.WithoutCancel`), `requirePaneID(ctx, paneID) error`, `registrationDeadline` / `registrationInterval`, `(s *tabSpawner) awaitRegistration(ctx, tabName, preexisting) string`, `(s *tabSpawner) postLedger`.
- `pkg/tab_name.go` — `PreexistingHolders(ctx, registry, tabName) map[string]struct{}`, `FindRegisteredByName(ctx, registry, name, exclude) (string, error)`.
- `pkg/roster_mcp.go` — `(s *RosterToolServer) enforceCaps(ctx, body spawnAgentArgs, mode string) error` (worker cap with mode blanked for a manager, manager cap with `ManagerKindForLabel` and `s.vaultRoots(ctx)`, then `ReserveStart`); `(s *RosterToolServer) refuseTerminalResume(ctx, body, cwd) error` (its tail builds the `ErrTaskTerminal` refusal from a `*TaskRef`); `terminalTaskStatus(status string) bool`; `(s *RosterToolServer) vaultRoots(ctx) []string`. The file is 1967 lines and `revive` caps files at 2000 — put new code in new files, and make the extractions below shrink it, not grow it.
- `pkg/spawn_limits.go` — `SpawnCaps` (`CheckWorker(ctx, mode, operatorNamed)`, `CheckManager(ctx, kind, roots, operatorNamed)`, `ReserveStart(ctx)`).
- `pkg/manager_kind.go` — `ManagerKindGoal`, `ManagerKindTopic`, `FileExists(path string) bool`.
- `pkg/spawn_cwd.go` — `Vault{Name, Path, ClaudeScript, WorkerLauncher, ManagerLauncher}`, `VaultLister`, `validateLauncher(ctx, value, key, vaultName string) error`, `TaskLauncher(vaultPath, task string) string`, `TaskRef{Title, Status, Vault}`, `ErrNoLauncher` (in `pkg/errors.go`).
- `pkg/role_map.go` — `RefuseAgentTab(ctx, role) error`, `RoleHuman`, `RoleManager`.
- `pkg/ledger.go` — `LedgerReader.Post(ctx, spawnerSessionID string, row LedgerRow) error`; `LedgerRow` fields `SessionID`, `AgentID`, `Label`, `Cwd`, `Mode`, `Launcher`, `PaneID`, `Role`, `ModeSource`, `ResumedFrom`, `Location`, `SpawnedAt`.
- `pkg/flagged_rows_source.go` — `CommandRunner` and `ExecCommandRunner` (stdout only; returns `nil, err` on a non-zero exit, which loses the script's own refusal line — that is why this prompt adds a separate `ScriptRunner`).
- The supervisor's own recipe, ported here (read-only reference, NOT in this repo; the argv and the `unset` list are quoted below so you do not need it): `claude-supervisor` `commands/open.md` Step 2B item 4 / 2C item 4 spawn the manager with `CLAUDE_CODE_ENTRYPOINT=cli` and `unset CLAUDE_CODE_MESSAGING_SOCKET CLAUDE_CODE_MESSAGING_TOKEN CLAUDE_CODE_SESSION_ID CLAUDE_CODE_CHILD_SESSION` — `wezterm cli spawn` forwards the calling process's environment, so without the `unset` the manager comes up named after its spawner, and without the entrypoint a tab registers as `sdk-cli`.
- `manager-launcher.py` contract: `python3 manager-launcher.py <launcher> <page> --default-source manager-default` prints `<launcher>\t<source>` on success; exits 1 with the reason on STDERR, 2 on a usage error.
- `topic-spawn.py` contract: `python3 topic-spawn.py --vault <root> --topic <topic> --topic-open --hand-run` prints `open-ok: <topic>` and exits 0 to admit; otherwise exits 1 printing the token on line 1 and the detail on the following line(s) of STDOUT.
- Test fixtures (package `pkg`): `fakeTabClient` (`spawned`, `texts`, `onSpawn`, `spawnErr`; `Text` renders the last text then a fresh composer), `fakeRegistry{names, live, err}`, `fakeVaults`, `fakeLedger{posts}`, `steppingClock()` in `pkg/tab_spawn_test.go` / `pkg/spawn_cwd_test.go`; `fakeSpawnCaps{workerErr, managerErr, startErr, workerCalls, managerCalls, startCalls}` in `pkg/spawn_limits_test.go`.
- `.golangci.yml` — `funlen` 80/50, `gocognit` 20, `nestif` 4, `dupl` (non-test code), file length 2000.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`

Error library is `github.com/bborbe/errors` v1.7.0: `errors.Wrap(ctx, err, msg)`, `errors.Wrapf(ctx, err, format, args...)`, `errors.Errorf(ctx, format, args...)`. Clock is `libtime "github.com/bborbe/time"` (`libtime.CurrentDateTimeGetter`).
</context>

<requirements>
1. **Tab resume and pane hygiene in `pkg/tab.go`.** Add two fields to `TabSpawnOptions`, after `ExitRecord`:

   ```go
   	// Resume is the full session id the launcher continues with `--resume`, or
   	// empty for a fresh conversation. Only the open endpoint sets it.
   	Resume string
   	// ResetSessionEnv clears the spawning process's session identity in the
   	// pane and marks the session a CLI one, as claude-supervisor's own spawn
   	// recipe does: `wezterm cli spawn` forwards the caller's environment, so
   	// without the unset the session registers under its spawner's name, and
   	// without CLAUDE_CODE_ENTRYPOINT=cli it registers as sdk-cli. Only the open
   	// endpoint sets it; existing tab spawns are unchanged.
   	ResetSessionEnv bool
   ```

   Change `BuildTabInner`:
   - When `ResetSessionEnv` is true, the string STARTS with `unset CLAUDE_CODE_MESSAGING_SOCKET CLAUDE_CODE_MESSAGING_TOKEN CLAUDE_CODE_SESSION_ID CLAUDE_CODE_CHILD_SESSION; export CLAUDE_CODE_ENTRYPOINT='cli'; ` — ahead of the `Env` exports and the `cd`, so the pane's shell drops the spawner's identity before anything else runs (the doc comment's first rule still holds: exports precede the `cd`). Keep the variable names in an unexported `inheritedSessionVars` slice.
   - When `Resume` is non-empty, render ` --resume <shellQuote(Resume)>` between the launcher and ` -n`, i.e. `'<launcher>' --resume '<id>' -n '<name>' '<prompt>'`.
   - When `Prompt` is empty, omit the trailing prompt word (`'<launcher>' -n '<name>'; }`). Every existing caller passes a non-empty prompt (the tab spawner refuses an empty one), so existing output is byte-identical.
   - Update the doc comment with one line per new behaviour.

2. **Ready-gated send with an explicit wait, in `pkg/tab.go`.** Refactor `SendWhenReadyWith` into an unexported `sendWhenReady(ctx, tabs, paneID, text, now, composerTimeout, ready, confirmed)` that takes the composer wait as a parameter; `SendWhenReadyWith` calls it with `composerDeadline` (behaviour unchanged). Add:

   ```go
   // SendWhenReadyWithin is SendWhenReady with the composer wait given by the
   // caller: a manager's start command waits up to five minutes for the
   // previous command's planning turn to finish. The readiness and confirmation
   // rules are SendWhenReady's own (ComposerReady, SubmissionConfirmed).
   func SendWhenReadyWithin(
   	ctx context.Context,
   	tabs TabClient,
   	paneID string,
   	text string,
   	now libtime.CurrentDateTimeGetter,
   	wait time.Duration,
   ) error
   ```

3. **`ScriptRunner` in a new file `pkg/script_runner.go`.** The launcher, ceiling and (prompt 3) answer-path scripts must report their own refusal text and exit code, which `CommandRunner` discards.

   ```go
   // ScriptResult is what one external script run produced.
   type ScriptResult struct {
   	Stdout   string
   	Stderr   string
   	ExitCode int
   }

   // ScriptRunner runs one external script as an argv, never through a shell.
   //
   // A non-zero exit is a RESULT, not an error: the scripts this daemon calls
   // refuse by exiting 1 with their reason, and that reason is the message the
   // caller must pass through. The error is reserved for "the script could not
   // run or did not finish" — an exec failure or the timeout.
   type ScriptRunner interface {
   	RunScript(ctx context.Context, name string, args ...string) (ScriptResult, error)
   }

   // defaultScriptTimeout bounds one script run. None of the three scripts the
   // open endpoint calls reads more than a few vault pages.
   const defaultScriptTimeout = 30 * time.Second

   // ExecScriptRunner runs scripts with os/exec under a timeout.
   type ExecScriptRunner struct {
   	// Timeout defaults to defaultScriptTimeout when zero.
   	Timeout time.Duration
   }
   ```

   `RunScript`: derive `runCtx, cancel := context.WithTimeout(ctx, timeout)`; `exec.CommandContext(runCtx, name, args...)` with `Stdout` / `Stderr` into `bytes.Buffer`s; `cmd.Run()`. Then: `runCtx.Err() != nil` → return the result so far and `errors.Wrapf(ctx, runCtx.Err(), "%s did not finish within %s", name, timeout)`; an `*exec.ExitError` (use `errors.As` from stdlib `errors` or `github.com/bborbe/errors`'s `As`) → `ExitCode: exitErr.ExitCode()`, nil error; any other error → `errors.Wrapf(ctx, err, "run %s failed", name)`. Add a `#nosec G204` comment like `ExecCommandRunner.Run`: the name and script path are daemon flags, and every caller-supplied value is one argv element.

4. **Shared guards in a new file `pkg/spawn_guards.go`** (extractions, behaviour-preserving):

   a. Move the body of `RosterToolServer.enforceCaps` into

   ```go
   // enforceSpawnCaps runs the worker cap, the manager cap for a manager, then
   // reserves a start slot — the order and the reasons are enforceCaps' own.
   // manager is nil for a non-manager spawn; for a manager it returns the
   // manager kind and the vault roots the live count classifies against, and is
   // called only after the worker cap passed (one vault read, as before).
   func enforceSpawnCaps(
   	ctx context.Context,
   	caps SpawnCaps,
   	mode string,
   	manager func(ctx context.Context) (kind string, roots []string),
   	operatorNamed bool,
   ) error
   ```

   Semantics exactly as today: `caps == nil` → nil; when `manager != nil` the worker cap is asked with mode `""`; then `CheckManager(ctx, kind, roots, operatorNamed)`; then `ReserveStart(ctx)`; every refusal is returned as-is (never wrapped — callers pass the cap's own message through). `enforceCaps` becomes: build `manager` only when `strings.TrimSpace(body.Role) == RoleManager` as `func(ctx context.Context) (string, []string) { roots := s.vaultRoots(ctx); return ManagerKindForLabel(body.Label, roots, FileExists), roots }`, then `return enforceSpawnCaps(ctx, s.Caps, mode, manager, body.OperatorNamed)`. Keep `enforceCaps`'s doc comment.

   b. Move the status test and the `ErrTaskTerminal` refusal at the tail of `refuseTerminalResume` into

   ```go
   // refuseTerminalTask refuses a resume of session whose task ref is finished.
   // nil ref, or a status terminalTaskStatus does not call terminal, is allowed.
   func refuseTerminalTask(ctx context.Context, session string, ref *TaskRef) error
   ```

   returning exactly the `errors.Wrapf(ctx, ErrTaskTerminal, "resume of session %s refused: …", …)` text it returns today. `refuseTerminalResume` keeps its lookup and its unresolved-id log line, then returns `refuseTerminalTask(ctx, session, ref)`.

   c. In `pkg/tab_spawn.go`, extract the loop of `awaitRegistration` into `awaitTabRegistration(ctx context.Context, registry SessionRegistry, now libtime.CurrentDateTimeGetter, sleep func(time.Duration), tabName string, preexisting map[string]struct{}) string`; the method delegates. Behaviour unchanged.

   All existing tests in `pkg/spawn_limits_test.go`, `pkg/roster_resume_test.go`, `pkg/terminal_task_test.go` and `pkg/tab_spawn_test.go` must pass unmodified.

5. **The opener, in a new file `pkg/open_tab.go`.**

   ```go
   // OpenModeSource is the mode_source an open-endpoint spawn files in the
   // spawn ledger, so a worker can be traced to the open that made it.
   const OpenModeSource = "open"

   // OpenTabRequest is one human or manager tab the open executor asks for.
   // The window is already resolved (ResolveOpenWindow); this type never reads
   // the role map.
   type OpenTabRequest struct {
   	// Kind is task, goal or topic; Role is human or manager.
   	Kind string
   	Role string
   	// Subject is the page title; PagePath its absolute path.
   	Subject  string
   	PagePath string
   	Vault    Vault
   	WindowID int
   	Chip     string
   	// Resume is the recorded session id; empty on create.
   	Resume string
   	// FirstStep is typed in front of a human row's work command.
   	FirstStep string
   	// TaskStatus is the task page's `status:`, for the terminal-task guard.
   	TaskStatus       string
   	OperatorNamed    bool
   	SpawnerSessionID string
   }

   // OpenTabResult is what an opened tab produced.
   type OpenTabResult struct {
   	PaneID    string
   	SessionID string
   	TabName   string
   	Launcher  string
   	// Note reports a start command that was not sent; empty when every one landed.
   	Note string
   }

   // OpenTabOpener opens a human row's Direct tab or a manager's Managers tab.
   type OpenTabOpener interface {
   	Open(ctx context.Context, request OpenTabRequest) (*OpenTabResult, error)
   }

   // NewOpenTabOpener wires the tab half of the open endpoint.
   func NewOpenTabOpener(
   	tabs TabClient,
   	registry SessionRegistry,
   	vaults VaultLister,
   	caps SpawnCaps,
   	ledger LedgerReader,
   	scripts ScriptRunner,
   	now libtime.CurrentDateTimeGetter,
   	managerLauncherScript string,
   	topicSpawnScript string,
   	exitRecord string,
   ) OpenTabOpener
   ```

   The unexported `openTabOpener` struct holds those plus `sleep func(time.Duration)` (default `time.Sleep`) for the registration poll. Nil `caps` or nil `ledger` skips that step (unit tests only; the daemon always wires both). The interpreter is the constant `openPython = "python3"`.

   `Open` runs in this order — every refusal before the first side effect (the pane):

   1. `RefuseAgentTab(ctx, request.Role)` (defence in depth); a role other than human/manager → `errors.Errorf(ctx, "open tab: role %q has no tab", role)`. `request.Vault.Path` empty → `errors.Errorf`.
   2. **Terminal-task guard:** `Kind == OpenKindTask && Resume != ""` → `refuseTerminalTask(ctx, request.Resume, &TaskRef{Title: request.Subject, Status: request.TaskStatus, Vault: request.Vault.Name})`.
   3. **Launcher.**
      - human → `humanLauncher(ctx, &request.Vault, request.Subject) (launcher, source string, err error)`, matrix Rule 4: the task's own `TaskLauncher(vault.Path, subject)`; else the `-claude` sibling of `ClaudeScript` when it exists; else `ManagerLauncher`; else `ClaudeScript`; none → `errors.Wrapf(ctx, ErrNoLauncher, "vault %q has no launcher for a human row", name)`. Every candidate goes through `validateLauncher` (key `launcher`, `claude_script` or `manager_launcher`). The sibling is a port of `manager-launcher.py`'s `sibling()`: `dir, base := filepath.Split(cs)`; `base = strings.TrimSuffix(base, "-deepseek")`; if `strings.HasSuffix(base, "-claude")` return `cs` unchanged, else `filepath.Join(dir, base+"-claude")`. "Exists" is `FileExists` for a value containing `/`, `exec.LookPath` otherwise.
      - manager → `managerLauncher(ctx, request)`: `Vault.ManagerLauncher` empty → `errors.Errorf(ctx, "vault %q has no manager_launcher in vault-cli config, so no manager launcher can be resolved", name)`. Run `scripts.RunScript(ctx, openPython, o.managerLauncherScript, request.Vault.ManagerLauncher, request.PagePath, "--default-source", "manager-default")`. Run error → `errors.Wrap(ctx, err, "manager-launcher.py could not run")`. Non-zero exit → `errors.Errorf(ctx, "%s", text)` where `text` is the trimmed stderr, else the trimmed stdout, else `manager-launcher.py exited <code>` — passed through unchanged (AC19). Exit 0 → the first stdout line split on `\t`; field 0 trimmed is the launcher; empty → `errors.Errorf`; then `validateLauncher(ctx, launcher, "manager_launcher", vault.Name)`.
   4. **Topic ceiling (topic `create` only, `Resume == ""`):** `scripts.RunScript(ctx, openPython, o.topicSpawnScript, "--vault", request.Vault.Path, "--topic", request.Subject, "--topic-open", "--hand-run")`. Run error → wrapped error. Collect the trimmed non-empty stdout lines. Exit 0 and line 1 equals `open-ok: <subject>` → admitted. Anything else → `errors.Errorf(ctx, "%s", msg)` where `msg` is line 2 when present, else line 1, else trimmed stderr, else `topic-spawn.py exited <code>` (`docs/open-endpoint.md`: "spawn-refused with its second line").
   5. **Caps:** human → `enforceSpawnCaps(ctx, o.caps, SpawnModeInteractive, nil, request.OperatorNamed)`; manager → `enforceSpawnCaps(ctx, o.caps, SpawnModeInteractive, func(ctx context.Context) (string, []string) { return kind, o.vaultRoots(ctx) }, request.OperatorNamed)` with `kind` `ManagerKindGoal` or `ManagerKindTopic` from `request.Kind` (the subject's kind is known, so `ManagerKindForLabel` is not needed). `vaultRoots` mirrors `RosterToolServer.vaultRoots` (nil lister or list error → nil roots). The refusal is returned unchanged.
   6. **Tab name:** human → `request.Subject`; manager → `request.Subject + " Manager"` (the name `DecideOpen` matches on later opens). On `create`, snapshot `preexisting := PreexistingHolders(ctx, o.registry, tabName)` now, before the pane exists.
   7. **Spawn:** `paneID, err := o.tabs.Spawn(ctx, TabSpawnOptions{Cwd: request.Vault.Path, Launcher: launcher, TabName: tabName, Prompt: colour, WindowID: &windowID, ExitRecord: o.exitRecord, Resume: request.Resume, ResetSessionEnv: true})` where `colour` is `"/color " + request.Chip` when `Chip != ""`, else `""`. Error → return it. Then `requirePaneID(ctx, paneID)`.
   8. From here the tab is OPEN: everything below runs on `tailCtx := context.WithoutCancel(ctx)` (a disconnecting client must not orphan an unrecorded pane — the same rule `tabSpawner.Spawn` follows) and never returns an error.
   9. **Identity:** `resume` → `SessionID = request.Resume`; `create` → `awaitTabRegistration(tailCtx, o.registry, o.now, o.sleep, tabName, preexisting)` (may be `""`).
   10. **Ledger:** when `SessionID != ""` and the ledger is wired, `Post(tailCtx, request.SpawnerSessionID, LedgerRow{SessionID, AgentID: SessionID, Label: tabName, Cwd: request.Vault.Path, Mode: SpawnModeInteractive, ModeSource: OpenModeSource, Launcher: launcher, PaneID: paneID, Role: request.Role, ResumedFrom: request.Resume, Location: "local", SpawnedAt: <now RFC3339>})`; a failure is a `log.Printf("WARNING: …")`, never returned.
   11. **Start commands**, each via `SendWhenReadyWithin(tailCtx, o.tabs, paneID, text, o.now, wait)`, in order, on both `create` and `resume` (a resumed manager with no turn is idle and its loop must re-arm):

       | Kind | Commands, in order | Wait before each |
       |---|---|---|
       | task (human) | `<first_step>\n\n/vault-cli:work-on-task "<task>"`, or the bare command without a first step | 60 s |
       | goal | `/vault-cli:work-on-goal "<goal>"`, then `/supervisor:goal-loop "<goal>"` | 60 s, then 300 s |
       | topic | `/supervisor:goal-loop "<topic>"` | 60 s |

       Name the waits as constants (`openStartWait = 60 * time.Second`, `openGoalLoopWait = 300 * time.Second`). A failed send sets `Note = fmt.Sprintf("start command %q not sent: %v", text, err)`, logs one line, and sends no later command. No retry beyond what `SendWhenReadyWithin` itself does. A start-command failure is NOT an error: the tab is open and the executor reports `create`/`resume` with the note.
   12. Return `&OpenTabResult{PaneID, SessionID, TabName, Launcher, Note}`.

   Keep each function inside the lint budgets (split launcher resolution, ceiling, sends into helpers). Every subject, path and launcher reaches the scripts as ONE argv element, never through a shell.

6. **Tests** — new files `pkg/open_tab_test.go`, `pkg/script_runner_test.go`, and additions to `pkg/tab_test.go` (package `pkg`, Ginkgo/Gomega with prefixes). Add a `fakeScriptRunner` in `pkg/open_tab_test.go` that records each argv and answers from a map keyed by the script path (argv[1]) with a `ScriptResult` and an error.

   a. **BuildTabInner:** with `ResetSessionEnv: true` the string has the prefix `unset CLAUDE_CODE_MESSAGING_SOCKET CLAUDE_CODE_MESSAGING_TOKEN CLAUDE_CODE_SESSION_ID CLAUDE_CODE_CHILD_SESSION; export CLAUDE_CODE_ENTRYPOINT='cli'; ` and the unset precedes any `Env` export; with `Resume: "1b6a…"` (a full UUID) it contains `'/scripts/cc-x' --resume '<uuid>' -n '⚙ t' 'do it'`; with an empty `Prompt` it ends `-n '⚙ t'; }`. Every existing `BuildTabInner` spec passes unmodified.

   b. **SendWhenReadyWithin:** with `fakeTabClient` it sends the text once; with a tab client whose `Text` never shows an empty composer and a clock advancing 30 s per read, a 60 s wait returns an error and `SendText` was never called.

   c. **ExecScriptRunner:** `RunScript(ctx, "sh", "-c", "echo out; echo err >&2; exit 3")` → `ExitCode 3`, `Stdout "out\n"`, `Stderr "err\n"`, nil error; a missing binary → error; `ExecScriptRunner{Timeout: 100 * time.Millisecond}` on `sh -c "sleep 5"` → error containing `did not finish`.

   d. **AC7 — window, resume id and sent texts in order** (construct `&openTabOpener{...}` with `fakeTabClient`, `fakeRegistry`, `fakeLedger`, a `fakeScriptRunner` answering manager-launcher with `ScriptResult{Stdout: "/scripts/cc-mgr\tmanager-default\n"}` and topic-spawn with `ScriptResult{Stdout: "open-ok: T\n"}`, `steppingClock()`, `sleep: func(time.Duration) {}`; `onSpawn` adds the tab name to `fakeRegistry.names` so `create` resolves an id):
      - goal `create`, window 14, chip `orange`: one `TabSpawnOptions` with `*WindowID == 14`, `TabName "G Manager"`, `Prompt "/color orange"`, `Launcher "/scripts/cc-mgr"`, `Resume ""`, `ResetSessionEnv true`; `tabs.texts` equals exactly `["/vault-cli:work-on-goal \"G\"", "/supervisor:goal-loop \"G\""]`; the manager-launcher argv is `python3 <script> <vault ManagerLauncher> <PagePath> --default-source manager-default`.
      - goal `resume` with a UUID: `Resume` equals it, the same two texts, `result.SessionID` equals it.
      - topic `create`: the ceiling argv is `python3 <script> --vault <path> --topic T --topic-open --hand-run`; texts equal `["/supervisor:goal-loop \"T\""]`; topic `resume` does NOT run the ceiling.
      - human `create` in vault `private-personal`, window 12, chip `cyan`, `ClaudeScript` pointing at a temp file `cc-private` with a sibling temp file `cc-private-claude`: `*WindowID == 12`, `TabName "Task"`, `Prompt "/color cyan"`, `Launcher` the `-claude` sibling; texts equal `["/vault-cli:work-on-task \"Task\""]`; with `FirstStep: "X"` the text is `"X\n\n/vault-cli:work-on-task \"Task\""`.
      - human `resume`: `Resume` set, same work command.
      - one ledger row per open with `ModeSource "open"`, `Mode "interactive"`, `PaneID "pane-1"`, and `ResumedFrom` equal to the resume id on resume.

   e. **Human launcher chain:** task `launcher:` wins; no sibling file → `ManagerLauncher`; neither → `ClaudeScript`; `ClaudeScript` already `…-claude` → itself; an unsafe value (`cc x;rm`) → `ErrNoLauncher`.

   f. **AC19 at the opener (refusals before the pane, message unchanged):**
      - manager-launcher exit 1 with `Stderr: "unsafe launcher: x\n"` → `err.Error() == "unsafe launcher: x"`, `tabs.spawned` empty, caps not consulted (`fakeSpawnCaps.workerCalls` empty);
      - caps `workerErr: stderrors.New("the worker fleet is FULL")` → `err.Error() == "the worker fleet is FULL"`, `tabs.spawned` empty;
      - a manager row checks `CheckManager` once with the goal kind (assert `managerCalls` length 1);
      - human `resume` with `TaskStatus: "completed"` → `errors.Is(err, ErrTaskTerminal)`, `tabs.spawned` empty;
      - topic ceiling exit 1 with stdout `topic-unflagged\n<T> is not flagged.\n` → `err.Error() == "<T> is not flagged."`, `tabs.spawned` empty.

   g. **Best-effort start command:** goal `create` whose pane never shows an empty composer (custom tab fake; clock advancing 30 s per read): no error, `result.Note` contains `not sent`, no text sent, one tab spawned, the ledger row still filed.

   h. **Agent refused:** `Role: "agent"` → `errors.Is(err, ErrAgentTab)`, nothing spawned.

7. **No doc change** in this prompt beyond what the code requires; `docs/open-endpoint.md` already describes this recipe.

8. **Self-check:** before finishing, re-run `<verification>`; confirm every existing spec in `pkg/tab_test.go`, `pkg/tab_spawn_test.go`, `pkg/spawn_limits_test.go`, `pkg/roster_resume_test.go` passes unmodified, and name the `It` covering AC7 and each AC19 case in the completion report.
</requirements>

<constraints>
Copied from the spec:

- Human and manager rows open as tabs only in their mapped window: human rows to `Direct`, goal and topic managers to `Managers`. The window arrives resolved; this code never substitutes another window and never opens in Hold.
- Manager tabs follow `docs/open-endpoint.md` § Manager rows: window `Managers.window_id`, chip `orange`, tab name `<subject> Manager`; goal `create` sends `/vault-cli:work-on-goal "<goal>"` then `/supervisor:goal-loop "<goal>"`, topic `create` sends `/supervisor:goal-loop "<topic>"`, both ready-gated — never a fixed sleep. A send that never reaches a ready composer is not made; the tab stays open; no retry.
- `manager-launcher.py` or `topic-spawn.py` refusing → spawn-refused with the script's own line; no tab. The wrapper does not fall back to a goal's member tasks; that stays with the caller.
- One implementation per external check: the scripts are invoked, never re-derived, from their daemon-flag paths.
- Existing guards apply to every spawn this operation makes, with none bypassed or duplicated: spawn caps, the start-rate limiter, the terminal-task resume guard, spawn-ledger filing, and the tab client's pane hygiene (exit recorder, entrypoint, inherited-session unset).
- `name` reaches `manager-launcher.py` and `topic-spawn.py` as one argv element, never through a shell.
- Probe, scripts and spawns stay bounded. Each runs under its timeout, and the operation adds no retry loop.
- Existing routes are unchanged, and so are `spawn_agent`, `spawn_session` and the flagged reopen tick. The tab resume added here is reachable only from the open operation.
- Do NOT add a fallback window, an "open interactive anyway" flag for agent rows, or a probe-skip flag.

General:

- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- Anchor edits by identifier names, never line numbers. Keep `pkg/roster_mcp.go` under 2000 lines (the extractions move code out of it).
</constraints>

<verification>
Run in the repo root:

```
make precommit
go test ./pkg/ -count=1 -ginkgo.focus='openTabOpener|OpenTab|BuildTabInner|SendWhenReadyWithin|ExecScriptRunner|spawn caps|tabSpawner' -ginkgo.v
wc -l pkg/roster_mcp.go
grep -n 'ResetSessionEnv\|Resume ' pkg/tab.go
```

`make precommit` must exit 0. `wc -l` must print a number below 2000. The focused run must include the new opener specs and the existing `tabSpawner` / spawn-cap specs, all passing.
</verification>
