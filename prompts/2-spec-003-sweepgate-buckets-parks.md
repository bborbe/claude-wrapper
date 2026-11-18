---
spec: ["003-sweep-gate-port"]
status: draft
created: "2026-11-18T00:35:03Z"
---

# sweepgate classification: buckets, holds, liveness, parks and waits

<summary>
- Every tracked row gets exactly one status cell and one bucket key, in the same precedence order the vault-side script uses
- A held session's row shows the operator's own reason and how long the hold has stood, and stops producing any instruction
- A deferred row is labelled as deferred without changing its bucket, so the change signal stays stable
- Liveness is read from the session registry, the headless heartbeat store and the attention feed, with the feed winning when it answers
- A worker parked on a gate the feed never registered, or one that declared a machine-watched wait, is classified as waiting rather than as a problem
- A row is called blocked-upstream only when at least one named blocker is not completed, and a blocker outside the tracked set still counts
- Each classification guard that refuses, suppresses or degrades has its own named test, so the refusal list can be derived mechanically later
</summary>

<objective>
Port the classification half of the gate into `pkg/sweepgate`: the bucket/status precedence, the hold and defer overlays, liveness from the three real sources, and the five render-only park/wait flags. This is what turns a tracked set into the cells a frame renders; it renders nothing itself.
</objective>

<context>
Read `pkg/sweepgate/config.go`, `pkg/sweepgate/task.go`, `pkg/sweepgate/snapshot.go`, `pkg/sweepgate/digest.go` and `pkg/sweepgate/state.go` first — the previous prompt created them and this prompt extends the package. Also read `pkg/sweepgate/*_test.go` for the suite shape.

Read the reference at `testdata/sweepgate/reference/sweep-gate.py`. The functions you are porting are `bucket`, `_classify`, `_is_waiting`, `read_holds`, `held_reason`, `held_age`, `held_label`, `is_held`, `is_deferred`, `_parse_defer_date`, `open_box_count`, `idle_stuck`, `apply_stuck`, `blocker_statuses`, `apply_blocked_upstream`, `liveness_of`, `liveness_of_set`, `enrich_liveness`, `heartbeat_live`, `read_registry`, `read_feed`, `is_open_gate`, `is_external_wait`, `apply_external_waits`, `apply_declared_waits`, `park_threshold_seconds`, `parked_on_unregistered_gate`, `session_in_flight`, `apply_unregistered_parks`, and the constant blocks `BUCKETS`, `GLYPHS`, `BUCKET_ORDER`, `DEFER_OVERLAY_EXEMPT`, `NON_SPAWNABLE_ROLES`, `LIVENESS_*`, `PARKED_VERBS`, `EXTERNAL_WAIT_MARKER`, `KIND_FROM_MECHANISM`, `_YAML_NULLS`, `STUCK_SECONDS`, `LIVE_WINDOW`, `STUCK_MARKER_SECONDS`, `HEARTBEAT_TTL_SECONDS`.

Repo patterns to follow:
- `pkg/flagged_tick.go` — `decision` is a pure function returning `(reason, refused, skipped)`; `orderFlaggedRows` is a stable sort. Classification here is the same shape: pure functions over a row, named branches, one reason string per branch.
- `pkg/subject_state.go` — reading the registry/worker-manager JSON from a directory with `os.ReadFile` + `json.Unmarshal`, and the rule that a malformed record is "no record", never an error.
- `pkg/attention_read.go` — `readJSONArray`: only a 200 is a read, and a decode failure is "could not read it", never an empty result.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
</context>

<requirements>
1. **Extend `Task` with the render-only fields** (do not change the fields the previous prompt defined): `Liveness string`, `Stuck bool`, `InFlight bool`, `ExternalWait bool`, `DeclaredWaitActive bool`, `UnregisteredPark bool`, `FeedGate bool`, `BlockedUpstream bool`, `BlockedByUnmet []string`, `BusySince map[string]BusyStamp` is NOT on the task — the busy-since map is cross-tick state returned by `ApplyStuck`, defined in requirement 5.

2. **`pkg/sweepgate/bucket.go` — the status cell and the bucket key.**
   - `const (LivenessLive = "live"; LivenessParked = "parked"; LivenessNone = "none")`.
   - `var Glyphs map[string]string` and `var BucketOrder []string` with exactly the reference's keys, glyphs and order. `BucketOrder` must contain `upstream` and `declared` — a key present in `Glyphs` but absent from `BucketOrder` is counted and then silently dropped from the marker, which is the reference's own recorded defect.
   - `func Bucket(t Task) (label string, key string)`:
     - `status` in `{completed, aborted, hold, backlog}` returns the `BUCKETS` label/key pairs: `✅ done`/`done`, `✅ done · aborted`/`done`, `⏸️ blocked/hold`/`hold`, `—`/`backlog`.
     - the hold overlay applies FIRST and unconditionally (`HeldLabel(t.Session)` non-nil → that label, ordinary key), because a hold is an operator statement about the session and must read as held whatever else the row says.
     - the deferred overlay applies when `key` is not in `{done, hold, backlog, waiting}` and `IsDeferred(t)` — label `⏸️ deferred`, key unchanged.
     - `Classify(t)` is the ordinary precedence, in this exact order: `phase: human_review` → `⌛ waiting-on-human`/`waiting`; `liveness == parked` → `waiting`; `phase: todo` and not `OperatorFlagged` → `⌛ waiting-approval`/`approval`; `ExternalWait` → `⌛ waiting-external`/`external`; `InFlight` → `🔄 progressing`/`progressing`; `DeclaredWaitActive` → `⌛ waiting-declared`/`declared`; `UnregisteredPark` → `⌛ parked-on-unregistered-gate`/`unregistered`; role in `{human, manager}` AND not `BlockedUpstream` AND `liveness != live` → `👤 YOURS`/`yours`; `Stuck` and not `IsWaiting(t)` → `⚠️ problem`/`problem`; `status == next` or (`status == in_progress` and `phase == planning`) → live owner `progressing`, blocked-upstream `⏸️ blocked/upstream`/`upstream`, else `🚀 ready-to-start`/`ready`; `status == in_progress` → `progressing`; otherwise `—`/`other`.
     - The order is the contract. Do not reorder to "tidy" it: each placement has a recorded false positive behind it (an in-flight row must beat the park cell; the park cell must beat `stuck`; the role carve-out must beat `stuck` and the offer). Keep those reasons as comments on the branches, shortened to one line each.
   - `func StatusLabel(t Task) string` — the label for a bare status, built by calling `Bucket` on a neutral row (`phase`, `session` empty, `LivenessNone`, `Stuck` false), never a second status→glyph map.
   - `func IsDeferred(t Task) bool` — parse `DeferDate` as a bare date (`YYYY-MM-DD` first, then RFC3339), compare against `cfg.Now`'s date in local time; unparseable or absent → false.
   - `func IsWaiting(t Task) bool` — any of: parked liveness, `BlockedUpstream`, deferred, `DeclaredWaitActive`.

3. **`pkg/sweepgate/holds.go` — the operator's hold overlay.**
   - `type Hold struct { Reason string \`json:"reason"\`; HeldAt string \`json:"held_at"\` }`, `type HoldStore struct { Holds map[string]Hold \`json:"holds"\` }`.
   - `func ReadHolds(ctx context.Context, path string) (map[string]Hold, error)` — a missing, unreadable or malformed store returns an empty map and a NIL error. A hold is an operator instruction to STOP, so inventing one from an unreadable file would suppress wakes for sessions the operator never held; reading a real store as empty merely fails to honour a hold, loudly, on the next tick.
   - `func HeldReason(holds map[string]Hold, sessionID string) (string, bool)` — empty id → false; a non-object entry → false; the reason defaults to `held`.
   - `func HeldAge(now time.Time, heldAt string) string` — `%dm` under an hour, `%dh` under a day, `%dd` otherwise, `?` for an absent or unparseable stamp (never age zero, which would read as a hold set seconds ago). Parse the stamp as `%Y-%m-%dT%H:%M:%SZ` in UTC.
   - `func HeldLabel(now time.Time, holds map[string]Hold, sessionID string) (string, bool)` — `⏸️ HELD — <reason> · <age>`, exactly.
   - The whole store is read once per tick and passed down; do not re-read the file per row. (The reference re-reads it per row; that is a cost, not a contract, and one read per tick cannot change an answer.)

4. **`pkg/sweepgate/liveness.go` — three sources, one verdict.**
   - `type RegistryRecord struct { PID int; Status string; Name string; Alive bool }`; `func ReadRegistry(ctx context.Context, dir string) (map[string]RegistryRecord, error)` — one `*.json` per pid; `sessionId` absent → skip; `alive` is `os.FindProcess`+`Signal(syscall.Signal(0))`-style probing (`syscall.Kill(pid, 0)` on unix; a `PermissionError` equivalent — `EPERM` — counts as alive). Put the signal probe behind a small function with a `//go:build` split if the package must compile on non-unix; this repo's `pkg/procexit_darwin.go` / `pkg/procexit_other.go` is the precedent for that split.
   - `func HeartbeatLive(ctx context.Context, dir, sessionID string, now time.Time) (live bool, readable bool)` — the verdict is the stamp's AGE against a 60-second TTL, never the file's existence; a missing file is `(false, true)`, any other stat error is `(false, false)`. `false`/`false` means "could not read", never "not live".
   - `type FeedRecord struct { SessionID, ItemID, Kind, State, Event, Detail, Mechanism string; Replayed bool }` plus the fields the store returns; `func ReadFeed(ctx, cfg Config, now time.Time) (map[string]FeedRecord, error)` — the reference's three sources in its exact order: the attention store (primary, WINS when it answers), then `<sid>.events.jsonl` (the fallback, every record stamped `Replayed`), then the legacy `<sid>.needs.json` merged LAST and only for sessions the primary did not cover. The store read is an HTTP GET against `cfg.StoreURL`; only a 200 is a read, and an unreachable store is NOT an error — it falls through to the event log. Fold the event log as: an item is open when its `item_id` has an `open` line and no matching `close` line; a malformed line is skipped; only bytes up to the last newline are consumed. `_keep` collapses to ONE record per session, preferring a raised gate over any other item.
   - `func IsOpenGate(rec FeedRecord) bool` — `state == answered` → false; `kind` not in `{permission, question}` → false; `event == Stop` → false (a rendered closer panel is not a gate); otherwise `true` unless the normalised `detail` starts with a `PARKED_VERBS` entry (`later (on `). Normalise `&nbsp;`-family entities and collapse whitespace before that test.
   - `func IsExternalWait(rec FeedRecord) bool` — `event == Stop` AND `detail` contains `⏰ Ends:`. A record without `event` keeps the old gate behaviour.
   - `func LivenessOf(sid string, registry map[string]RegistryRecord, feed map[string]FeedRecord, heartbeat func(string) (bool, bool)) string` — no id → `none`; a registry record that is alive → `parked` when the feed holds an open gate for it OR its status is `waiting`, else `live`; no live registry record → the heartbeat decides: not live → `none`, live → `parked` when the feed holds an open gate, else `live`. Pass the heartbeat verdict in rather than re-statting per task; a distinct sentinel (e.g. a `nil` func) means "read it for me".
   - `func LivenessOfSet(ids []string, ...) string` — precedence `live` > `parked` > `none`, so a dead `claude_session_id` cannot mask a live `metrics_sessions` id.
   - `func EnrichLiveness(ctx, cfg Config, tasks []Task, registry, feed, heartbeat) ` — sets `Task.Liveness` from the whole id set (falling back to the single `Session` when `SessionIDs` is empty).
   - `func SessionInFlight(ctx, cfg Config, sid string, registry map[string]RegistryRecord, now time.Time) bool` — three limbs in order: registry `status == shell` → true, `status == waiting` → false; then `<sid>.tool.json` with `state == open` AND age `< 20m` → true (an older open marker falls through); then the transcript `<projectsDir>/<sid>.jsonl` or `<projectsDir>/*/<sid>.jsonl`, minimum age `< 5m` → true; no transcript → false. `false` means "not proven in flight", never "dead".

5. **`pkg/sweepgate/parks.go` — stuck, blocked, and the four wait flags.**
   - `func OpenBoxCount(met string) int` — `n/m` → `m-n`; no `/` → 0.
   - `func IdleStuck(t Task, now time.Time) bool` — four gates, all required: not parked, `phase == execution`, at least one open box, and `now - Mtime >= 30m`.
   - `func ApplyStuck(tasks []Task, registry map[string]RegistryRecord, prev map[string]BusyStamp, now time.Time) map[string]BusyStamp` — reuse the `BusyStamp` defined in `state.go` by the previous prompt. Both branches: a continuously `busy` live session with an unchanged `ProgressHash` for `>= 30m` sets `Stuck` (the clock is observed across ticks, so `prev` is the state file's `busy_since`); a Progress write resets the clock and clears `Stuck`; a non-busy row takes `IdleStuck`. The branches are disjoint.
   - `func BlockerStatuses(ctx, vault string) (map[string]string, error)` — every `25 Tasks/*.md` file's `status`, keyed by task name, VAULT-WIDE (a blocker may sit outside the tracked set).
   - `func ApplyBlockedUpstream(tasks []Task, statuses map[string]string)` — `BlockedByUnmet` = every named blocker whose status is not `completed` (missing, unreadable, `aborted` and status-less all count as unmet); `BlockedUpstream` is true only when the list is non-empty. An empty `blocked_by` must not make every row unspawnable.
   - `func ParkThreshold(cfg Config, branch, subject string, now time.Time) (seconds int, basis string, armed bool)` — read `<branch>--<slug>.cadence`; a positive integer interval → `2*interval`, basis `2 x cadence <n>s (armed) from <path>`, armed true; otherwise `1800`, basis `no cadence record at <path>; fixed 1800s`, armed false. The basis is ALWAYS returned and the caller always prints it — a silent default is the defect this exists to remove.
   - `func ParkedOnUnregisteredGate(t Task, now time.Time, parkSeconds int, registry map[string]RegistryRecord) bool` — four-way discriminator in order: terminal status → false; `phase != execution` → false; fewer than one open box → false; no mtime → false; `now - Mtime < parkSeconds` → false (working); `FeedGate` → false (a registered gate is the report); `Liveness != live` → false (orphan, not a park); `InFlight` → false. All eight conditions are required.
   - `func ApplyUnregisteredParks(ctx, cfg Config, tasks []Task, feed map[string]FeedRecord, registry map[string]RegistryRecord, now time.Time, parkSeconds int)` — sets `FeedGate` (the feed holds an open gate for the row's current owner) and `UnregisteredPark` per the predicate above. Read `t.Session` only — not the whole id set — so the two feed-derived flags agree about whose record they read.
   - `func ApplyExternalWaits(tasks []Task, feed map[string]FeedRecord)` — `ExternalWait` from `IsExternalWait(feed[t.Session])`, empty session → false.
   - `func ApplyDeclaredWaits(tasks []Task)` — `DeclaredWaitActive` is true only for a non-empty `DeclaredWait` that is not one of `""`, `~`, `null`, `Null`, `NULL` (quotes already stripped by the parser).
   - Every one of the five flags is RENDER-ONLY: none is an input to `Digest`. Add a comment saying so, because the reference's own comment records that adding one would break lockstep with the plugin's port.

6. **`pkg/sweepgate/prepare.go` — the one pipeline both the tick and the invariant run.**
   ```go
   // Prepared is one subject, fully enriched, ready to digest, render or inspect.
   type Prepared struct {
       Snapshot Snapshot
       Tasks    []Task
       Busy     map[string]BusyStamp
       Digest   string
   }
   func Prepare(ctx context.Context, cfg Config) (Prepared, error)
   ```
   It runs, in the reference's order: `ResolveSubject` → `ResolveTracked` → `ReadRegistry` → `ReadFeed` → `EnrichLiveness` → `ParkThreshold` → `ApplyUnregisteredParks` → `ApplyExternalWaits` → `ApplyDeclaredWaits` → `ApplyStuck` (seeded from the state file's `busy_since`) → `BlockerStatuses` + `ApplyBlockedUpstream` → `Digest`. The order is load-bearing: the digest is built from what the frame renders, so a flag resolved after the digest would move the table without moving the change signal; and `ApplyBlockedUpstream` runs AFTER `ApplyStuck` exactly as the reference does, with `IsWaiting` doing the suppression. Return the `ParkThreshold` basis to the caller (add a `ParkBasis string` and `ParkSeconds int` field) — the tick mode prints it on every tick.
   Bound the vault reads (`ResolveSubject`, `ResolveTracked`, `BlockerStatuses`) with `cfg.ReadTimeout` via `context.WithTimeout`, the way `FlaggedTick.readRows` bounds a predicate read. The bound exists to abandon a HUNG read, never a slow one, so it is deliberately larger than the tick interval; a read that exceeds it returns an error that names the timeout, and the caller logs it and continues with the next subject rather than stopping the loop.
   A subject that cannot resolve is an error; the caller decides the exit code. Every other read failure degrades to its safe default, never an error.

7. **The refusal tests (AC3's test half).** Every guard that refuses, suppresses, degrades or falls back gets a named test. Use the naming convention `It("refuses <name>", ...)` for a guard that declines an action and `It("degrades <name>", ...)` for one that reads an unreadable input as a safe default — the documentation prompt will cite these exact strings, so the names are part of the contract. Minimum set for this prompt:
   - `refuses a non-live owner the ready offer`
   - `refuses a spawn offer to a non-spawnable role`
   - `refuses the problem cell to a waiting row`
   - `refuses the deferred overlay to a definitive disposition`
   - `refuses an action to a held row`
   - `refuses a spawn offer to an unapproved row`
   - `refuses the park cell to a registered gate`
   - `refuses the park cell to an ownerless row`
   - `refuses the park cell to an in-flight row`
   - `refuses a parked row the stuck flag`
   - `refuses blocked-upstream to a row whose blockers all completed`
   - `degrades an unreadable hold store to nothing held`
   - `degrades an unreadable registry record to no record`
   - `degrades an unreadable heartbeat store to not live`
   - `degrades an unreadable feed to the event-log fallback`
   - `degrades an unreachable store to a replayed feed`
   - `degrades a malformed feed line to a skipped line`
   - `degrades a malformed JSON record to no record`
   Each test drives the real function with a real fixture file in `ginkgo.GinkgoT().TempDir()` or the checked-in `testdata/sweepgate/inputs/`, and asserts the specific outcome (the label, the key, or the flag). Do not assert on an internal call count.

8. **Tests for the classification contract.** In addition to the refusal set:
   - `Describe("classifies every bucket in the reference order", ...)` — one case per branch of `Classify`, each asserting BOTH the label and the key, including `aborted` → `done` with the `· aborted` suffix, `waiting-approval` for an unflagged `todo`, and `YOURS` for a sessionless `role: human` row at `phase: execution`.
   - `Describe("renders the held overlay with reason and age", ...)` — exact-equality on `⏸️ HELD — <reason> · <age>` for the three age bands and for an unreadable stamp (`?`); an unheld control row.
   - `Describe("keeps the digest blind to the render-only flags", ...)` — set every render-only flag on a task, assert `Digest` is unchanged.

9. **`CHANGELOG.md`.** Add one bullet under the existing `## Unreleased` heading:
   `- feat: pkg/sweepgate classifies sweep rows — bucket precedence, hold and defer overlays, three-source liveness, and the park/wait flags, with every refusal carried as a named test`
   Do not create a second `## Unreleased` heading.

10. **Self-check before finishing.** Re-run `<verification>` and confirm it passes. Walk requirements 1-9. Confirm no render-only flag reaches `Digest`, and confirm every branch of `Classify` appears in `BucketOrder`'s key space (`Glyphs` has an entry for each key the classifier can return).
</requirements>

<constraints>
- The classification ORDER is the contract, not a style choice: each placement has a recorded false positive behind it. Keep the one-line reason as a comment on each branch.
- The hold store, the registry, the heartbeat store, the feed and every parsed JSON record are UNTRUSTED input: a malformed record reads as "no record" and never as a fatal error. A malformed record must never be an error return that stops a tick.
- The attention store and session registry are READ, never written. This prompt adds no producer.
- Render-only flags are not digest inputs. Adding one to `Digest` silently changes what "unchanged" means and breaks lockstep with the plugin's port.
- The `SWEEP_GATE_*` overrides are trusted local config, set by the operator or the fixture; they are not a request surface.
- No hardcoded vault path anywhere (AC7); the vault is `Config.Vault`.
- `github.com/bborbe/errors` only (`errors.Wrap`, `errors.Wrapf`, `errors.Errorf`, `errors.Is`); `ctx` threaded. Do not introduce `fmt.Errorf`.
- The clock is `libtime.CurrentDateTimeGetter` from `Config.Now`; no `time.Now()` outside a test's own fixed clock.
- Ginkgo v2 + Gomega, in-package tests, qualified imports, no counterfeiter mocks.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Additional checks (all container-executable):
- `! grep -q 'Obsidian/Personal' pkg/sweepgate/*.go` — no hardcoded vault path.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='refuses the problem cell to a waiting row' -ginkgo.v` — exits 0.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='degrades an unreadable hold store to nothing held' -ginkgo.v` — exits 0.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='classifies every bucket in the reference order' -ginkgo.v` — exits 0.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='keeps the digest blind to the render-only flags' -ginkgo.v` — exits 0.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
</verification>
