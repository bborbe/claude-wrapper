---
status: completed
spec: [005-open-endpoint]
summary: Added the pure open decision core (DecideOpen, ResolveOpenWindow, FormatOpenLogLine) with every refusal reason and 72 passing Ginkgo specs, documented the verdict message field, and added an Unreleased changelog entry.
execution_id: 9881bf24-5a56-5157-8303-c8eca57d0834-exec-009-spec-005-open-decision
dark-factory-version: dev
created: "2026-11-18T18:59:25Z"
queued: "2026-11-18T19:12:27Z"
started: "2026-11-18T19:14:19Z"
completed: "2026-11-18T19:22:59Z"
---

# Pure open decision: row facts to one verdict, with window resolution and every refusal

<summary>
- The wrapper gains one pure decision that turns the facts about a row (its page, its recorded session, which sessions are live, the role map) into one verdict, with no side effects.
- A task with no role becomes an agent and is marked as defaulted; an unknown role is refused and named; a task that declares itself a manager is reported as the operator's own and nothing opens.
- Goals and topics are always managers; a goal or topic page that asks to run headless is refused.
- An agent is always headless; a stale "interactive" on an agent is corrected and reported.
- Rows the endpoint does not port (goal loops, non-local targets), a truncated session id, and an unreadable session registry are refused by name before anything else happens.
- The decision picks jump, live, resume or create from the live sessions it is handed; an agent is never jumped to.
- Tab rows resolve their window from the role map: humans to Direct, managers to Managers. A missing or broken map, a missing key or a missing window id refuses and names the key; a window equal to Hold's refuses.
- Every verdict has the same fixed set of fields, and one log-line formatter renders it for the operator.
- The contract page gains the verdict's `message` field.
</summary>

<objective>
Add the side-effect-free core of the open endpoint: `DecideOpen` (row facts → verdict plus the write-back and spawn hints the executor needs), `ResolveOpenWindow` (role map → window or refusal) and `FormatOpenLogLine`, implementing `docs/open-endpoint.md` § Verdict and § Decision order items 1–6 and the window half of item 7. Later prompts add the tab opener (prompt 2), the executor that gathers the facts and performs the open (prompt 3), and the REST/MCP surfaces (prompt 4); this prompt is the correctness surface, so every refusal reason and branch is decided and tested here with no I/O.
</objective>

<context>
Read the repo `README.md` for project conventions (there is no `CLAUDE.md` in this repo).

Read these files before changing anything:

- `docs/open-endpoint.md` — the contract. § Verdict (field table, `reason` tokens, log line), § Decision order (items 1–8). This prompt implements items 1–6 and the window rules of item 7. It is the single home of the tokens; the code must use exactly its spellings.
- `specs/in-progress/005-open-endpoint.md` — Desired Behaviors 2, 4, 6, 7 and ACs 5, 8, 9, 11, 12, 13.
- `pkg/role_map.go` — `RoleManager`, `RoleAgent`, `RoleHuman`, `DefaultRole`, `rolePurposes` (`RoleManager: "Managers"`, `RoleHuman: "Direct"`), `agentTabRefusal` (unexported const), `RoleMapEntry{Chip *string; WindowID *int}`, `RoleResolution`, `ChipForRole(resolution *RoleResolution, vault string) string`, `NewRoleMapReader(path string) RoleMapReader` and its `Read`: an ABSENT file returns an empty map and nil error; an unparseable file returns an error wrapped as `parse role map %q failed`.
- `pkg/spawn_mode.go` — `SpawnModeHeadless = "headless"`, `SpawnModeInteractive = "interactive"`.
- `pkg/supervisor.go` — `uuidPattern` (unexported `regexp.MustCompile(`^[0-9a-fA-F]{8}-…-[0-9a-fA-F]{12}$`)`), the shape `claude --resume` accepts. Reuse it; do not add a second UUID regex.
- `pkg/terminal_task.go` — `readFrontmatter(ctx, path) (map[string]string, error)` returns the page's top-level scalars (quotes stripped). The executor (prompt 3) fills `OpenPage.Fields` with exactly this map, so keys are lowercase frontmatter names (`role`, `mode`, `claude_session_id`, `goal_loop`, `target`, `shipping`, `flag`, `flag_set_by`, `status`).
- `pkg/session_registry.go` — `RegistryEntry{PID int; SessionID string; Status string; Cwd string; Name string}`.
- `pkg/sweepgate/task.go` — `operatorFlagged(fm string) bool`: its `truthy` set (`true`/`True`/`TRUE`, `yes`/`Yes`/`YES`, `on`/`On`/`ON`, `y`/`Y` — the supervisor's strict YAML-boolean spellings) and `(setBy == "operator" || setBy == "legacy")`. This is the operator-named rule `OperatorNamed` must mirror exactly. Do not import sweepgate (it is a separate package with its own frontmatter scalar reader); re-state both sets locally as unexported package vars (`openFlagTruthy`, `openOpeningActors`) with a comment naming `pkg/sweepgate/task.go` `operatorFlagged` and `claude-supervisor` `commands/open.md` § Gate 3 / `OPENING_ACTORS` as the rule's homes.
- `pkg/spawn_cwd_test.go` — `fakeRoleMap{entries, err}` (reuse it in tests).
- Test style: package `pkg`, Ginkgo v2 + Gomega with explicit `ginkgo.` / `gomega.` prefixes (see `pkg/tab_spawn_test.go`).
- `.golangci.yml` — `funlen` (80 lines / 50 statements), `gocognit` (20), `nestif` (4), `revive` file-length 2000. `DecideOpen` must be split into per-step helpers to stay inside these.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md` (for the token constants; plain string constants are fine — the verdict is a JSON wire type)
</context>

<requirements>
1. **Create `pkg/open_decision.go`** (package `pkg`, BSD header like every other file). It does no I/O: no file reads, no exec, no registry or role-map reads. Everything it needs arrives in its arguments.

2. **Token constants**, with exactly the spellings of `docs/open-endpoint.md` § Verdict:

   ```go
   // Open kinds.
   const (
   	OpenKindTask  = "task"
   	OpenKindGoal  = "goal"
   	OpenKindTopic = "topic"
   )

   // Open branches.
   const (
   	OpenBranchJump    = "jump"
   	OpenBranchLive    = "live"
   	OpenBranchResume  = "resume"
   	OpenBranchCreate  = "create"
   	OpenBranchYours   = "yours"
   	OpenBranchRefused = "refused"
   )

   // Open paths.
   const (
   	OpenPathHeadless = "headless"
   	OpenPathTab      = "tab"
   	OpenPathNone     = "none"
   )

   // Where a verdict's role came from.
   const (
   	OpenRoleSourceOnDisk    = "on-disk"
   	OpenRoleSourceDefaulted = "defaulted"
   	OpenRoleSourceKind      = "kind"
   )

   // OpenModeNoteNormalised is the only non-empty mode_note value.
   const OpenModeNoteNormalised = "normalised interactive->headless"

   // Open refusal reasons.
   const (
   	OpenReasonMissingRow          = "missing-row"
   	OpenReasonRowUnreadable       = "row-unreadable"
   	OpenReasonUndecidableRole     = "undecidable-role"
   	OpenReasonManagerModeHeadless = "manager-mode-headless"
   	OpenReasonTruncatedID         = "truncated-id"
   	OpenReasonLivenessUnreadable  = "liveness-unreadable"
   	OpenReasonAnswerPath          = "answer-path"
   	OpenReasonRoleMapKeyMissing   = "role-map-key-missing"
   	OpenReasonHoldWindow          = "hold-window"
   	OpenReasonGoalLoopUnsupported = "goal-loop-unsupported"
   	OpenReasonTargetUnsupported   = "target-unsupported"
   	OpenReasonInFlight            = "in-flight"
   	OpenReasonSpawnRefused        = "spawn-refused"
   )

   // OpenWindowKeyHold is the role-map key no open may ever land in.
   const OpenWindowKeyHold = "Hold"

   // OpenAnswerPathRemedy is the role-aware remedy of role-mode matrix
   // § Answer-path remedy for an agent row. It never says "open interactive":
   // that is the tab Rule 2 forbids an agent.
   const OpenAnswerPathRemedy = "arm this session's manager loop and re-run"
   ```

   The `Direct` / `Managers` keys are NOT re-declared: they come from `rolePurposes[RoleHuman]` and `rolePurposes[RoleManager]`, so the role table stays the single home of which key a role uses.

3. **Remedies.** Add an unexported `openRemedies map[string]string` keyed by reason, used by every refusal. Required texts (keep them short, imperative, and never containing the word `interactive` for `answer-path`):
   - `missing-row`: `check the page title, kind and vault, then re-run`
   - `row-unreadable`: `fix the page or its vault, then re-run`
   - `undecidable-role`: `set role to agent, human or manager on the task, then re-run`
   - `manager-mode-headless`: `remove mode: headless from the page — a manager opens as a tab — then re-run`
   - `truncated-id`: `write the full 36-character claude_session_id on the page, or clear it, then re-run`
   - `liveness-unreadable`: `re-run once the session registry is readable`
   - `answer-path`: `OpenAnswerPathRemedy`
   - `role-map-key-missing`: `add the key with a window_id to the WezTerm role map, then re-run`
   - `hold-window`: `point the key at its own window in the WezTerm role map, then re-run`
   - `goal-loop-unsupported`: `the open endpoint does not port goal_loop rows; open this row by hand`
   - `target-unsupported`: `the open endpoint opens local rows only; spawn this row on its target with spawn_agent`
   - `in-flight`: `wait for the other open of this row to finish, then re-run`
   - `spawn-refused`: `read the message, clear what refused (a slot, the launcher or the task state), then re-run`

4. **Types.** Add (field order and JSON tags exactly as shown; no `omitempty` anywhere on `OpenVerdict` — AC5 requires every field to be present on every verdict):

   ```go
   // OpenRow is one row of an open request.
   type OpenRow struct {
   	Kind      string `json:"kind"`
   	Vault     string `json:"vault"`
   	Name      string `json:"name"`
   	FirstStep string `json:"first_step"`
   }

   // OpenVerdict is the one answer an open returns per row.
   //
   // Field set and token values: docs/open-endpoint.md § Verdict. An empty
   // string means "not decided for this row" (a row refused at the page read
   // has no role yet); window_id is null unless the row opened, or will open,
   // a tab.
   type OpenVerdict struct {
   	Kind       string `json:"kind"`
   	Vault      string `json:"vault"`
   	Name       string `json:"name"`
   	Role       string `json:"role"`
   	RoleSource string `json:"role_source"`
   	Mode       string `json:"mode"`
   	ModeNote   string `json:"mode_note"`
   	Branch     string `json:"branch"`
   	Path       string `json:"path"`
   	WindowKey  string `json:"window_key"`
   	WindowID   *int   `json:"window_id"`
   	SessionID  string `json:"session_id"`
   	PaneID     string `json:"pane_id"`
   	Reason     string `json:"reason"`
   	Remedy     string `json:"remedy"`
   	Message    string `json:"message"`
   }

   // OpenRefusal is one named refusal: its reason token, the detail, the remedy.
   type OpenRefusal struct {
   	Reason  string
   	Message string
   	Remedy  string
   }

   // OpenPage is what the executor read for one row's page.
   type OpenPage struct {
   	// Found is false when no page of the row's kind exists in the vault.
   	Found bool
   	// ReadErr is set when a page exists but could not be read.
   	ReadErr error
   	// Path is the page's absolute path; set when Found.
   	Path string
   	// Fields are the page's top-level frontmatter scalars (readFrontmatter).
   	Fields map[string]string
   }

   // OpenLiveEntry is one live session-registry entry.
   type OpenLiveEntry struct {
   	PID       int
   	SessionID string
   	Name      string
   }

   // OpenRowFacts is everything DecideOpen reads for one row.
   type OpenRowFacts struct {
   	Row  OpenRow
   	Page OpenPage
   	// Live is the registry's live set; LiveErr is set when it could not be read.
   	Live    []OpenLiveEntry
   	LiveErr error
   	// PaneFor returns the WezTerm pane a live pid is attached to, or "" for
   	// none. It is consulted only for a human or manager row that matched a live
   	// entry; nil means no pane is known for any pid.
   	PaneFor func(pid int) string
   }

   // OpenDecision is DecideOpen's answer: the verdict so far plus what the
   // executor needs to finish the open.
   type OpenDecision struct {
   	Verdict OpenVerdict
   	// Resume is the recorded session id a resume continues; empty otherwise.
   	Resume string
   	// Shipping reports a task's `shipping: true`.
   	Shipping bool
   	// OperatorNamed reports `flag: true` set by an opening actor.
   	OperatorNamed bool
   	// WriteRole and WriteMode are the task values to write back with
   	// `vault-cli task set`; empty means no write.
   	WriteRole string
   	WriteMode string
   }

   // OpenWindow is the WezTerm window a tab row resolved to.
   type OpenWindow struct {
   	Key  string
   	ID   int
   	Chip string
   }
   ```

   Add an unexported method `func (v *OpenVerdict) refuse(refusal OpenRefusal)` that sets `Branch = OpenBranchRefused`, `Reason`, `Message`, `Remedy` (from the refusal; when `refusal.Remedy == ""` use `openRemedies[refusal.Reason]`), and sets `WindowID = nil`. It leaves `Path`, `Role`, `Mode` and `WindowKey` as they were, so a refusal decided after the path is known still reports it (an agent refused `answer-path` logs `path=headless`).

5. **`func DecideOpen(facts OpenRowFacts) OpenDecision`.** First match wins, in exactly this order (§ Decision order). Start from a verdict carrying `Kind`, `Vault`, `Name` echoed from `facts.Row` and `Path = OpenPathNone`. Split the steps into unexported helpers (e.g. `decideOpenRole`, `decideOpenMode`, `unportedOpenFields`, `decideOpenBranch`) so no function exceeds the lint budgets. The path choice MUST be its own function, `openPathFor(role, branch string) string` (step 6) — prompt 3's AC17 mutation run targets it by that name.

   **Step 1 — page.**
   - `facts.Row.Kind` not one of the three kinds → refuse `missing-row`, message `unknown kind "<kind>"`.
   - `!facts.Page.Found` and `facts.Page.ReadErr == nil` → refuse `missing-row`, message `no <kind> page named "<name>" in vault "<vault>"`.
   - `facts.Page.ReadErr != nil` → refuse `row-unreadable`, message `read <kind> page "<name>" failed: <ReadErr.Error()>`.

   **Step 2 — role.** Read `strings.TrimSpace(fields["role"])` for task rows only.
   - task, empty → `Role = RoleAgent`, `RoleSource = OpenRoleSourceDefaulted`, `WriteRole = RoleAgent`.
   - task, `agent` or `human` → that role, `RoleSource = OpenRoleSourceOnDisk`.
   - task, `manager` → `Role = RoleManager`, `RoleSource = OpenRoleSourceOnDisk`, `Branch = OpenBranchYours`, `Path = OpenPathNone`; return immediately (nothing else is decided, nothing is written).
   - task, any other value → refuse `undecidable-role`, message `task role "<value>" is not agent, human or manager`. Exact comparison after trim; no case folding (the matrix values are lowercase).
   - goal or topic → `Role = RoleManager`, `RoleSource = OpenRoleSourceKind` (the page's own `role:` is ignored).

   **Step 3 — mode by Rule 2.** Read `strings.TrimSpace(fields["mode"])`.
   - agent → `Mode = SpawnModeHeadless`. Stored `interactive` → `ModeNote = OpenModeNoteNormalised`, `WriteMode = SpawnModeHeadless`. Stored empty → `WriteMode = SpawnModeHeadless` (absent). Stored `headless` → no write. Any other stored value → `WriteMode = SpawnModeHeadless`, `ModeNote` stays empty (the note token names the interactive case only).
   - human (task) → `Mode = SpawnModeInteractive`. Stored empty → `WriteMode = SpawnModeInteractive`. Any stored non-empty value → no write (human honours the disk per `commands/open.md` Step 0.6).
   - goal / topic → `Mode = SpawnModeInteractive`; stored `headless` → refuse `manager-mode-headless`, message `<kind> page "<name>" declares mode: headless, but a manager opens as a tab (role-mode matrix Rule 2)`. Goal and topic rows never set `WriteRole` / `WriteMode`.

   **Step 4 — unported fields (task rows only).**
   - `strings.TrimSpace(fields["goal_loop"]) == "true"` → refuse `goal-loop-unsupported`, message `task "<name>" carries goal_loop: true, which the open endpoint does not port`.
   - `target := strings.TrimSpace(fields["target"])`; `target != "" && target != "local"` → refuse `target-unsupported`, message `task "<name>" targets "<target>"; the open endpoint opens local rows only`.
   - Then set `Shipping = strings.TrimSpace(fields["shipping"]) == "true"` (task rows only) and `OperatorNamed = openFlagTruthy[strings.TrimSpace(fields["flag"])] && openOpeningActors[strings.Trim(strings.TrimSpace(fields["flag_set_by"]), `'"`)]` — the same spellings `operatorFlagged` accepts, nothing wider (every kind — a goal/topic manager's cap check reads it too).

   A refusal at steps 4–6 clears `WriteRole` and `WriteMode` in the returned decision (a refused row is never written; spec DB7).

   **Step 5 — recorded id and registry.** `recorded := strings.TrimSpace(fields["claude_session_id"])`.
   - `recorded != "" && !uuidPattern.MatchString(recorded)` → refuse `truncated-id`, message `recorded claude_session_id "<recorded>" is not a full 36-character UUID`.
   - `facts.LiveErr != nil` → refuse `liveness-unreadable`, message `session registry unreadable: <LiveErr.Error()>`.

   **Step 6 — branch.**
   - `recorded != ""`: find the live entry with `SessionID == recorded`.
     - found, role is human or manager, and `facts.PaneFor != nil` and `pane := facts.PaneFor(entry.PID); pane != ""` → `Branch = OpenBranchJump`, `SessionID = recorded`, `PaneID = pane`.
     - found otherwise (any agent row, or no pane) → `Branch = OpenBranchLive`, `SessionID = recorded`. `PaneFor` is NOT called for an agent row.
     - not found → `Branch = OpenBranchResume`, `SessionID = recorded`, `Resume = recorded`.
   - `recorded == ""`, goal or topic: find a live entry whose `strings.TrimSpace(Name)` equals `<name> Manager` under `strings.EqualFold`. Found with a pane → `jump` (`SessionID` = that entry's id, `PaneID` = pane); found without a pane → `live` (`SessionID` set). Not found → `create`.
   - `recorded == ""`, task → `Branch = OpenBranchCreate`.
   - `Path` is set by `func openPathFor(role, branch string) string`, a separate unexported function and the ONLY place the path is chosen: `resume` / `create` → `OpenPathHeadless` for `RoleAgent`, `OpenPathTab` for `RoleHuman` or `RoleManager`; every other branch → `OpenPathNone`. It takes the resolved role and the branch, never the stored `mode` (Rule 2: routing follows role).

   The window rules of § Decision order item 7 are NOT applied inside `DecideOpen`: the executor reads the role map only for tab rows (AC6 asserts the reader is never consulted for an agent row), so window resolution is the separate function below.

6. **`func ResolveOpenWindow(role string, vault string, entries map[string]RoleMapEntry, readErr error) (OpenWindow, *OpenRefusal)`** — the window half of item 7, pure.
   - `key, ok := rolePurposes[role]`. `!ok` (an agent, or anything else) → return a refusal `spawn-refused` whose message is `agentTabRefusal` (an agent never reaches this function from the executor; this is defence in depth).
   - `readErr != nil` → refusal `role-map-key-missing`, message `role map unreadable, so key "<key>" cannot be resolved: <readErr.Error()>` (AC8: the parse error text is in the message).
   - `entry, ok := entries[key]`; `!ok` → `role-map-key-missing`, message `role map has no "<key>" key`. An absent file reaches this branch, because `NewRoleMapReader` returns an empty map for it.
   - `entry.WindowID == nil` → `role-map-key-missing`, message `role map key "<key>" has no window_id`.
   - `hold, ok := entries[OpenWindowKeyHold]`; `ok && hold.WindowID != nil && *hold.WindowID == *entry.WindowID` → `hold-window`, message `role map key "<key>" names window <id>, which is Hold.window_id — nothing opens in Hold`. A map without a `Hold` key is not a refusal.
   - Otherwise return `OpenWindow{Key: key, ID: *entry.WindowID, Chip: chip}` where `chip` is: for a manager, ALWAYS `"orange"` (spec AC7 and `docs/open-endpoint.md` § Manager rows fix the manager chip; the `Managers` entry's own chip is not read); for a human, `ChipForRole(&RoleResolution{Role: RoleHuman, Purpose: key, Chip: entryChip}, vault)` with `entryChip` = `*entry.Chip` when non-nil (the vault family's chip, falling back to the Direct entry's).
   - Never substitute another key or window (spec Non-goals: no fallback window).

7. **`func FormatOpenLogLine(verdict OpenVerdict) string`** — exactly the § Verdict log format:

   ```
   open: row=<vault>/<kind>/<name> role=<r> mode=<m> branch=<b> path=<p> window=<id|none> reason=<reason|->
   ```

   Empty `Role`, `Mode`, `Branch` or `Path` render as `-`; a nil `WindowID` renders `none`; an empty `Reason` renders `-`. One line, no trailing newline.

8. **Docs** — in `docs/open-endpoint.md` § Verdict, add one table row after `reason`, `remedy`:

   `| \`message\` | free text: the refusal's detail, or a best-effort start-command note on an opened tab |`

   and one sentence under the table: `A field that is not decided for a row (a row refused at the page read has no role yet) is the empty string; \`window_id\` is null unless the row opened a tab.` Change nothing else in the doc.

9. **Tests** — create `pkg/open_decision_test.go` (package `pkg`). Build facts with a small helper (e.g. `taskFacts(fields map[string]string) OpenRowFacts` with `Found: true`). One `ginkgo.It` per case:

   a. **AC11 — role arms:** absent role → `Role agent`, `RoleSource defaulted`, `WriteRole agent`; blank `role: "  "` → same; `role: robot` → `Branch refused`, `Reason undecidable-role`, message contains `robot`; `role: manager` on a task → `Branch yours`, `Path none`, `WriteRole == ""`, `WriteMode == ""`; a goal and a topic → `Role manager`, `RoleSource kind`; a goal with `mode: headless` → `Reason manager-mode-headless`.

   b. **Rule 2 arms:** agent + `mode: interactive` → `Mode headless`, `ModeNote == OpenModeNoteNormalised`, `WriteMode headless`; agent + `mode: headless` → no write, empty note; agent + no mode → `WriteMode headless`, empty note; human + no mode → `Mode interactive`, `WriteMode interactive`; human + `mode: headless` → `Mode interactive`, no write.

   c. **AC12 — row/session/unported arms:** `missing-row` (Found false), `row-unreadable` (ReadErr set; message contains the error text), `truncated-id` (`claude_session_id: "1234abcd"`), `liveness-unreadable` (`LiveErr` set; registry facts otherwise valid), `goal-loop-unsupported`, `target-unsupported` (`target: docker`); `target: local` is NOT refused. A refused decision at steps 4–6 has empty `WriteRole`/`WriteMode` even when the role was defaulted. `shipping: "true"` → `Shipping` true.

   d. **AC13 — branch selection** with `PaneFor` from a map: human task recorded id live with pane `"p7"` → `jump`, `PaneID p7`, `SessionID` set; human live with no pane → `live`; agent row recorded id live (PaneFor returns a pane) → `live`, and a counter inside `PaneFor` shows it was called 0 times; recorded but not live → `resume`, `Resume` equals the id, `Path headless` for agent / `Path tab` for human; nothing recorded → `create`; goal with no recorded id and a live entry named `"g manager"` for goal `"G"` (case-insensitive) with pane → `jump`.

   e. **OperatorNamed:** `flag: true` + `flag_set_by: operator` → true; `+ legacy` → true; `flag: yes`, `flag: On` and `flag: Y` + `operator` → true; `flag: "1"` + `operator` → false (not in the set); `flag: true` + `agent` → false; `flag_set_by` absent → false.

   e2. **`openPathFor`:** table over role × branch — agent+resume/create → `headless`; human/manager+resume/create → `tab`; every role with `jump`/`live`/`yours`/`refused` → `none`.

   f. **AC8 — `ResolveOpenWindow` role-map-key-missing, one case per condition**, each asserting `Reason role-map-key-missing` and a message containing the key name:
      - the map file is absent: `NewRoleMapReader(filepath.Join(ginkgo.GinkgoT().TempDir(), "absent.json")).Read(ctx)` → pass its entries and error;
      - the key is absent: entries with only `Managers` and `Hold`, role human (key `Direct`);
      - `window_id` null: `"Direct": {Chip: new("cyan")}` with nil `WindowID`;
      - the file does not parse: write `{not json` to a temp file, read it with `NewRoleMapReader(path)`, pass the error; the message contains both `Direct` and the text of `readErr.Error()`.
      Repeat the key-absent case for role manager (key `Managers`).

   g. **AC9 — hold-window:** entries `Direct {window 2}`, `Hold {window 2}` → `Reason hold-window`, message contains `Direct` and `Hold`. Same for `Managers`. A distinct window (Direct 12, Hold 2) resolves `OpenWindow{Key: "Direct", ID: 12}`.

   h. **Chip:** human in vault `private-personal` → chip `cyan` (family chip) even when the Direct entry's chip is `"pink"`; manager with Managers chip `"orange"` → `orange`; manager whose Managers entry chip is `"pink"` → still `orange`; manager with nil chip → `orange`; role agent → a `spawn-refused` refusal.

   i. **AC5 — verdict shape:** marshal one decided verdict per branch (`yours`, `live`, `resume`, `create`, a refusal, a `jump`) with `encoding/json`, decode into `map[string]any`, and assert the key set is EXACTLY (literal list in the test, not derived from the struct): `kind vault name role role_source mode mode_note branch path window_key window_id session_id pane_id reason remedy message`. Assert each non-empty value of `role`, `role_source`, `mode`, `mode_note`, `branch`, `path`, `reason` is in the literal token list copied from `docs/open-endpoint.md` § Verdict.

   j. **Log line:** an agent resume verdict renders `open: row=v/task/T role=agent mode=headless branch=resume path=headless window=none reason=-`; a human tab verdict with `WindowID` 12 renders `… path=tab window=12 reason=-`; a missing-row refusal renders `role=- mode=- branch=refused path=none window=none reason=missing-row`.

10. **Self-check:** before finishing, re-run `<verification>` and confirm it passes; walk ACs 5, 8, 9, 11, 12, 13 against the tests you wrote and name the `It` that covers each in the completion report.
</requirements>

<constraints>
Copied from the spec:

- The rules are ported, never restated differently. They come from `claude-supervisor/docs/role-mode-matrix.md` (Rules 1, 2, 4 and § Answer-path remedy) and the `commands/open.md` 0.341.2 baseline. "Undecidable role" means an unrecognised value; absent or blank takes the default, per the matrix.
- `docs/open-endpoint.md` is the single home of the verdict table, the token sets, the decision order and the manager recipe. A change to any of them is a change to that page.
- The role map is read on every call and never cached. Which window each key names is the operator's content and is not re-decided here.
- Every refusal is decided before the first side effect. This prompt has no side effects at all.
- When the role map has no window for the role a row needs, refuse and name the missing key. Never substitute another window. Nothing opens in Hold.
- Do NOT add a fallback window, an "open interactive anyway" flag for agent rows, or a probe-skip flag.
- A missing `role` takes the default; classifying it from the task body is out of scope.
- The verdict fields and token values are a frozen contract with the future thin caller.

General:

- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. This prompt adds files and one doc edit; it changes no existing Go code.
- Anchor edits by identifier names, never line numbers.
</constraints>

<verification>
Run in the repo root:

```
make precommit
go test ./pkg/ -count=1 -ginkgo.focus='DecideOpen|ResolveOpenWindow|FormatOpenLogLine|open verdict' -ginkgo.v
grep -n 'message' docs/open-endpoint.md
grep -c 'OpenReason' pkg/open_decision.go
```

`make precommit` must exit 0. The focused run must list the specs from requirement 9 (name your `Describe` blocks so the focus matches). The last grep must print a number of at least 13.
</verification>
