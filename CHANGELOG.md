# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

- test: **the 2026-11-11 nesting incident is now a regression test, on both key spellings.** `maxConcurrent` 90 + `maxManagers` 13 = 103 against a `maxSessions` of 90 (and 120 + 20 = 140 against a hard 120) refused every spawn with `NESTING_VIOLATION` until the operator repaired the file; the suite covered the *shape* of an over-committed config but never that exact arithmetic, so the shape that actually took the fleet down had no test. The new spec asserts the message carries all three numbers — both children and the sum — and runs the same config under the pre-rename and current names, because the old-name arm alone passes on the pre-rename code and would be evidence of nothing. It belongs here rather than in a live probe: the refusal is a property of `ResolveSpawnAxes`, the config is its argument, there is no load-time check to observe, and `spawn_agent` takes no config override — so a live run can only ever exercise the valid arm. Verified to fail: pointing the assertion at a wrong sum fails the spec. Change set: `pkg/spawn_axes_test.go`, `CHANGELOG.md`.

## v0.20.2

- fix: **a missing `X-Claude-Code-Session-Id` header now fails the one call that needs it instead of the whole MCP handshake, so a session keeps its `supervisor` tools.** `ServeHTTP` checked the header before dispatch, so `initialize` and `tools/list` were refused with HTTP 400 and the client registered no tools at all — measured 2026-11-11: 39 of 59 live sessions carried no `mcp__supervisor__*` tools while the server was configured and reachable. Only four calls actually consume the session (`spawn_agent`, `stop_agent`, `set_agent_permission_mode`, `answer_permission`); every protocol method and every read-only roster tool (`list_agents`, `agent_status`, `pending_permissions`, `await_permission`, `read_push_config`, `send_agent_message`) takes none. The check moved into `callTool`, where a session-less call to one of those four returns the MCP-conventional `isError` tool result naming `missing_session_header`, at HTTP 200 — attributable to the one call and invisible to the handshake. `writeRosterError` lost its last caller and is deleted. Change set: `pkg/roster_mcp.go`, `pkg/roster_mcp_test.go`, `pkg/errors.go`, `CHANGELOG.md`.

## v0.20.1

- fix: **a headless spawn now receives the launcher's own MCP config, so a worker opened by `spawn_agent` carries the vault's servers instead of only the generated `claude-wrapper` one.** `SpawnOptions.MCPConfigs` existed and `buildArgs` passed every entry to `--mcp-config`, but nothing in the repo populated it: the headless spawn built its options without it, so the child's only server was the generated permission-prompt one. Every vault-declared server — `supervisor`, `a2a`, `sentry` — was absent, and a manager that comes up without the supervisor tools cannot open a worker headless at all: `/supervisor:open` takes its wezterm fallback, which is a tab by construction. Measured 2026-11-11 across 64 live sessions (union of `deferred_tools_delta.addedNames`): 30 carried the tools, 34 did not, and **33 of those 34 had never loaded the server** rather than failed to connect — the missing-session-header 400 accounted for exactly one. `headlessMCPConfigs` resolves the same launcher `headlessModel` already reads (the task's own `launcher:` first, then the vault's worker launcher) and returns its `--mcp-config` file, which `LauncherMCPConfig` parses with the same comment-stripping `ParseLauncherModel` uses — the real `cc-*` launchers mention the flag in prose as well as in the call. ⚠️ **The parser accepts `--mcp-config=PATH` as well as `--mcp-config PATH`, and that alternation is load-bearing**: the real launchers write `--mcp-config="$HOME/.claude/mcp-<vault>.json"` with an equals sign, so a pattern requiring whitespace after the flag matched every hand-written fixture and none of the launchers it was written for. Caught by running the acceptance criterion against `cc-private-deepseek` instead of against a fixture — a check that cannot fail looks identical to one that passed. `$HOME` / `${HOME}` in the value is expanded here, because the child receives the path as an argument rather than through a shell. An unresolvable launcher yields no configs and the spawn still opens: fewer servers is a degraded worker, a refused spawn is a stopped one. Change set: `pkg/launcher_mcp.go`, `pkg/launcher_mcp_test.go`, `pkg/roster_mcp.go`, `CHANGELOG.md`.
- fix: **an answer-reach id carrying a control character is refused, and the failure line quotes rather than interpolates it.** `isBareAnswerReachID` rejected `/`, `\` and NUL but allowed a newline, so a caller-supplied id could forge a log line on the `answer reach record … failed` path; it now refuses every character below `0x20` and `0x7f`, and the log line uses `%q`. Two gosec findings (`G703` on the reach write, `G706` on the log line) were already failing `make precommit` on `master`; the write carries a `//nolint` naming the validator gosec cannot see through, and the log line keeps `%q` as the actual fix. Change set: `pkg/answer_reach.go`, `pkg/roster_mcp.go`, `CHANGELOG.md`.

## v0.20.0

- feat: **every spawn-cap axis key now carries an explicit `Soft`/`Hard` suffix, so the nesting invariant is readable from the names alone — and the pre-rename spellings keep parsing with a warning that names both forms.** The seven axes read `maxWorkerSoft`/`maxWorkerHard`, `maxManagersSoft`/`maxManagersHard`, `maxSessionsSoft`/`maxSessionsHard`, `maxHeadlessSoft`/`maxHeadlessHard`, `maxInteractiveSoft`/`maxInteractiveHard`, `maxTopicManagersSoft`/`maxTopicManagersHard` and `maxGoalManagersSoft`/`maxGoalManagersHard`. `maxConcurrent` no longer said *workers*; `maxManagers` had no `Soft` counterpart while `maxSessions` did; and nothing in the names showed that `headless + interactive <= worker`, `topic + goal <= manager`, `worker + manager <= session` — the invariant whose breach refused every spawn with `NESTING_VIOLATION` on 2026-11-11. Eight keys were renamed (`maxConcurrent`, `maxConcurrentHard`, `maxManagers`, `maxSessions`, `maxHeadless`, `maxInteractive`, `maxTopicManagers`, `maxGoalManagers`); the other six `*Hard` keys already carried their suffix. `mode` and `maxStartsPerMinute` are not cap axes and keep their names, and so do the `SUPERVISOR_MAX_*` environment variables — they are not config keys, so `SUPERVISOR_MAX_CONCURRENT` still sets `spawn.maxWorkerSoft`. `applyLegacySpawnKeys` folds each old name into its current field and logs one warning per key naming both forms; a config carrying both spellings of one axis resolves to the **current** name and says which legacy value it ignored, so the winner is never whichever field a reader happened to consult. Every refusal and help text names the new key. **No cap value changed.** Change set: `pkg/spawn_limits.go`, `pkg/spawn_axes.go`, `pkg/roster_mcp.go`, `main.go`, `pkg/spawn_axes_test.go`, `pkg/spawn_limits_test.go`, `CHANGELOG.md`.
## v0.19.0

- feat: **`answer_permission` records an answer-reach, so a spawner's ability to answer can be checked instead of assumed.** `answer-path-check.py` (claude-supervisor) used to certify a spawner by its armed manager loop alone; measured 2026-11-09, spawner `0110c4a3` read `ok: true` at exit 0 while its 27 workers' parks were released 56 times by the attention store and never once by it. The reach is written on **entry** to the handler, before the arguments are decoded, because the fact recorded is "this session's answer command reached the route" and the coverage sentinel names no agent. Keyed on the caller's `SessionHeader` session, never the target agent. Schema, `SUPERVISOR_ANSWER_REACH_DIR` override and timestamp shape match the claude-supervisor writers and `scripts/answer-path-check.py`, so one reader sees all three.

## v0.18.0

- feat: **managers get `set_agent_permission_mode` on the roster face, so a headless worker stuck on an auto-mode classifier denial can be unblocked without a pane nobody can press Shift+Tab in.** Between 2026-11-05 and 2026-11-11 the attention store logged 131 Shift+Tab closer lines, each an operator card with no performable remedy; the daemon already switched a running child's mode through the SDK control request, but only on its `/mcp` face, which managers do not bind. The tenth roster tool takes `agent_id` and `mode`, accepts exactly `acceptEdits` and `auto` (matched case-sensitively, checked before any lookup so a crafted value never reaches a child), and refuses every other mode by name — `bypassPermissions` stays unreachable because it lifts the permission-prompt path entirely, and a manager is itself a model session. It acts only on a local headless worker this daemon holds (an adopted child included): a tab worker's mode belongs to its pane and a cluster (A2A) worker's is fixed at spawn, and each refusal names the route that does apply. The reply reports `permission_mode` as the CHILD answers it — never the request echoed back — alongside `previous_mode` and `recorded`; the switch takes the same per-session lock the REST and `/mcp` faces take and fails fast with `running a sequence` rather than queuing. Every switch appends one line to `~/.local/state/claude-supervisor/permission-mode-changes.jsonl` with exactly `ts, agent_id, caller_session_id, from, requested, to`; a failed write does not undo a switch that already reached the child, and the reply says `recorded: false`. The shared permission-mode set also gains `auto` — the mode every roster/REST headless child is spawned in — because a worker switched away from it could never be switched back; `/mcp` `spawn_session` and `POST /api/1.0/sessions` therefore accept `auto` too. Spec `specs/in-progress/002-roster-set-permission-mode.md`.

## v0.17.3

- fix: **a headless `spawn_agent` with `shipping: true` now starts the worker in `acceptEdits` instead of `auto`.** The roster tool decoded the flag and dropped it, and the reply never named the mode, so a caller could not tell. Measured 2026-11-11: 119 of 120 live children in `auto`, none in `acceptEdits`, and a live replay with `shipping: true` reported `auto`. The reply gains `permission_mode` and `permission_mode_source` (`shipping` / `default`), and the tool schema documents `shipping`. The mode has exactly one source — the flag — so `bypassPermissions` stays unreachable through this tool; the tab and A2A refusals are unchanged. Spec `specs/in-progress/001-bug-headless-spawn-drops-shipping-permission-mode.md`.

## v0.17.2

- fix: **a headless resume whose task is already `completed` or `aborted` is refused with `task-terminal`, so a close can no longer be undone by a resume that reopens finished work.** The resume that fires in production is `spawn_agent(resume=<id>)` — the fleet keepalive actor's path-A call, which passes no `task` and no `vault` — and nothing on that path read the resumed worker's task status, so a task the fleet had just closed could be handed a live worker again; a resumed finished worker is also a live session with no in-flight model request, which inflates every idle-ratio figure in the fleet. `spawnLocalAgent` now calls `refuseTerminalResume` after the cwd resolves and **before** the caps reserve a start slot, so a refused resume never spends one. The task is resolved from the session id FIRST — `TaskBySessionID` scans each configured vault's `25 Tasks` / `24 Tasks` for a matching `claude_session_id:` — because the production call carries no `task` at all, and a guard keyed on a caller-supplied task would never fire where it matters; a caller that does name one is checked on it as a second signal. `hold` and a future `defer_date` still resume: a parked or deferred worker is exactly what the park-watch resume exists to bring back, so refusing those would break the leg the guard protects. An id that no task carries is ALLOWED with one log line naming it — nothing claims the session, so nothing can be proven terminal, and refusing would break every existing path-A resume; the log line is what keeps that fail-open observable rather than silent. The refusal names the task, its vault and its status, and carries the `ErrTaskTerminal` sentinel so a caller can tell it from a transport failure. Change set: `pkg/roster_mcp.go`, `pkg/spawn_cwd.go`, `pkg/errors.go`, `pkg/roster_resume_test.go`, `CHANGELOG.md`.

## v0.17.1

- fix: **a child that never emits an `init` event is terminated after a startup deadline, so a session can no longer rest in `starting` forever holding its memory and its spawn slot.** `StateStarting` left only through `applyInit`, on the child's `system`/`init` event — no timer, no watchdog, no fallback — so a child that never initialised was `starting` for the life of its process, and `reap-idle-headless.py` refused every one of them `wrapper-busy` because they were not `idle`. Measured 2026-11-10: nine such sessions sat at ~174 MiB each for nearly three hours after the daemon restarted at `17:25:28Z`, and a 19-sid reap closed none of them. `pkg/startup_deadline.go` adds `EnforceStartupDeadline`, run by a sweeper every 30 s: a session whose state is `starting` **and** whose `model` is still empty past `startup.maxStartingSeconds` (default 300 s, `0` disables, read per sweep from `~/.config/claude-supervisor/config.json`) is SIGKILLed by pid — the same path `Stop` takes, so an adopted child whose `*exec.Cmd` this process never held is covered too — and its own watch goroutine settles the record to `exited`. The `model` guard is what keeps this safe: a populated model proves the child has done real work, so a long start there is slow, not stuck, and is never killed. A session with no pid is skipped rather than signalling pid 0, and one with no `StartedAt` is skipped rather than read as infinitely old.

## v0.17.0

- fix: **`send_agent_message` now resolves a headless worker's registry name and hands it to the caller, instead of refusing it with a channel the caller cannot address.** The daemon answered a headless worker with *"agent … is a headless worker with no pane — there is nothing to type into. Answer its parked prompts with answer_permission, or reach it mid-task with SendMessage"*, and that remedy cannot be performed: `SendMessage` resolves by NAME and the caller holds only a session id. Measured 2026-11-08 on a live worker: `SendMessage(to="6d16758d…")` answered `No agent named … is reachable` while `SendMessage(to="claude-1f")` delivered. `SessionRegistry` gains `NamesForSession` — the inverse of `Named`, PID-CHECKED because the result is an ADDRESS a caller will send to, so a crashed session's leftover entry would produce a name that fails at the far end (`Named` keeps presence for the opposite reason: a stale title still blocks a new one). `send_agent_message` returns `{ agent_id, sent: false, channel: "peer", name, session_id, note }`, because the daemon is not a Claude session and cannot call `SendMessage` itself — ⚠️ **`sent: false` is the contract, not a failure report**; claiming `sent: true` would repeat the defect, a channel reported as working while delivering nothing. The failure paths match the Node server's contract exactly (claude-supervisor `server/message-channel.mjs`): two names for one id refuse, an unreadable registry is retryable and never reads as "the worker is gone", and no entry at all refuses with the parked-prompt channel named. A tab worker's refusal is unchanged — it has a pane, and the pane is where its message goes. Change set: `pkg/session_registry.go`, `pkg/roster_mcp.go`, `main.go`, `pkg/session_registry_test.go`, `pkg/tab_spawn_test.go`, `pkg/spawn_cwd_test.go`, `pkg/roster_fixtures_test.go`, `CHANGELOG.md`.

## v0.16.0

- feat: **the fleet cap is split into seven soft/hard axes, so a headless storm binds on its own sub-cap instead of spending the whole fleet's budget.** Until now there were two pairs — `spawn.maxConcurrent`/`maxConcurrentHard` over all workers, headless and interactive sharing one counter, and `spawn.maxManagers`/`maxManagersHard` with no soft/hard split. Measured 2026-11-10: the flagged-open leg opened 36 rows (33 of them headless resumes) and the fleet went 18 → 58 sessions in 25 minutes, stopping only at the shared 50-worker hard cap with 12 rows held, while interactive tabs and managers had nothing left to open into. `pkg/spawn_axes.go` adds `session`, `worker headless`, `worker interactive`, `topic manager` and `goal manager` beside the existing worker and manager pairs, with the operator's table as the shipped defaults (`90/120`, `60/90`, `45/70`, `12/20`, `12/20`, `3/5`, `9/13`) — an absent key resolves to its default, never to unlimited. The children **nest** under their parents at both thresholds and a config that breaks the invariant is refused with a named `NESTING_VIOLATION` rather than clamped, because a child that can outgrow its parent makes the parent's number meaningless. Every refusal now names the axis that bound and its numbers (`axis "worker headless"`, 45 live against 45), so the reader knows which population to close. `CheckWorker` takes the resolved spawn mode and `CheckManager` the manager kind plus the vault roots, both classified by the one rule in `pkg/manager_kind.go` (the label's subject page under `23 Topics/` or `24 Goals/` / `23 Goals/`) — so a manager cannot be counted under one cap and opened under another. The session cap counts every session kind and is the last backstop, refusing even an operator-named spawn at its hard cap. `spawn.maxConcurrent: 0` still switches the worker pair off, but the session cap is now its own key: use `spawn.maxSessions: 0` to uncap the fleet. ⚠️ A config that lowers `maxConcurrent` below the shipped headless + interactive defaults (57) is refused as a `NESTING_VIOLATION` — lower the mode keys with it.

## v0.15.1

- fix: **the spawn gate counts a resumed worker, so the cap binds on the population the keep-alive leg grows.** `pkg/spawn_limits.go`'s `countWorkers` skipped every ledger row carrying `resumed_from`, mirroring the node counter, so a resumed worker was live and invisible to the cap — measured 2026-11-10 on the node twin, **22 counted against 78 live worker sessions**, every missing session a resume. A resume is the same load as a fresh spawn, and a sweep that revives N dead workers has added N to the fleet. `role: manager` is now the only exclusion; the resume path's own crash-loop cap is a separate control and is unchanged. The counting test keeps its manager, ghost, dead and unledgered rows as the control, with the expected count moved 1 → 2.

## v0.15.0

- feat: **a turn end files a terminal verdict on the spawn ledger, so a finished worker leaves a
  `done` row instead of nothing.** The port kept the spawn `POST` and dropped the node server's
  terminal patch: 151 of 151 rows spawned on 2026-11-09 read `status: ""` with no `result`, and no
  reader could tell a worker that finished from one that never started. `LedgerRow` gains
  `result` and `ended_at`, `LedgerReader` gains `Patch` (the store already carries the fields and
  serves `PATCH /api/1.0/session-ledger/{session_id}`; a POST would blank the spawn's own
  fields), and the supervisor that holds the child writes the verdict at its `result` event —
  `done`, or `error` when that event says `is_error`.
- feat: the error half is ported too. A child that leaves with a turn open files `status: error`
  with the cause inside `result`, because the store has no `error` column and refuses a patch
  naming one.
- fix: a new turn re-opens the row (`running`, `ended_at`/`result` cleared) before the turn
  reaches the child. A terminal row stops the heartbeat poster stamping a session, and a headless
  worker here outlives its turn — `done` left behind would read a reachable worker as finished.
- docs: `claude-supervisor`'s `server/agent-loop.mjs` terminal patch is documented as the
  node-only path; the ledger's terminal writer is the process that holds the child.

## v0.14.3

- test: **the headless model precedence is now pinned by a test, not only by the spawn handler's prose.** `pkg/roster_mcp_model_test.go` drives `headlessModel` against a temp vault: the caller's explicit `model` wins unchanged, the task's own `launcher:` frontmatter resolves through that script's export, a task naming none falls back to the vault's `worker_launcher`, an absent task does the same, and a server with no vault lister returns empty rather than a guess. Written because the v0.14.2 review flagged the precedence wiring as untested — the fix itself was verified against a live spawn, which cannot be re-run on demand at the worker cap.

## v0.14.2

- fix: **a headless spawn now carries its task's launcher model, so a worker stops starting on Claude Code's default model while its task names deepseek.** The child is a bare `claude --print`, so the launcher script is never executed; `pkg/roster_mcp.go` passed `body.Model` straight through, and `commands/open.md` never sends one. Measured 2026-11-09: continuous `model=claude-opus-5-5` requests in the router log while every interactive pane read `deepseek-v4-flash-max[1m]`, and a live headless child whose environment held `ANTHROPIC_BASE_URL` and nothing else. `headlessModel` now resolves the model in the daemon when the caller names none: the caller's explicit `model`, else the task's own `launcher:` frontmatter (`TaskLauncherModel`, read from `<vault>/25 Tasks/<task>.md`), else the vault's `worker_launcher` — the same chain a tab spawn follows. `""` still means "no model could be resolved" and leaves the child on its own default, never a guess. The launcher parsing reuses `ParseLauncherModel`, which already strips whole comment lines: the real `cc-private-deepseek` carries `# … a launch that passes no --model inherits the ambient`, and a naive scan reads the model as `inherits`.

## v0.14.1

- fix: **the central heartbeat poster no longer re-stamps a session the spawn ledger records
  as ended.** A headless worker's turn and its child process have different lifetimes: the
  supervisor clears the worker's stamp when the turn ends and the ledger records `done`, but
  the child holds its stdin and keeps its pid, so `sessionRegistry.Live()` still reported it
  and the poster re-created the cleared stamp within one tick. Measured 2026-11-08: session
  `a60db816` read `status: done` with `ended_at 12:25:31Z` while its stamp was rewritten at
  `19:37:53Z` — and again at `22:42:33Z`, 10 h 17 min after it ended. Every reader therefore
  saw a finished worker as LIVE: the auto-resume gate refused to resume it, and
  `worker-sessions.py --count` counted it against the spawn cap. ⚠️ **The rule is
  ONE-DIRECTIONAL, and inverting it is worse than the bug.** A terminal status (`done`,
  `error`) proves the session ended; a missing row, an unreadable ledger and every other
  status prove nothing, and all of them still post. Skipping on an unknown would stop stamping
  most of the fleet, because nothing closes a record whose server went away. The ledger is
  read once per round, and a failed read logs `WARNING: heartbeat ledger could not be read —
  posting every live session` and suppresses nothing. `LedgerRow` gains `status`, which this client previously
  discarded, and `NewHeartbeatPoster` takes the reader as its third argument.
- fix: `source: mcp-timer` was documented in `attention-controller` as *the only source that
  proves a session is posting for itself*, and this poster writes that value for sessions it
  stamps on their behalf. The field is therefore descriptive only; no reader may treat it as
  evidence of self-posting until the producer distinction is carried on the wire.

## v0.14.0

- feat: a `local` `spawn_agent` — tab or headless — now enforces the node supervisor's fleet
  caps before anything is created: `spawn.maxConcurrent` / `maxConcurrentHard` (worker
  soft/hard, defaults 20/50, env `SUPERVISOR_MAX_CONCURRENT[_HARD]`), `spawn.maxManagers` /
  `maxManagersHard` for `role: manager` (defaults 3/5), and `spawn.maxStartsPerMinute`
  (default 4, env `SUPERVISOR_MAX_STARTS_PER_MINUTE`, paced up to 5 min, shared
  `start-rate.json` with the node). A refused spawn returns the node's refusal text — count,
  limit, source and config path — and opens no pane and no child. New `operator_named`
  argument lets a spawn open between the soft and hard caps. Config is re-read per spawn;
  an uncountable fleet refuses rather than reading zero. Known differences from the node:
  hand-started managers (no ledger row) are not counted, and there is no stale-count fallback.
- fix: a paced start no longer loses its response. The roster cleared the
  server's 60 s write deadline before dispatching, so a start held by the rate
  limit (up to 5 min) still answers the caller instead of making it retry into
  the burst the limit exists to bound.
- fix: a headless spawn records its `role` and `resumed_from` on the ledger row,
  so a `role: manager` worker is counted against the manager cap and an
  auto-resume is excluded from the worker cap. Ledger rows are collapsed per
  session id, as the node does, so one session recorded twice counts once.
- feat: A2A (`docker` / `k8s`) spawns answer to the worker cap and the start
  rate too, as the node's `spawnA2AWorker` does.
- fix: the spawn mode and the caps resolve the config file the node's way —
  `SUPERVISOR_CONFIG`, else `$XDG_CONFIG_HOME/claude-supervisor/config.json`, else
  `~/.config/claude-supervisor/config.json` — through one resolver, which the
  A2A client now shares instead of holding its own hardcoded path.

## v0.13.0

- fix: a `local` `spawn_agent` that omits `interactive` now resolves its mode through the node
  supervisor's full chain — explicit `interactive`, then `SUPERVISOR_SPAWN_MODE`, then
  `spawn.mode` in `~/.config/claude-supervisor/config.json`, then the `interactive` default.
  The port honoured only the argument and the default, so with `spawn.mode: headless`
  configured every argument-less spawn opened a WezTerm tab. An invalid value in either
  operator source refuses the spawn, even when `interactive` is passed, as the node does.
- feat: the spawn response and the ledger row carry `mode` and `mode_source`
  (`argument` / `env` / `config` / `default`), so the step that chose a worker's mode is
  reported rather than inferred.
- fix: a headless spawn that names `vault` runs in that vault's directory, and one with neither
  `cwd` nor `vault` is refused instead of running in the daemon's own working directory.

## v0.12.0

- feat: a re-adopted child's exit is observed with kqueue `EVFILT_PROC`/`NOTE_EXIT` instead of a
  100 ms pid poll. After a restart the child is reparented to pid 1, so there is no `Wait` to
  collect and the only observable is the pid going away — the watcher now sleeps until the
  kernel wakes it, so an idle adopted session costs no wakeups and the exit is seen when it
  happens rather than up to one poll interval later. A pid that already left (or never existed)
  returns immediately, and a platform without kqueue falls back to polling.
- fix: a permission park raised **during** a wrapper restart is retried instead of lost. The
  child's MCP sidecar registered a park with one POST; while the daemon was between processes
  that POST failed, `decide` took its failure path, and the tool returned a denial for a
  question no operator ever saw — even though parks are persisted by request id for the
  successor to re-serve, which made that persistence unreachable. Registration now retries with
  exponential backoff (100 ms → 2 s, 15 s budget sized to launchd's SIGTERM → 5 s exit timeout
  → relaunch), retries a refused connection and a 5xx, and does **not** retry a 4xx or a
  cancelled caller — those are a rejected request and a verdict nobody can still receive.
- feat: a `claude-wrapper` restart no longer kills the workers it is streaming. Each child's
  stdin becomes a per-session FIFO and its stdout an append-only `events.jsonl`, both passed as
  descriptors the child itself holds, and the child is started with `SysProcAttr{Setsid: true}`
  — so a daemon death leaves it running instead of taking `EPIPE` on its next write. Measured on
  a scratch launchd job: `kickstart -k` and `bootout` kill the job's own process group and leave
  a setsid child reparented to pid 1.
- feat: the daemon persists its session registry under `sessions/<id>/record.json` and
  re-adopts every live child on start, rebuilding each session from its `events.jsonl` replay.
  Held permission parks are persisted by request id, so an answer that arrives during the
  restart gap is still deliverable instead of being lost or denied.
- feat: SSE frames carry `id:` and the handler honours `Last-Event-ID`, so a reconnecting
  supervisor resumes from the event it stopped at rather than replaying the whole retained
  session. A resume point no longer retained returns `409` (`ErrStreamGap`) instead of silently
  handing back a different point and leaving an invisible hole in the stream.
- feat: one process-wide `SIGTERM`/`SIGINT` handler with a bounded drain and
  `http.Server.Shutdown`. The previous poster-only `NotifyContext` registered and then
  unregistered a handler, so the **first** `SIGTERM` did nothing and the daemon only died on the
  second or on a `SIGKILL`.
- fix: five defects found in review of the restart work. The replay window numbered a large
  session's first replayed line one below its own index, so a client resuming at
  `Last-Event-ID + 1` skipped one event — possibly a `result` — on the first reconnect after a
  restart once the event file passed 8 MiB, and the drift grew by one per restart. The drain is
  now followed by a bounded shutdown instead of a 10 s `Shutdown` that waits on live SSE
  handlers, so the process stays inside launchd's 5 s `ExitTimeOut` and logs its own exit.
  Re-adoption restores each session's `Mode` — a board answer for an adopted headless worker was
  refused as if it were a tab worker — and its spawn policy, without which every adopted park
  escalated and was then denied by the timeout. Concurrent record writes use a per-write temp
  file and a store lock, so two parks from one child can no longer interleave into an
  unparseable `record.json` that `List` skips.
- chore: `make precommit` is clean. The branch's new files tripped `funlen`, `gocognit`,
  `nestif`, `forcetypeassert`, `unused` and four `gosec` findings; the oversized functions were
  split, and the four `gosec` findings are annotated where the analysis is wrong (a pid
  conversion, an SSE frame carrying JSON, and two goroutines that outlive every request).
- fix: the daemon warns at start when `--drain-bound` plus the shutdown grace would reach
  launchd's 5 s `ExitTimeOut`. The bound is a flag, so nothing else stops an operator from
  raising it past the point where launchd SIGKILLs the process mid-drain and the exit is
  reported as a kill rather than as a drain.

## v0.11.3

- fix: a `k8s` spawn waits for the worker's agent card instead of reading it once. The card was
  read immediately after the create call returned, but a worker's ingress is created after that
  point, so the first read is routinely 404 or 503 — both observed on consecutive nukedev spawns,
  with the card answering 200 about a minute later. A single read lost the status channel
  permanently: no card meant no endpoint, no turn, no task id and no push registration, leaving a
  running, healthy worker reporting `push_registration: not-attempted` forever. The read now
  retries inside a bounded window (60 attempts 2 s apart, both configurable on `Manager`), stops
  early on context cancellation, and names the attempt count when the window is spent.

## v0.11.2

- fix: `spawn_agent` resumes a headless worker by session id again. The MCP adapter typed
  `resume` as a bool and `spawnLocalAgent` never forwarded it, so every headless resume
  opened a fresh conversation, and the tab path refused it outright. A dead `mode: headless`
  worker therefore could only come back as a tab. `resume` is now the full session id. The
  headless path passes it to `SpawnOptions.Resume`, which emits `--resume <id>`. The
  pre-fix `"resume": true` fails to decode and the error names the field, so it can no
  longer start a silent fresh spawn. The tab path and the A2A targets still refuse a resume,
  and the tool schema now advertises the field.

## v0.11.1

- fix: release a child's event replay buffer when it exits. Exited records are never removed
  from `s.sessions`, so each dead session held up to `maxRetainedEvents` (4096) decoded
  `Event` maps for the life of the process — measured at 51.15 % of the live heap across 45
  records on a 1.2 GB footprint. The `Session` record and its metadata stay for listing; only
  the event history is released, and `publish` refuses to re-retain it afterwards. Live-session
  replay is unchanged.

## v0.11.0

- feat: `read_push_config` reads a worker's push registration back **from the worker**, so
  `push_registration: attempted-ok` can be told apart from a config the worker actually holds.
  The read half of the A2A push round trip, and the ninth and last of the supervisor's roster
  tools — the MCP cutover asserts nine, so it does not precede this.
- fix: read the A2A cluster allowlist from the shape `vaults.json` actually uses.
  `readVaultsConfig` unmarshalled every vault entry as `{"clusters": [...]}`, so the live file's
  flat arrays (`"private-personal": ["nukedev", …]`) failed to parse and the swallowed error left
  the allowlist empty — every cluster spawn refused for a vault that is in fact declared. Both
  shapes are accepted now; an entry in neither still fails closed.
- test: `pkg/a2a/tunnel_test.go` covers the relay puller — the empty-secret refusal, a pull that
  files a status with `source: "push"`, an unknown token dropped, a non-2xx pull reported as an
  error rather than a delivery, survival of failed pulls followed by a delivery, and a cancelled
  context ending the loop within 2 s.

## v0.10.1

- fix: `spawn_agent` reports the model the tab actually runs on instead of the literal
  `inherits` — the parser matched the launcher's own comment prose (`# … a launch that passes
  no --model inherits the ambient`) before the real `--model` flag, so a correct spawn reported
  a model it never used. Comment lines are now stripped before matching.
- fix: the tab colour's submission confirmation waits the same 8s the node server allows,
  instead of 2s, so a `/color` that applied is reported as applied rather than a false negative.
- fix: a tab `spawn_agent` with no `prompt` is refused with a message naming the headless
  escape (`interactive: false`), instead of the spawner's generic "invalid spawn options". The
  schema keeps `prompt` optional because the headless and A2A paths accept an empty one.

## v0.10.0

- feat: `spawn_agent` opens a WezTerm tab under the vault's `cc-*` launcher and `send_agent_message`
  types into that pane, so the supervisor's most-used spawn path — 3,177 of 4,322 ledger rows — runs
  from the Go daemon instead of the node server. Slice 2 of the port, and the local target the A2A
  slice left refused by name. An absent `interactive` opens a tab (the node's own default); an
  explicit `interactive: false` keeps the in-process headless worker from v0.9.0. Spawn caps are
  **not** part of this slice: the daemon has no cap code yet, so a port of the spawn-limit module
  lands separately and the MCP cutover must not precede it.

## v0.9.1

- fix: compact the per-child event replay buffer in place instead of reallocating it, so a
  wrapper past 4096 events per child stops copying the whole retained window on every event.
  Benchmarked at capacity: 81 921 B/op and 2 allocs/op before, 0 B/op and 0 allocs/op after
  (~55-130 µs/op down to ~1.2-3 µs/op). On a live wrapper the old path measured 76-80 % of
  cumulative allocation and ~129 MB/min against a 1.2 GB footprint. The retained window is
  unchanged — still the most recent `maxRetainedEvents` in publish order — and two specs pin it.

## v0.9.0

- feat: the daemon applies the supervisor's approval policy **in the process that holds the
  park**. `policy.json` is ported to Go (`pkg/policy.go`) with the same rule shape, the same
  anchored `matchType: "command"` semantics, the same `cwd` path-boundary test and the same
  segment-by-segment allowance for a chained Bash command; the bundled defaults are embedded
  and `~/.config/claude-supervisor/policy.json` (`SUPERVISOR_POLICY`) overlays them, ahead of
  which a per-spawn `policy` path may overlay both. A permission park is decided the moment it
  registers — no HTTP hop to itself and no 2-second poll — and the decision is written to
  `permissions.jsonl` with the node server's exact key set, `decided_by: policy` for a released
  prompt and `decided_by: escalated` for one left to the manager. An unanswered park is denied
  after 15 minutes with a `decided_by: timeout` row, which is the window the node server
  enforced and the wrapper is now the only process able to enforce.
- feat: board answers are delivered from the daemon, once, instead of from every session's own
  supervisor. A `permission` card answered on the board settles its park and writes a
  `decided_by: store` row; a `message` card settles it as `deny` + a message prefixed
  `Operator answer, via supervisor:` — never `allow`, which would run an `AskUserQuestion` in a
  tty-less session. Delivery keeps the node server's two-speed cadence (250 ms park re-check,
  2 s while parked, 30 s idle) so the `failed` delivery record survives, and only the process
  holding the producing session records an attempt.
- feat: `answer_permission`, `await_permission` and `pending_permissions` join the roster MCP
  face, and `spawn_agent` grows a `local` target that creates a headless worker in-process and
  delivers its opening turn; the three park tools settle and observe wrapper parks, with
  `answer_permission` writing a `decided_by: manager` row.
- feat: `--permission-timeout`, `--permission-log` and `--mcp-hold` flags; a child's own
  permission-tool hold now follows the daemon's timeout so the daemon's deny is the verdict the
  tool reads rather than the tool giving up first with its default.

## v0.8.0

- feat: the wrapper serves the A2A spawn targets — `spawn_agent`, `send_agent_message` and
  `stop_agent` for `docker`, `k8s` and `k8s:<cluster>` workers created on an
  `a2a-worker-controller`. `spawn_agent` calls the controller's `create` and returns an agent id
  immediately, sending the first turn asynchronously so a cluster card read (measured at ~45 s
  behind an ingress) cannot slow the control call; the local-only arguments `interactive`,
  `resume`, `policy`, `window_id`, `role`, `env` and `shipping` are refused rather than dropped,
  and the retired `cluster` target is refused naming its 2026-10-15 retirement. Per-target URLs
  come from `a2a.url.<key>` in `~/.config/claude-supervisor/config.json` and bearer tokens from
  `SUPERVISOR_A2A_TOKEN_<CLUSTER>`, with a plaintext `a2a.token` in the file refused; a vault's
  cluster allowlist (`vaults.json`) is enforced, so a wrong placement fails as the data-boundary
  breach it is rather than being retried. One push receiver in the daemon accepts a worker's
  `A2A-Notification-Token`-authenticated status, files it under the worker the token names (never
  the body), bounds each worker at 50 entries and surfaces them as `status_messages`; a cluster
  worker's status is pulled from an in-cluster relay (`/relay/pull`), because the Mac cannot
  accept an inbound connection from the cluster.

## v0.7.0

- feat: serve the `net/http/pprof` endpoints on `/debug/pprof/` when `-listen` is a loopback
  address, so a running wrapper can be profiled with `go tool pprof` without a rebuild. The mount
  is gated on the listen address: an empty host (`:18090`), a wildcard, a routable address or an
  unresolvable name withholds the whole surface, because `/debug/pprof/cmdline` publishes the
  process's argv and a heap profile can carry session content. Both halves of the gate log, so a
  mounted endpoint and a skipped registration are distinguishable from default-verbosity output.

## v0.6.0

- feat: the wrapper serves the supervisor's roster tools — `list_agents`, `agent_status` and
  `stop_agent` — over a new HTTP MCP face at `/supervisor/mcp`, with the calling session read from
  an `X-Claude-Code-Session-Id` header supplied by a `headersHelper`; a request without it is
  refused `400 missing_session_header` rather than defaulted, because a roster call attributed to
  nobody is worse than one refused. `list_agents` answers from the union of this process's own
  children and every **live** session the attention store's ledger knows, so a worker spawned by
  another session is visible — the limit a per-process roster has. Liveness is the registry ∪ the
  heartbeat store, because a headless worker is an in-process query with no pid and therefore no
  registry entry. `agent_status` reports the tool a worker is inside right now and how long it has
  held it, read from its transcript tail. `stop_agent` acts only on a child this wrapper spawned
  and refuses any other id by name.
- feat: the daemon posts a session heartbeat every 30 s for every live entry in
  `~/.claude/sessions/`, so a session reads live without having started its own supervisor. Both
  this and the session-ledger client write one attribution line per outbound POST to stdout, which
  is what makes the row attributable to this daemon rather than to any caller that could POST the
  same body.

## v0.5.3

- fix: a `resume` spawn is keyed by the resumed claude session id instead of a freshly minted one,
  so one conversation is one record however often it is resumed. A minted id per resume left the
  original record `exited` under the claude id while the live question sat under an id no reader
  knew, and the attention board refused deliverable answers (`wrapper holds no question`). An
  exited record is replaced; resuming a still-running conversation is refused with `409`.

## v0.5.2

- chore: update github.com/bborbe/errors to v1.6.6, github.com/bborbe/time to v1.27.25

## v0.5.1

- test: a table-driven parity suite over the seven session actions in `pkg/handler` asserts the
  write-deadline, nil-sequencer and partial-reporting invariants for every route, so a route that
  omits one fails `make precommit` instead of depending on a reviewer who happens to know its
  sibling. The action table in `pkg/handler/routes.go` is now the single dispatch source, and the
  test's expected action list is a literal, so a row deleted while its action stays reachable is a
  red suite rather than a silent fall-through to the session route.
- fix: every session route clears the server's `WriteTimeout` before doing any work. `answer`,
  `message`, `question`, `permission-mode` and the default session route were missing the clear, so
  their response could be truncated at the first write past the 60 s deadline.

## v0.5.0

- feat: `POST /api/1.0/sessions/{id}/model` and the `set_model` MCP tool change a **running**
  session's model without respawning it, and report the model that actually served the next turn.
- feat: `POST /api/1.0/sessions/{id}/permission-mode` and the `set_permission_mode` MCP tool change
  a **running** session's permission mode over the SDK's own control channel, and report the mode
  the child answers with rather than the one the caller sent.
- feat: `Session` gains `permission_mode`, read from the child's `system`/`init` event and stored from
  the `control_response` the moment a change is answered, so `GET` reflects a change without waiting
  for the next turn.
- feat: `pkg.Supervisor` gains `SetPermissionMode`. This is an addition to an exported interface,
  so an out-of-tree implementer must add the method.
- fix: an `assistant` event whose `message.model` is `"<synthetic>"` — the turn a local slash
  command answers itself — no longer overwrites the session's model with a value that names no
  model at all.

## v0.4.1

- fix: **the event stream no longer dies at the server's 60 s `WriteTimeout`.** `GET
  …/sessions/{id}/events` is an SSE response that can legitimately write nothing for
  minutes — a child parked on a permission prompt says nothing until it is answered — but
  `main.go` sets `WriteTimeout: 60s`, and Go applies that as a connection deadline at the
  moment the request is read. A deadline closes nothing: it makes the **next write** fail,
  so the stream looks healthy right up to the first event after the 60 s mark, and that
  write then ends the response. A subscriber sees a body cut mid-stream, which a Node
  client reports as `TypeError: terminated` — how a healthy child gets recorded as a dead
  worker. `handleEvents` now clears the deadline, exactly as the macro route and the MCP
  tool already did. Measured before the fix: a stream opened at `t0`, a turn sent after the
  deadline, `curl: (18) transfer closed with outstanding read data remaining` at
  `time_total=73.5 s` with `size` unchanged since the replay. Also: the response headers are
  now flushed before the first frame, so a subscriber to a child with an empty replay can
  tell "connected and quiet" from "not connected yet".

## v0.4.0

- feat: `pkg.Supervisor` gains `ForgetQuestion`, and `DELETE …/question/{request_id}` exposes it.
  This is an addition to an exported interface, so an out-of-tree implementer must add the method.
- fix: a request whose hold expired is now dropped (`DELETE …/question/{request_id}`), instead of
  being reported as pending for the life of the child. Without it every later `get_pending` said
  "parked" and every later `send_message` returned that stale request instead of awaiting its own
  turn.
- fix: an `AskUserQuestion` that expires unanswered now returns a deny naming the expiry, rather
  than the permission default of allow — which let the child proceed with no answers at all,
  silently.
- fix: a registration carrying an out-of-set `kind` is rejected with `400` instead of creating a
  request no answer shape can ever match.
- feat: `get_pending` and `answer_pending` join the client-facing MCP face, so a caller can see
  what a session is parked on — including an `AskUserQuestion`'s options — and answer it without a
  second round-trip or a REST call.
- fix: `send_message` returns as soon as the child registers a request, instead of waiting for the
  turn to close. The child's permission hold is 90 s, so a caller that only learned of the question
  at turn-close could never answer inside it — the recorded-but-never-delivered failure this
  release exists to remove.
- fix: every child was pointed at `http://localhost:18090` regardless of the daemon's own
  `--listen`. A wrapper on any other port handed its children the URL of a *different* process, so
  registrations failed silently and the gate defaulted to allow. The URL is now derived from
  `--listen` via `pkg.SelfURL`, which also maps a bare port (`:18090`) and a wildcard host
  (`0.0.0.0`, `::`) to loopback — both are valid listen targets whose naive prefix the child's
  HTTP client rejects.
- fix: the permission tool's `updatedInput` for an `AskUserQuestion` must satisfy the tool's own
  input schema, so it now echoes the original `questions` back beside `answers`. Returning
  `answers` alone made the child reject the answer with `The required parameter "questions" is
  missing`.
- feat: the pending registry carries a kind-discriminated payload instead of a bare allow/deny
  string. A permission request keeps its tool name and input; an `AskUserQuestion` keeps its
  `questions[]` list — each prompt with its own `options[]`, `header` and `multiSelect` — so a
  selection can be recorded rather than coerced into a verdict. Both shapes arrive through the same
  child-facing `permission_prompt` tool; the tool name is what tells them apart.
- feat: `POST /api/1.0/sessions/{id}/answer` accepts `{"request_id","answers":{…}}` for a question
  alongside the existing `{"request_id","behavior"}` for a permission, and
  `GET …/question/{request_id}` returns the full payload with the recorded answer.
- feat: an answer whose shape does not match the pending request returns **422 Unprocessable
  Entity** through a new `ErrAnswerShapeMismatch`, distinct from the existing 409 for an unknown or
  already-answered request, and leaves the request unanswered.

## v0.3.0

- feat: serve a client-facing MCP face at `/mcp` on the daemon's own mux, over streamable HTTP, so an
  MCP client (Codex) can drive a Claude session instead of only REST. Five tools —
  `spawn_session`, `send_message`, `list_sessions`, `get_session`, `kill_session`. This is the
  opposite direction from `--mcp-stdio`, which is child-facing: that one gives a child a permission
  tool, this one lets a client drive a child.
- feat: `send_message` returns the assistant text that closed the turn, taken from the child's
  `result` event. The REST `POST …/message` route answers `202 {"status":"sent"}` and carries no
  answer, so this is the one tool that cannot delegate to it.
- feat: a turn that never closes returns a tool error naming the timeout (`--mcp-turn-timeout`,
  default 120s) rather than a success. The write deadline is cleared for the duration of a call,
  because the turn deadline deliberately outlasts the server's 60s `WriteTimeout` and a response
  written after it would never reach the client.
- fix: `/mcp` rejects a non-loopback `Host` or `Origin`, a socket whose own local address is not
  loopback, and a body that is not declared `application/json`. A localhost HTTP server is
  otherwise reachable by any browser page through DNS rebinding, which with `spawn_session` on
  the other end is remote code execution as the daemon user. An oversized body answers `413`
  rather than being truncated into a parse error.
- fix: `send_message` refuses a session whose child already has a turn open. The session lock
  excludes other writers, but the timeout path releases it while the child is still generating —
  writing then would open a second turn on top of the first and make the two results
  indistinguishable.

## v0.2.0

- feat: run a named macro — a fixed, ordered list of user turns — against one child, writing each step
  only after the previous turn's `result` event arrives. Until now the wrapper could send one turn
  and watch, so a sequence like `/vault-cli:prepare-compact` → `/compact` →
  `/vault-cli:post-compact` had to be driven by hand: a timer cannot do it, because a compaction's
  duration is unpredictable and a delay short enough to be safe fires the next command mid-turn.
  The endpoint takes a macro name rather than a command list (`compact`, `terminal`) because it can
  drive `/exit`, so what it is able to run stays reviewable as a diff. Both macros contain a
  production-touching step and are refused with 409 without `confirm`; the call blocks and answers
  with every step, naming the ones a stopped run never reached.

## v0.1.0

- feat: let a spawn hand the child its own environment, resume an existing conversation, and load
  extra MCP config files. Each closes a silent divergence: without `env` the child inherits the
  wrapper's environment instead of the one its caller resolved; without `resume` a conversation
  cannot be continued through the wrapper at all; without `mcp_configs` a launcher's own
  `--mcp-config` file — a CLI flag, so no setting source can reach it — is absent and the child
  runs with fewer servers than its hand-started twin. A malformed `env` key or value, a
  flag-shaped MCP config entry and a non-UUID `resume` are refused with 400 rather than reaching
  the child's argv: `--mcp-config` is variadic, so an entry beginning with `-` would be parsed as
  a flag and silently change the child instead of being rejected.

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
