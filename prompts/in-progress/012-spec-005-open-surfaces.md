---
status: approved
spec: [005-open-endpoint]
created: "2026-11-18T18:59:25Z"
queued: "2026-11-18T19:12:27Z"
---

# Expose the open executor as POST /api/1.0/open and the open_rows MCP tool

<summary>
- The daemon serves one open operation two ways: an HTTP route `POST /api/1.0/open` and an `open_rows` tool on the fleet-facing MCP face.
- Both take the same body (`{"rows": [...]}`), run the same validation and the same executor, and return byte-identical JSON with one verdict per row in request order.
- A malformed request is rejected whole before anything opens: empty rows, an unknown kind, an empty name or vault, a vault that is not configured, a name with a slash, `..` or a control character, a non-JSON body, or a body over 1 MiB.
- The HTTP route keeps the loopback, JSON-only and 1 MiB guards the MCP face already has, and answers 405 for anything but POST.
- Nothing in a request can switch the operation to the flagged tick's mode; an extra field is ignored.
- The three supervisor script paths (answer-path probe, manager launcher, topic ceiling) become daemon flags defaulting to the deployed marketplace copies.
- The README, the contract page and the changelog describe the new endpoint.
</summary>

<objective>
Expose prompt 3's `OpenExecutor.OpenRequest` over `POST /api/1.0/open` and the MCP tool `open_rows`, through one shared entry (`OpenSurface.Serve`) that decodes, validates and marshals, so the two surfaces cannot drift (AC4). Wire the executor, the tab opener and the three script-path flags into the daemon, and document the endpoint (README, `docs/open-endpoint.md` § Daemon flags, CHANGELOG). This is the last prompt of spec 005; the thin `/supervisor:open` caller in `claude-supervisor` will point at this route.
</objective>

<context>
Read the repo `README.md` for project conventions (there is no `CLAUDE.md` in this repo).

Read these files fully before changing anything:

- `docs/open-endpoint.md` — § Request (body shape, `X-Claude-Code-Session-Id`), § Verdict, § Answer-path probe ("The script path is a daemon flag that defaults to the marketplace clone, matching `--flagged-rows-script`").
- `specs/in-progress/005-open-endpoint.md` — DB1, DB7, DB8; ACs 1, 2, 3, 4, 5, 21, 22, 23, 24; § Security / Abuse Cases.
- `pkg/open_decision.go` (prompt 1) — `OpenRow`, `OpenVerdict`, `OpenKind*` constants.
- `pkg/open_executor.go` (prompt 3) — `OpenExecutor` (`OpenRequest(ctx, rows, callerSessionID) []OpenVerdict`, `OpenTick`), `OpenExecutorDeps`, `NewOpenExecutor`. `pkg/open_headless.go` — `(s *RosterToolServer) SpawnOpenHeadless`. `pkg/open_tab.go` (prompt 2) — `NewOpenTabOpener(tabs, registry, vaults, caps, ledger, scripts, now, managerLauncherScript, topicSpawnScript, exitRecord)`. `pkg/script_runner.go` — `ExecScriptRunner`.
- `pkg/roster_mcp.go` — the `ToolName` const block (`ToolSpawnAgent` … `ToolReadPushConfig`), `RosterToolNames()`, `sessionRequiredTools()`, the `RosterToolServer` struct, `ServeHTTP` (`originAllowed`, `isJSONRequest`, `SessionHeader`), `handlePost` (clears the write deadline; `maxMCPBody` cap answered `413`), `callTool` (`toolResult(text, isError)`), `invoke` (the tool switch), `rosterToolDefinitions()`. The file must stay under 2000 lines — put the new tool's code in a new file.
- `pkg/mcptools.go` — `maxMCPBody = 1 << 20`, `originAllowed(req) bool`, `isLoopbackHost`, `isJSONRequest(req) bool`, `marshalJSON`, `toolResult`, `toolDef(name ToolName, description string, props map[string]any, required ...string)`, `stringProp`.
- `pkg/handler/liveness.go` and `pkg/handler/handler.go` — handler style: `http.HandlerFunc`, `writeError(resp, status, err)`, `writeJSON`; method refusal with `errors.Errorf(ctx, "method %q not allowed", req.Method)`.
- `pkg/handler/handler_test.go`, `pkg/handler/parity_test.go` — package `handler_test`, Ginkgo/Gomega dot imports, `httptest`.
- `pkg/roster_mcp_test.go` — `postRPC(server, body, sessionID)`, `rosterForServer()`, and the spec `lists exactly the eleven roster tools`.
- `main.go` — `defaultFlaggedRowsScript`, `flaggedTickFlags` / `registerFlaggedTickFlags()` (the flag-group pattern that keeps `main` inside its length budget), `startFleetFaces(...)` (builds `tabs`, `vaults`, `tabSpawner` with exit recorder `""`, `caps`, and the `&pkg.RosterToolServer{...}` mounted on `/supervisor/mcp`), `expandHome(path string) string`, the `http.Server` `WriteTimeout: 60 * time.Second`.
- `README.md` — `## MCP` → `### Tools` table, `## REST` sections (`### POST /api/1.0/sessions` style).
- `CHANGELOG.md` — `## Unreleased` already exists with one `fix:` bullet; add below it.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/readme-guide.md`
</context>

<requirements>
1. **Sentinel** — in `pkg/errors.go`, next to `ErrUnknownVault`:

   ```go
   // ErrInvalidOpenRequest is returned when an open request is malformed: not
   // JSON, empty rows, an unknown kind, an empty or unsafe name, an empty or
   // unconfigured vault. Nothing has been opened when it is returned.
   ErrInvalidOpenRequest = stderrors.New("invalid open request")
   ```

2. **Shared surface, in a new file `pkg/open_surface.go`:**

   ```go
   // Default script paths: the deployed marketplace clone the running fleet
   // uses, matching --flagged-rows-script.
   const (
   	DefaultAnswerPathCheckScript = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/answer-path-check.py"
   	DefaultManagerLauncherScript = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/manager-launcher.py"
   	DefaultTopicSpawnScript      = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/topic-spawn.py"
   )

   // MaxOpenBody is the open request's body cap — the same 1 MiB the MCP faces enforce.
   const MaxOpenBody = maxMCPBody

   // OpenRequestBody is the body of POST /api/1.0/open and the open_rows arguments.
   type OpenRequestBody struct {
   	Rows []OpenRow `json:"rows"`
   }

   // OpenResponseBody is what both surfaces return.
   type OpenResponseBody struct {
   	Verdicts []OpenVerdict `json:"verdicts"`
   }

   // OpenSurface is the one entry both open surfaces call, so REST and MCP
   // decode, validate, open and marshal through the same code.
   type OpenSurface struct {
   	Executor OpenExecutor
   	Vaults   VaultLister
   }

   // Serve decodes body, validates every row, opens them in request mode and
   // returns the marshalled OpenResponseBody. A malformed request returns an
   // error wrapping ErrInvalidOpenRequest before anything is opened.
   func (s *OpenSurface) Serve(ctx context.Context, body []byte, callerSessionID string) ([]byte, error)

   // ValidateOpenRows refuses a malformed request before anything is opened.
   func ValidateOpenRows(ctx context.Context, rows []OpenRow, vaults []Vault) error

   // LoopbackRequest and IsJSONRequest expose the MCP face's structural guards
   // to the REST route, so both surfaces share them rather than copy them.
   func LoopbackRequest(req *http.Request) bool { return originAllowed(req) }
   func IsJSONRequest(req *http.Request) bool   { return isJSONRequest(req) }
   ```

   `Serve`: `json.Unmarshal(body, &OpenRequestBody{})` — unknown fields are IGNORED (no `DisallowUnknownFields`: a row carrying `"mode": "tick"` must behave exactly as request mode, AC24); a decode error → `errors.Wrapf(ctx, ErrInvalidOpenRequest, "body is not a JSON open request: %v", err)`. `Vaults.List(ctx)` error → return it wrapped WITHOUT `ErrInvalidOpenRequest` (a server-side failure). `ValidateOpenRows`. Then `verdicts := s.Executor.OpenRequest(ctx, rows, callerSessionID)` — `Serve` only ever calls `OpenRequest`, never `OpenTick` (DB8: neither surface can select tick mode). Return `json.Marshal(OpenResponseBody{Verdicts: verdicts})`.

   `ValidateOpenRows` — every refusal wraps `ErrInvalidOpenRequest` and names the row index:
   - `len(rows) == 0` → `rows is empty`;
   - `kind` not `task`/`goal`/`topic` → `row <i>: unknown kind "<kind>"`;
   - `strings.TrimSpace(name) == ""` → `row <i>: name is empty`; `strings.TrimSpace(vault) == ""` → `row <i>: vault is empty`;
   - `name` contains `/`, `\` or `..`, or any rune with `unicode.IsControl(r)` → `row <i>: name %q may not contain a path separator, ".." or a control character`;
   - `vault` not the `Name` of any listed vault → `row <i>: vault %q is not configured`.
   - `first_step` is not validated (composer text, bounded by the body cap; it never reaches an argv or a shell).

3. **REST handler, in a new file `pkg/handler/open.go`:** `func Open(surface *pkg.OpenSurface) http.Handler`. In order:
   - `!pkg.LoopbackRequest(req)` → `403` — checked FIRST, before the method, matching `RosterToolServer.ServeHTTP`, so a non-loopback caller learns nothing about the route;
   - method not `POST` → set `Allow: POST`, `writeError(resp, http.StatusMethodNotAllowed, …)`;
   - `!pkg.IsJSONRequest(req)` → `415`;
   - clear the server's 60 s WriteTimeout for this response with `http.NewResponseController(resp).SetWriteDeadline(time.Time{})`, ignoring `http.ErrNotSupported` (a goal manager's start commands may wait up to six minutes; same idiom as `clearRosterWriteDeadline`);
   - read `io.ReadAll(io.LimitReader(req.Body, pkg.MaxOpenBody+1))`; longer than `pkg.MaxOpenBody` → `400` with message `body exceeds 1 MiB` (AC2 requires 400 here);
   - `out, err := surface.Serve(ctx, body, req.Header.Get(pkg.SessionHeader))`; `errors.Is(err, pkg.ErrInvalidOpenRequest)` → `400`; any other error → `500`;
   - success → `Content-Type: application/json`, `200`, and write `out` verbatim (not through `writeJSON`, which appends a newline — the bytes must equal the MCP tool's text, AC4).

4. **MCP tool, in a new file `pkg/open_rows_mcp.go`** plus minimal edits in `pkg/roster_mcp.go`:
   - Add `ToolOpenRows ToolName = "open_rows"` to the const block and to `RosterToolNames()` (after `ToolReadPushConfig`). Do NOT add it to `sessionRequiredTools()`: a call without a session is answered per row (`answer-path` refusal for agent rows), not refused whole.
   - Add the field `Open *OpenSurface` to `RosterToolServer` with a doc comment (nil means `open_rows` refuses with `open_rows is not wired`).
   - In `invoke`, `case ToolOpenRows: return s.openRows(ctx, args, sessionID)`.
   - In `pkg/open_rows_mcp.go`: `func (s *RosterToolServer) openRows(ctx context.Context, args json.RawMessage, sessionID string) (string, error)` → `s.Open == nil` → `errors.Errorf(ctx, "open_rows is not wired")`; else `out, err := s.Open.Serve(ctx, args, sessionID)`; return `string(out), err`. `func openToolDefinitions() []any` returning one `toolDef(ToolOpenRows, <description>, map[string]any{"rows": <array schema>}, "rows")`, where the array schema is a literal map: `{"type": "array", "description": "rows to open, in order", "items": {"type": "object", "properties": {"kind": stringProp("task, goal or topic"), "vault": stringProp("a configured vault name"), "name": stringProp("the page title"), "first_step": stringProp("optional text typed before the work command")}, "required": []string{"kind", "vault", "name"}}}`. The description says: opens each row the way its role requires (agent headless, human in the Direct window, goal/topic manager in the Managers window), returns one verdict per row identical to `POST /api/1.0/open`, and runs the answer-path probe for this session before any headless spawn. Append `openToolDefinitions()...` in `rosterToolDefinitions()`.

5. **Daemon wiring in `main.go`:**
   - A flag group, registered next to `registerFlaggedTickFlags()`:

     ```go
     // openFlags are the open endpoint's three script paths, registered together
     // so main stays inside its length budget.
     type openFlags struct {
     	answerPathCheckScript *string
     	managerLauncherScript *string
     	topicSpawnScript      *string
     }

     func registerOpenFlags() openFlags {
     	return openFlags{
     		answerPathCheckScript: flag.String("answer-path-check-script", pkg.DefaultAnswerPathCheckScript,
     			"path to claude-supervisor's answer-path-check.py, run before every headless spawn the open endpoint makes for a caller"),
     		managerLauncherScript: flag.String("manager-launcher-script", pkg.DefaultManagerLauncherScript,
     			"path to claude-supervisor's manager-launcher.py, which resolves a goal or topic manager's launcher"),
     		topicSpawnScript: flag.String("topic-spawn-script", pkg.DefaultTopicSpawnScript,
     			"path to claude-supervisor's topic-spawn.py, the topic manager ceiling the open endpoint checks before a topic create"),
     	}
     }
     ```
   - The flag defaults are the `pkg.Default*Script` constants from requirement 2 — never string literals in `main.go` — so AC21 is proven twice: the constants are unit-tested in `pkg/open_surface_test.go` (requirement 6f) and their use as defaults is grep-verified in `main.go` (`<verification>`).
   - Pass the three expanded paths (`expandHome(*...)`) into `startFleetFaces` (one new parameter: a small struct of three strings, e.g. `openScriptPaths`).
   - In `startFleetFaces`, bind the `&pkg.RosterToolServer{...}` literal to a variable `rosterServer` (unchanged fields) and call a new helper `mountOpenSurface(mux, rosterServer, tabs, registry, vaults, caps, ledger, now, paths)` BEFORE `mux.Handle("/supervisor/mcp", rosterServer)`. The helper builds:
     - `opener := pkg.NewOpenTabOpener(tabs, registry, vaults, caps, ledger, pkg.ExecScriptRunner{}, now, paths.managerLauncher, paths.topicSpawn, "")` — exit recorder `""`, the same value and the same comment as the tab spawner's ("the daemon ships no copy of the script");
     - `executor := pkg.NewOpenExecutor(pkg.OpenExecutorDeps{Vaults: vaults, Registry: registry, Panes: pkg.NewWeztermPaneJoiner(""), RoleMap: pkg.NewRoleMapReader(""), Tabs: tabs, TabOpener: opener, Headless: rosterServer, Runner: pkg.ExecCommandRunner{}, Scripts: pkg.ExecScriptRunner{}, AnswerPathScript: paths.answerPath})`;
     - `surface := &pkg.OpenSurface{Executor: executor, Vaults: vaults}`; `rosterServer.Open = surface`; `mux.Handle("/api/1.0/open", handler.Open(surface))` with a one-line comment naming `docs/open-endpoint.md`.
   - Do not wire `OpenTick` anywhere (spec `open-tick-reroute` does). Keep `main` and `startFleetFaces` inside `funlen`.

6. **Tests.**

   a. **AC2 — `pkg/handler/open_test.go`** (package `handler_test`). Build a real `pkg.OpenSurface` over `pkg.NewOpenExecutor(pkg.OpenExecutorDeps{...})` with handler-local fakes for the exported interfaces (`pkg.VaultLister`, `pkg.SessionRegistry`, `pkg.PaneJoiner`, `pkg.RoleMapReader`, `pkg.TabClient`, `pkg.OpenTabOpener`, `pkg.OpenHeadlessSpawner`, `pkg.CommandRunner`, `pkg.ScriptRunner`), a temp vault `v1` with task pages, and `httptest`. Requests use `Host: localhost` and `Content-Type: application/json`. Assert:
      - a well-formed two-row request → `200`, two verdicts in request order;
      - each of these → `400` and 0 calls on the headless, tab-opener and probe fakes: `{"rows":[]}`; kind `"widget"`; empty `name`; empty `vault`; vault `"nope"`; name `"a/b"`; name `"a..b"`; name `"a\u0007b"`; body `not json`; a body of 1 MiB + 1 byte;
      - `GET` → `405`; `Host: example.com` → `403`, also for a `GET` (loopback is checked before the method); `Content-Type: text/plain` → `415`.

   b. **AC5 — verdict shape at the handler:** decode a response containing an agent `resume`, a human `create` and a `missing-row` refusal into `[]map[string]any`; assert every verdict has exactly the 16 keys (literal list: `kind vault name role role_source mode mode_note branch path window_key window_id session_id pane_id reason remedy message`) and every non-empty token field value is in the literal token sets of `docs/open-endpoint.md` § Verdict.

   c. **AC4 — REST/MCP parity, `pkg/handler/open_parity_test.go`:** the same three rows (one agent `resume`, one human `create`, one refused) sent through `handler.Open(surface)` and through `&pkg.RosterToolServer{Roster: <a fake pkg.Roster>, Open: surface}` (`tools/call` `open_rows` with the identical `rows` object as `arguments`, same `X-Claude-Code-Session-Id`), each against a FRESH copy of identical fakes. Assert the REST body bytes equal `result.content[0].text` byte-for-byte, and the two runs recorded identical headless requests, tab-opener requests and probe argvs.

   d. **AC24 at the surfaces:** a row carrying `"mode": "tick"` through REST and through `open_rows` → the probe fake records 1 call and the tab client's activation fake records 1 activation for a jump row, exactly as without the field.

   e. **AC3 — `pkg/roster_mcp_test.go`:** rename `lists exactly the eleven roster tools` to `lists exactly the twelve roster tools` and add `"open_rows"` to its `ConsistOf`. Add a spec asserting the `open_rows` definition's `inputSchema` requires `rows`; and one that `open_rows` with `Open: nil` returns `isError` with `not wired`.

   f. **AC21 — `pkg/open_surface_test.go`:** assert each `Default*Script` constant equals `~/.claude/plugins/marketplaces/claude-supervisor/scripts/<file>.py` for `answer-path-check`, `manager-launcher`, `topic-spawn`; plus table tests for `ValidateOpenRows` (every refusal in requirement 2, each `errors.Is(err, ErrInvalidOpenRequest)`; a valid request with `first_step` passes).

7. **Docs.**
   - `docs/open-endpoint.md`: add after § Verdict a `## Response` section (`{"verdicts": [<verdict>, ...]}`, one per request row in request order; a malformed request is `400` on REST and `isError` on `open_rows`, with nothing opened) and, before § Known limitations, a `## Daemon flags` section with a table naming `--answer-path-check-script`, `--manager-launcher-script` and `--topic-spawn-script`, each with its default (the `~/.claude/plugins/marketplaces/claude-supervisor/scripts/…` path) and the script it locates. Change nothing else.
   - `README.md`: add a `### POST /api/1.0/open` section in `## REST` after `### POST /api/1.0/sessions` (or after the session routes; keep heading level 3), 4–8 lines: the body shape, the `X-Claude-Code-Session-Id` header, "returns `200` with one verdict per row in request order", the `400` cases in one sentence, and a link `[docs/open-endpoint.md](docs/open-endpoint.md)` for the verdict, decision order and recipes. In `### Tools` add a row `| \`open_rows\` | open task, goal or topic rows by role — served on \`/supervisor/mcp\`, the fleet face; same rows and verdicts as \`POST /api/1.0/open\` |` and amend the sentence above the table that counts "the nine below" so it stays true (it describes the Codex `/mcp` face; say `open_rows` is the one row served on `/supervisor/mcp`). Add the three flags to the README's flag/run documentation only if the README already lists daemon flags there.
   - `CHANGELOG.md`: under `## Unreleased`, after the existing bullet, add one bullet starting `- feat: **the wrapper gains the open endpoint, \`POST /api/1.0/open\` and the MCP tool \`open_rows\`,**` and describing in 2–4 sentences: one verdict per row computed by code; agent rows always headless behind the answer-path probe; human rows in Direct and goal/topic managers in Managers; a missing role-map key or the Hold window refused by name; the three new script-path flags. Do not edit a released section.

8. **Self-check:** before finishing, re-run `<verification>`; confirm the `awk` prints exactly `## Unreleased` (once per matching line, all `## Unreleased`); walk ACs 1, 2, 3, 4, 5, 21, 22, 23, 24 and name the covering test or grep in the completion report.
</requirements>

<constraints>
Copied from the spec:

- One decision, two surfaces: `POST /api/1.0/open` and `open_rows` take `{"rows": [{"kind", "vault", "name", "first_step"}]}`, delegate to the same code, and return one verdict per row in request order. One row's refusal never aborts the rows after it.
- Both surfaces share the loopback listener's structural guards the README documents for `/mcp`: loopback `Host` and socket, `application/json`, a 1 MiB body cap.
- Caller-controlled `vault`, `kind` and `name` are validated. `kind` is a closed set. `vault` must be a configured vault. A name with `/`, `..` or a control character is refused `400`. `name` reaches `vault-cli`, `manager-launcher.py` and `topic-spawn.py` as one argv element, never through a shell.
- `first_step` is composer text only.
- The calling session id travels in the existing `X-Claude-Code-Session-Id` header on both surfaces.
- The executor's mode is set by the entry point, never by the request. Neither HTTP nor MCP can select tick mode.
- The verdict fields, `branch`, `path` and `reason` tokens, the route path, the MCP tool name and the daemon flag names are the contract with the future thin caller and are frozen. `docs/open-endpoint.md` is their home.
- The three external script paths are daemon flags, each defaulting to the marketplace-clone path, matching `--flagged-rows-script`.
- Existing routes are unchanged, and so are `spawn_agent`, `spawn_session` and the flagged reopen tick.
- Do NOT add a fallback window, an "open interactive anyway" flag for agent rows, or a probe-skip flag.

General:

- Do NOT commit — dark-factory handles git.
- Existing tests must still pass (the one deliberate change is the roster tool-count spec in requirement 6e).
- Anchor edits by identifier names, never line numbers. Keep `pkg/roster_mcp.go` under 2000 lines.
</constraints>

<verification>
Run in the repo root:

```
make precommit
go test ./pkg/... -count=1 -ginkgo.focus='open|Open|roster tools' -ginkgo.v
grep -rn '/api/1.0/open' main.go pkg/handler
grep -nE 'answer-path-check-script|manager-launcher-script|topic-spawn-script' main.go
grep -nE 'Default(AnswerPathCheck|ManagerLauncher|TopicSpawn)Script' main.go
grep -n '^### POST /api/1.0/open' README.md
grep -n 'open_rows' README.md
grep -n '^## Daemon flags' docs/open-endpoint.md
awk '/^## /{s=$0} /open endpoint|\/api\/1.0\/open/{print s}' CHANGELOG.md
wc -l pkg/roster_mcp.go
```

`make precommit` must exit 0. Each `grep` must print at least one line (the script-flag grep and the `Default*Script` grep at least three each). The `awk` must print only `## Unreleased` lines. `wc -l` must print a number below 2000.
</verification>
