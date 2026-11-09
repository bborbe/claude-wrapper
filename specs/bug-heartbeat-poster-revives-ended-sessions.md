---
status: draft
kind: bug
---

## Summary

- The wrapper daemon's central heartbeat poster stamps every session the pid-keyed session registry calls live.
- A session the supervisor has already ended keeps a fresh heartbeat, because its child process outlives its turn.
- A worker therefore reads `LIVE` to every reader long after its work finished — measured: 7 h 12 min after the ledger recorded `done`.
- Readers that act on that reading are wrong in the expensive direction: the auto-resume gate refuses to resume a finished worker, and the spawn-cap count counts it against the fleet.
- The fix is for the poster to skip sessions whose spawn-ledger row is terminal — and to keep stamping whenever the row is missing, unreadable, or non-terminal.

## Problem

`claude-wrapper` runs a central `HeartbeatPoster` that exists to cover sessions with no supervisor of their own. It derives its work from `sessionRegistry.Live()`, which answers "does this pid exist?" — a question about a **process**, not about a **session's work**.

A headless worker's turn and its child process have different lifetimes. When the supervisor's agent loop ends a turn it clears the worker's heartbeat stamp deliberately (`agent-loop.mjs`'s `finally`), and the ledger records the worker as `done`. The child `claude` process, however, is spawned with `--input-format stream-json` and holds its stdin, so it does not exit on its own; its pid stays alive and its registry entry stays present. Within 30 s the poster re-creates the stamp the supervisor just cleared.

The result is a session that is simultaneously:

- **not addressable** — the supervisor's map row is terminal, and the harness peer list carries interactive sessions only;
- **not resumable** — the auto-resume gate holds only when no id in the session's id set is live, and the forged heartbeat satisfies "live";
- **counted against the spawn cap** — `worker-sessions.py --count` joins the ledger to liveness, and the forged row supplies the liveness.

Nothing announces the loss, because from every reader's point of view the session looks healthy.

## Goal

The wrapper's heartbeat poster stamps only sessions that are not known to have ended. A session whose spawn-ledger row is `done` or `error` receives no heartbeat from the poster, so its liveness falls back to whatever the supervisor left behind — which is nothing, because the supervisor clears the stamp when the turn ends.

Every other session keeps being stamped exactly as it is today, including a session with no ledger row at all.

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0 — evidence: exit code.
- [ ] **AC2** — **One table-driven test covers the whole decision rule**, and its evidence is a **mutation check** — the one shape a fabricated print cannot satisfy. Two runs:

  1. `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='suppresses only a terminal ledger row' -ginkgo.v` exits 0 and prints seven lines of the form `case=<name> ledger=<status> posted=<bool>`, with `posted=false` for `case=done` and `case=error` and `posted=true` for the other five.
  2. With the terminal set emptied (`terminalLedgerStatuses` assigned an empty set), the same command **exits non-zero with `[FAILED]` in the spec report and all seven `case=` lines still printed**. The verifier applies the mutation, runs, and restores with `git checkout -- pkg/heartbeat.go`. A build failure does **not** satisfy this half — a non-zero exit from a broken compile would let a fabricated-print test read as a passing mutation check.

  In both runs, exit 0 alone is not the evidence: `-ginkgo.focus` on a string matching no spec reports `Ran 0 of 1 Specs … SUCCESS!` and exits 0, so the named output must also appear.

  `-v -count=1` is required on both: without `-v`, `go test` swallows a passing package's output, so a `grep` over it returns 0 however correct the implementation is; `-count=1` stops a cached result replaying a pre-fix pass.

  The test drives `round()` with a registry of seven live sessions and a fake ledger, asserts the **posted session-id set on the recording client** (not a count, and not a print), and asserts the fake ledger's `Rows()` was called exactly once for the round.

  | Case | Ledger rows for that session | Posted? |
  |---|---|---|
  | `done` | one row, `status: "done"` | no |
  | `error` | one row, `status: "error"` | no |
  | `running` | one row, `status: "running"` | yes |
  | `empty` | one row, `status: ""` | yes |
  | `cancelled` | one row, `status: "cancelled"` (an unknown future status) | yes |
  | `no-row` | none | yes |
  | `duplicate` | two rows for one id, `done` then `running` | yes |

  The `running` case is the load-bearing one: nearly every live session carries it, so a rule that inverted this direction would suppress almost the whole fleet. `duplicate` is the resumed-session case — the last row wins.
- [ ] **AC3** — An unreadable ledger suppresses nothing — evidence: `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='posts for every live session when the ledger cannot be read' -ginkgo.v` exits 0, prints `case=ledger-unreadable posted=true` for a registry of three live sessions, and prints exactly one line matching `WARNING: heartbeat ledger could not be read`. That exact prefix is mandated in DB 5 and in `## Constraints`, so the AC and the behaviour cannot drift apart.
- [ ] **AC4** — `LedgerRow` decodes `status` — evidence: `grep -n 'json:"status' pkg/ledger.go` returns ≥1 line (it returns 0 today; the tag is grepped without its closing quote so a `,omitempty` form still matches), and `go test -v -count=1 ./pkg -run TestPkg -args -ginkgo.focus='decodes the ledger status' -ginkgo.v` exits 0 and prints `status=done` after unmarshalling a row carrying `"status":"done"`.
- [ ] **AC5** — The poster takes its ledger by injection and the wiring passes the existing client — evidence: `grep -n 'LedgerReader' pkg/heartbeat.go` returns ≥1 line; `grep -n 'NewLedgerClient' pkg/heartbeat.go` returns 0 lines; `grep -nE 'NewHeartbeatPoster\(registry, heartbeats, ledger, heartbeatInterval\)' main.go` returns exactly 1 line.
- [ ] **AC6** — A non-terminal session's posted row is unchanged — evidence: the existing spec `posts for a live session, skips a dead pid, and stops on cancel` still asserts `source == "mcp-timer"`, `location == "local"` and `state == "idle"` and passes under `make test`; its name is still present (`grep -n 'posts for a live session, skips a dead pid, and stops on cancel' pkg/heartbeat_test.go` returns ≥1).
- [ ] **Post-Deploy (Rung-2):** (AC7) after install and a poster restart, the reproduction no longer reproduces **and** a live session still gets stamped — evidence, both halves in the same 90 s window:
  - **the worker replayed in `# Reproduction` step 1**, whose ledger row reads `done` and whose pid is still alive at probe time (`kill -0 <pid>` exits 0 — a `done` session whose pid has already exited is never stamped by the poster even pre-fix, so probing one passes with the old binary), shows an unchanged stamp mtime, or no stamp file at all — probed as `stat -f %Sm -t '%H:%M:%S' <path>` succeeding with the same value twice, **or** `test ! -e <path>` succeeding (a bare `stat` on a missing file errors indistinguishably from a typo in the path);
  - **positive control:** a concurrent session whose ledger row reads `running` shows an advancing mtime **and** its stamp carries `"pid": null` — poster-written only. Without the `pid` clause an interactive session's own self-stamp satisfies the control even with the poster stopped, which is exactly the false pass this half exists to prevent.

  ```bash
  stat -f %Sm -t '%H:%M:%S' ~/.local/state/claude-supervisor/live/<sid>.json
  grep -o '"pid": *null' ~/.local/state/claude-supervisor/live/<control-sid>.json
  ```

  - `deploy_check:` `go version -m ~/.local/bin/claude-wrapper | sed -n 's/.*vcs\.revision=\(.\{7\}\).*/\1/p'`
  - `deploy_target:` `$(git rev-parse --short HEAD)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — tidy, format, lint and test all exit 0
- `make test` — the Ginkgo suite passes, including the three new specs named in the ACs
- `grep -n 'LedgerReader' pkg/heartbeat.go` — the reader is injected, not constructed
- `grep -n 'NewLedgerClient' pkg/heartbeat.go` — returns 0 lines

### Operator-executable (runs on the host after PR merge)

- `make build && make install` — the daemon binary is replaced
- `launchctl kickstart -k gui/$UID/de.bborbe.claude-wrapper` — the poster restarts with the new code
- The `Post-Deploy (Rung-2)` AC above is the reproduction replay; it runs on the host, not in the container.

## Desired Behavior

1. The poster reads the spawn ledger once per round, alongside the session registry it already reads.
2. A session whose ledger row carries `status` `done` or `error` is omitted from that round's posts.
3. A session with no ledger row is posted. Absence of a row is not evidence of anything, and treating it as "ended" would silently stop stamping every session the ledger has not yet recorded.
4. A session whose ledger row carries any other status — including `running`, an empty string, and any status added later — is posted.
5. When the ledger read fails, the round proceeds and posts for every registry-live session. The failure is logged once per round as `WARNING: heartbeat ledger could not be read — posting every live session: <cause>`, and never silently converts a store outage into a fleet-wide "nothing is live". The prefix is fixed so the AC can assert it; only the `<cause>` suffix is the implementer's wording.

## Constraints

- **The rule is one-directional, and inverting it is worse than the bug.** A terminal ledger row proves the session ended; the absence of a row proves nothing, and neither does a non-terminal status. Nothing in this change may skip a session on a missing row, an unreadable row, or an unknown status — that direction kills live workers, which is strictly worse than the stale heartbeat being fixed here. This invariant is to be carried as a doc comment on the status set, in the style the repo already uses for load-bearing rules (`pkg/heartbeat.go`, `pkg/ledger.go`).
- The ledger's own header states why: nothing closes a record whose server went away, so most records still assert `running`. Terminal is the only trustworthy value.
- **`LedgerRow` gains `Status string` (json `status`).** The store serves it today — the reproduction payload below carries `"status":"done"` — and this repo's client silently discards it. The poster is the field's only consumer.
- **The ledger is keyed by `session_id`, and where more than one row is served for one id the LAST row wins** — the same rule `ledgerBySessionId` applies in `claude-supervisor`. This matters because a resumed session keeps its id: a stale terminal row followed by a new `running` row must read as live.
- The row a non-terminal session receives stays byte-identical to today's: `source: "mcp-timer"`, `location: "local"`, `state: "idle"`. The round also stays cancellation-aware: a cancelled context stops the round between posts.
- The assertion lines on `source`, `location` and `state` in the existing poster tests stay unchanged; the constructor call sites in `pkg/heartbeat_test.go` may gain the reader argument.
- No new dependency on the ledger for any other code path. The poster is the only new reader.
- The ledger read must reuse the existing client (`NewLedgerClient`) rather than a second HTTP implementation.
- **The constructor signature is frozen:** `NewHeartbeatPoster(registry, client, ledger, interval)` — the ledger reader sits third, and the production call site becomes `NewHeartbeatPoster(registry, heartbeats, ledger, heartbeatInterval)` in `main.go`, where `ledger` is the client already built at `main.go:254`. Freezing it is what lets AC 5 assert the wiring mechanically instead of by eye.
- **The third parameter's type is the existing `LedgerReader` interface from `pkg/ledger.go`.** No second, narrower interface is declared in `pkg/heartbeat.go` — the local pattern there (`HeartbeatClient`, `HeartbeatReader`) would otherwise invite a lowercase `ledgerReader`, and AC 5's grep is case-sensitive.
- **The terminal status set is a named value `terminalLedgerStatuses` in `pkg/heartbeat.go`.** The mutation check in AC 2 empties that value and edits nothing else, so the name is a contract rather than an implementation preference.
- **The ledger-failure log line is `WARNING: heartbeat ledger could not be read — posting every live session: <cause>`.** ⚠️ It must NOT reuse the registry line's "round skipped" wording: the round continues and posts for every live session, so "skipped" would read as "nothing was stamped", the opposite of what happens. The distinct prefix is also what lets AC 3 assert exactly one occurrence without matching the registry failure. Only `<cause>` is the implementer's wording.
- The ledger read is a full list read per round: measured 4,540 rows / ~4.1 MB on this host. One list read beats one per-session read per registry entry (~56 requests), and the store serves it from an in-memory snapshot.
- The 15 s read timeout shared by the store's clients is not changed by this work.
- The `attention-controller` and `claude-supervisor` files cited in `# Why this is a bug` are **read-only references** — neither is in this repo and neither is to be modified.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Ledger endpoint unreachable | Round proceeds; every registry-live session is posted; one WARNING log line per round naming the cause | Store recovers; next round filters again. No operator action. |
| Ledger read slower than the tick | Round takes longer than 30 s; the next tick is delayed, not overlapped | None needed; the loop is serial by construction. |
| Ledger list read is large | One list read per round is the cheaper shape: one request against the store's snapshot, versus one per-session request per registry entry (~56 on this host). Measured 2026-11-08: 4,540 rows / ~4.1 MB per read | None; the read is bounded by the client's own 15 s timeout (`pkg/ledger.go`), not by the store. |
| A ledger row is `running` for a session that has actually ended | The session is still stamped — the pre-existing behaviour, unchanged by this fix | Out of scope: the record was never closed. This spec does not attempt to detect it. |
| A resumed session carries a stale terminal row beside its new `running` row | The last row wins, so the session is stamped | None; stated as a constraint and locked by the `duplicate` table row. |
| Two daemons run concurrently | Both post the same row for the same session; the store's write is atomic and last-writer-wins | Kill the duplicate daemon. |
| A session id appears in the registry but has no ledger row | Posted, as today | None. This is the one-directional rule working as designed. |
| The ledger serves a terminal row for a session that is in fact still running | The session is suppressed — the fix trusts the ledger, and this is the one direction where trusting it costs live work: the auto-resume gate would then resume a live worker | Detect by the spawn-cap count falling below the expected fleet while the registry still lists those pids; file against the ledger writer, not against this poster. The alternative — distrusting terminal rows — reinstates the bug this spec exists to fix. |

## Alternatives Considered

- **Supervisor kills the stream-json child when a turn ends.** Rejected as out of scope: it changes the wrapper/supervisor lifecycle contract, and the child-holds-stdin behaviour is deliberate (a worker must be able to take another turn). The heartbeat should describe the session, not depend on the child exiting.
- **The store refuses a `mcp-timer` POST for a session with a terminal ledger row.** Rejected for now: it fixes every producer rather than this one, but it puts a cross-store read on the write path of a service whose whole job is to be cheap, and it needs the `pid`/`mode` discriminator that the wire does not carry. Recorded as the deferred option.
- **Do nothing.** The defect is silent by construction and costs a worker its context every time a manager hits it; the stale row also inflates the spawn-cap count. Cheap to fix, so not doing it is not defensible.

## Reproduction

Observed 2026-11-08 on this host, against the wrapper daemon running commit `611531a` (`release v0.13.0`). The smallest reproduction is a headless worker whose turn ends while its child process stays alive.

1. Spawn a headless worker through the supervisor (`spawn_agent` with `interactive: false`). Record its session id.
2. Let its turn end. The supervisor clears the stamp and the ledger records `done`.

   ```bash
   curl -s "http://localhost:18080/api/1.0/session-ledger/<sid>"
   ```

   Observed for `a60db816-2efd-425e-b3c6-64f8abf842bf`:

   ```json
   {"session_id":"a60db816-2efd-425e-b3c6-64f8abf842bf","agent_id":"agent_138",
    "mode":"headless","spawned_at":"2026-11-08T12:25:12.802Z",
    "ended_at":"2026-11-08T12:25:31.793Z","status":"done",
    "result":{"subtype":"success","is_error":false,"result":"","num_turns":0}}
   ```

3. Wait, then read the stamp file:

   ```bash
   cat ~/.local/state/claude-supervisor/live/a60db816-2efd-425e-b3c6-64f8abf842bf.json
   ```

   Observed:

   ```json
   {"sessionId":"a60db816-2efd-425e-b3c6-64f8abf842bf","pid":null,"mode":"",
    "at":"2026-11-08T19:37:53.756956Z","source":"mcp-timer",
    "location":"local","activity":"idle"}
   ```

4. Confirm the row is a poster write, not a self-stamp: the supervisor's `startSelfStamp` writes `pid: process.pid` and `mode: "local"`, and the worker's own server stands down entirely when `SUPERVISOR_WORKER_MODE=headless`. A row carrying `pid: null` and `mode: ""` can only have been written through the store's `Post`, whose file struct carries `PID *int` and `Mode string` and sets neither.

5. Confirm the poster is the live writer:

   ```bash
   find ~/.local/state/claude-supervisor/live -name '*.json' -mmin -5 | wc -l
   ```

   Measured 22:05: 60 stamps touched in five minutes, of which 20+ carried `pid: null, source: mcp-timer, activity: idle`.

**Expected:** a session with a terminal ledger row receives no heartbeat.
**Actual:** it receives a fresh one every ~30 s, for as long as its pid is alive.

## Expected vs Actual

| | Behavior |
|---|---|
| **Expected** | A session the spawn ledger records as `done` is finished. Its liveness should decay to absent once the supervisor clears the stamp, which is what `heartbeat.mjs` states the clear is for: *"so the common case leaves nothing behind and the next reader does not have to wait out a TTL to learn what the server already knows."* |
| **Actual** | The wrapper's poster re-creates the stamp within one tick, and keeps re-creating it for the life of the child's pid. |

## Why this is a bug

Three documented contracts are broken at once, and each is load-bearing for a different consumer:

- `attention-controller/docs/session-heartbeat.md` defines `source: mcp-timer` as *"the only source that proves a session is posting for itself"*. The poster writes that exact value for a session that is not posting at all, so the store's one trust signal is forgeable by its own central producer.
- `claude-supervisor/server/heartbeat.mjs` states the owner's clear exists so a finished worker stops reading live. The poster defeats that clear by construction.
- `claude-supervisor/agents/goal-drive.md` gates the auto-resume on "no live id on ANY id in the set". A forged live row makes a finished worker permanently unresumable, which is the state the linked vault task records as costing three workers their context in one session.

The direction of the error matters: a stale `LIVE` makes work look running when it is not, so a manager waits, a cap slot is held, and a resume is refused. Skipping on a missing row would invert that into killing live workers — which is why the fix's rule is one-directional and why the missing-row case is an acceptance criterion rather than a comment.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `LedgerRow.Status` + poster consults the ledger and skips terminal sessions, wired in `main.go` | 1, 2, 3, 4, 5 | 1, 5, 6 | — |
| 2 | The three regression locks — table-driven decision rule, unreadable ledger, `Status` decode | 2, 3, 4 | 2, 3, 4 | prompt 1 |

Rationale: prompt 1 establishes the field, the rule and the injection seam across `pkg/ledger.go`, `pkg/heartbeat.go` and `main.go` (wiring only); prompt 2 pins it in `pkg/heartbeat_test.go` and the new `pkg/ledger_test.go`. AC 7 is the operator's post-deploy replay and is not a prompt deliverable. The table-driven spec in prompt 2 is what would catch an inverted rule, so it is not optional polish.
