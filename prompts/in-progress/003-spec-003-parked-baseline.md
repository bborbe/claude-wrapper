---
status: approved
spec: [003-parked-observable-reopen-gate]
created: "2026-11-17T12:00:00Z"
queued: "2026-11-17T20:40:51Z"
---

# Record each park's baseline and decide when it is satisfied

<summary>
- Each row's first sighting of a park is recorded on disk, tied to the session and to the exact words of the declaration.
- A later tick compares the observable against the recorded fingerprint and opens the row only when it moved or is satisfied.
- A park on something this build cannot observe expires after one hour instead of pinning the row forever.
- A baseline file name is a hash of the row key, so a key containing path separators cannot escape its directory.
- A missing or corrupt baseline costs exactly one tick and self-heals.
- A probe that cannot be read holds the row, with the class and the cause in one skip reason.
- The gate resolves the daemon's state directories through the same helper the session store uses, so the two can never disagree about where a session's events live.
- A failed baseline write is logged and never fails the tick.
- The gate answers one question — hold this row, or let it open — and does nothing else.

</summary>

<objective>
Turn the classifier and the four probes into the single decision the reopen tick consults: does this ABSENT row's last session sit on an observable that has not yet happened? The answer must be recorded durably, keyed so a resumed session that parks on a new declaration starts a new baseline, and bounded so an unprobeable declaration expires after one hour rather than holding a row forever.
</objective>

<context>
Read these before writing anything:

- `pkg/parked_closer.go` (prompt 1) — `LastCloser`, `ClassifyParked`, `ParkedClass`, `ParkedDeclaration`.
- `pkg/parked_probe.go` (prompt 2) — `CardProbe`, `PRProbe`, `HostProbe`, `ClockProbe`, `ParkedObservation`. `HostProbe` has no fields: its dial bound is the package constant `parkedDialTimeout`, so the gate builds it as `HostProbe{}`.
- `pkg/sessionstore.go` — `sessionEventsName`, `StateDirEnv`, and `sessionStore.sessionsRoot(ctx)`, which resolves `dir` → `$CLAUDE_WRAPPER_STATE_DIR/sessions` → `~/.claude/state/claude-wrapper/sessions`. `StateDirs` must reuse that resolution, not re-implement it.
- `pkg/supervisor.go` — `uuidPattern`, the session-id shape.
- `pkg/flagged_rows_source.go` — `FlaggedRow` and `FlaggedRow.Key()` (a session id when there is one, else `<vault>/<kind>/<name>`).
- `pkg/flagged_tick.go` — the `now()` idiom over `libtime.CurrentDateTimeGetter`, and `FlaggedTick.logf` for the nil-logger fallback.
- `pkg/flagged_tick_test.go` — `fixedNow(at)`. Note its return type: `libtime.CurrentDateTimeGetter` exposes only `Now()`. `SetNow` lives on `libtime.CurrentDateTime`, the type `libtime.NewCurrentDateTime()` returns, so a spec that must advance the clock keeps that concrete value rather than the value `fixedNow` returns.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. Create `pkg/parked_baseline.go` with the standard BSD header.

2. Declare the expiry bound exactly like this, name included:

   ```go
   // MAX_AGE_S is how long a park on an unprobeable observable may last before
   // the row is reopened anyway.
   //
   // It mirrors claude-supervisor/scripts/declared-wait.py's rule for a
   // declaration naming no future clock: one extra hour, never an indefinite pin.
   // It is a package constant, not a config key, and there is no per-class
   // variant of it.
   const MAX_AGE_S = 3600 * time.Second //nolint:revive // the name is frozen: the spec's mutation check and docs/parked-waits.md both use it
   ```

   The trailing `//nolint:revive` is deliberate: `MAX_AGE_S` is not Go casing, and `revive`'s var-naming rule would otherwise fail `make lint` on the frozen name.

   The file path is load-bearing: the spec's mutation check edits this file and restores it, so the constant must live here and nowhere else.

3. In the same file, declare the baseline and its store:

   ```go
   // ParkedBaseline is one row's recorded first sighting of a park.
   type ParkedBaseline struct {
       // Class is the wait class the declaration carried when it was recorded.
       Class ParkedClass `json:"class"`
       // Fingerprint is the observable's reading at first sighting.
       Fingerprint string `json:"fingerprint"`
       // FirstSeen is when this park was first recorded, on the daemon's clock.
       FirstSeen time.Time `json:"first_seen"`
       // SessionID is the session whose closer declared the park.
       SessionID string `json:"session_id"`
       // CloserHash is the sha256 hex of the closer text, so a resumed session
       // that parks again on different words starts a new baseline.
       CloserHash string `json:"closer_hash"`
   }

   // ParkedStore reads and writes one baseline per row.
   type ParkedStore interface {
       // Load returns the row's recorded baseline, or nil when none is readable.
       // A missing or corrupt file is nil, never an error: a stale baseline costs
       // one tick and self-heals.
       Load(ctx context.Context, key string) *ParkedBaseline
       // Save writes the row's baseline.
       Save(ctx context.Context, key string, baseline ParkedBaseline) error
   }

   // NewParkedStore creates a store rooted at dir.
   func NewParkedStore(dir string) ParkedStore
   ```

4. `parkedBaselinePath(dir, key string) string` returns `filepath.Join(dir, hex.EncodeToString(sum[:])+".json")` where `sum` is `sha256.Sum256([]byte(key))`. Hashing the key is what keeps a key containing `..` or `/` inside the parked directory.

5. `Save` creates `dir` with `0o700` when missing, then writes the JSON through a temp file in the same directory followed by a rename, so a crash cannot leave a half-written baseline that reads as corrupt on every later tick. Return a wrapped error on failure. `Load` returns nil for `os.IsNotExist` and nil for an undecodable body.

6. In the same file, add the state-directory resolution:

   ```go
   // StateDirs resolves the sessions directory and its parked/ sibling from the
   // --state-dir flag value.
   //
   // An empty value follows the same chain NewSessionStore follows, so the two
   // can never disagree about where the daemon's state lives.
   func StateDirs(stateDir string) (sessions string, parked string, err error)
   ```

   Implement it by extracting the existing resolution in `sessionStore.sessionsRoot` into one shared helper and calling that helper from both places. Do not duplicate the env-var / home-dir chain.

   - In `pkg/sessionstore.go`, extract the body into a package-level helper and reduce `sessionsRoot` to a one-line call to it:

     ```go
     // resolveSessionsRoot resolves the sessions directory from dir, then
     // $CLAUDE_WRAPPER_STATE_DIR/sessions, then
     // ~/.claude/state/claude-wrapper/sessions.
     func resolveSessionsRoot(ctx context.Context, dir string) (string, error)
     ```

     `sessionStore.sessionsRoot` becomes `return resolveSessionsRoot(ctx, s.root)`. Behaviour, error wrapping and the returned error string are unchanged.
   - `StateDirs(stateDir)` calls `resolveSessionsRoot(context.Background(), stateDir)` and returns `sessions` plus `parked = filepath.Join(filepath.Dir(sessions), "parked")`. Wrap a resolution error and return it.

7. Create `pkg/parked_gate.go` with the standard BSD header and the decision:

   ```go
   // ParkedHold decides whether a row's last session parked on an observable that
   // is not yet satisfied.
   //
   // A nil ParkedHold on the tick means the gate is inactive.
   type ParkedHold interface {
       // Hold reports whether the row must be held this tick, and the skip reason
       // when it must. The reason is the class token, optionally followed by
       // ": <cause>" when a probe failed.
       Hold(ctx context.Context, row FlaggedRow) (reason string, held bool)
   }

   // NewParkedGate wires the four probes and the baseline store.
   func NewParkedGate(
       sessionsDir string,
       parkedDir string,
       cards OpenCardReader,
       runner CommandRunner,
       now libtime.CurrentDateTimeGetter,
   ) ParkedHold
   ```

   The signature is fixed: a sibling prompt calls it with exactly these five arguments and consumes the returned `ParkedHold`. Do not add a sixth parameter and do not change the return type.

   The concrete gate carries `SessionsDir`, `ParkedDir`, `Store`, `Cards CardProbe`, `PRs PRProbe`, `Hosts HostProbe`, `Clocks ClockProbe`, `Now` and `Logf func(format string, args ...any)`. The constructor — the only construction path; do not build the struct literal anywhere else — populates each field from the argument that feeds it (`CardProbe{Cards: cards}`, `PRProbe{Runner: runner}`, `ClockProbe{Now: now}`, `HostProbe{}`), builds `NewParkedStore(parkedDir)`, and sets `Logf: log.Printf`. `Logf` has no constructor argument, so it must be set here or requirement 8's failure path would call a nil function; set it unconditionally.

   Give the gate a `now()` method mirroring `FlaggedTick.now()`: `Now.Now().Time()` when `Now` is non-nil, `time.Now()` otherwise.

   The gate selects the probe by `declaration.Class` in a switch.

8. `Hold` runs these steps in this order. Decompose into helpers so `Hold` stays inside `funlen` and `nestif` (min-complexity 4):

   1. `row.SessionID` empty or not matching `uuidPattern` → `"", false`.
   2. `closer := LastCloser(filepath.Join(g.SessionsDir, row.SessionID, sessionEventsName))`; empty → `"", false`.
   3. `declaration, parked := ClassifyParked(closer)`; not parked → `"", false`.
   4. Run that class's probe once. An error → return `string(declaration.Class)+": "+err.Error(), true`.
   5. `observation.Satisfied` → `"", false`. The row opens; do not delete the baseline (a stale baseline cannot match a later park, because it is keyed to the session id and the closer hash).
   6. Load the baseline for `row.Key()`; compute `hash` as the sha256 hex of `closer`.
   7. When the baseline exists **and** its `SessionID` equals `row.SessionID` **and** its `CloserHash` equals `hash`:
      - `baseline.Fingerprint != observation.Fingerprint` → `"", false` (the observable moved).
      - `declaration.Class == ParkedUntil && declaration.Deadline == nil && g.now().Sub(baseline.FirstSeen) >= MAX_AGE_S` → `"", false` (the unprobeable park expired).
      - otherwise → `string(declaration.Class), true`.
   8. Otherwise this is a first sighting: `Save` a baseline carrying the class, the observation's fingerprint, `FirstSeen: g.now()`, `row.SessionID` and `hash`, then return `string(declaration.Class), true`. A `Save` error is logged through `g.Logf` (never called on a nil — the constructor always sets it) and does not change the answer: a store write failure must not fail the tick, and the next tick simply records again.

   At most one probe runs per row per tick: no retry, no second class.

9. Write the tests. `<verification>` permits no spec beyond this list, so a branch left out here stays untested. Use qualified `ginkgo` / `gomega`.

   `pkg/parked_baseline_test.go`:

   - `ginkgo.It("resolves the state directories like the session store", ...)` — a table spec over `StateDirs`. Each case prints `case=<name> sessions=%s parked=%s`.
     - `flag value` — `StateDirs(tempDir)` returns `sessions == tempDir` and `parked == filepath.Join(filepath.Dir(tempDir), "parked")`, exactly as `NewSessionStore(tempDir)` would root its sessions.
     - `env fallback` — with `StateDirEnv` set to a temp directory and an empty `stateDir` argument, `StateDirs("")` returns `<temp>/sessions` and `<temp>/parked`. Set the variable and register `ginkgo.DeferCleanup` to unset it, so the spec cannot leak into another one.
     - `agrees with the store` — assert `NewSessionStore("").Dir(<uuid>)` is `filepath.Join(sessions, <uuid>)` for the same environment. That equality is the point: a gate reading a directory sessions are never written to would reopen every parked row forever, which is the defect this spec exists to prevent.

   `pkg/parked_gate_test.go`:

   - `ginkgo.It("expires an unprobeable park after one hour", ...)` — a real temp sessions directory holding a real `events.jsonl` for a UUID-shaped session id whose last assistant text carries `⏰ Ends: task ABC-123 flips to a terminal status on disk`, and a real temp parked directory. Build a **mutable** clock, not `fixedNow`'s return value:

     ```go
     clock := libtime.NewCurrentDateTime()
     clock.SetNow(libtime.DateTime(T0))
     gate := NewParkedGate(sessionsDir, parkedDir, nil, nil, clock)
     ```

     `libtime.CurrentDateTime` embeds `CurrentDateTimeGetter`, so `clock` is accepted where the gate takes a getter and still exposes `SetNow`. Then:
     1. `Hold` at `T0` — this is the first sighting, so it records the baseline and holds. Print `age=0m opened=0`; assert `held` is true.
     2. Advance with `clock.SetNow(libtime.DateTime(T0.Add(59 * time.Minute)))` and `Hold` again — the baseline now exists, so the age rule applies. Print `age=59m opened=0` and **assert `opened == 0`**; this assertion is what makes the spec fail when `MAX_AGE_S` is changed to `0`, which is the mutation the spec's verification applies.
     3. Advance to `T0.Add(61 * time.Minute)` and `Hold` again. Print `age=61m opened=1` and assert `held` is false.

     Print the age as whole minutes (`%dm`), not as a Go duration. The first call cannot be skipped: without it there is no baseline, and an "age 59 minutes" reading has nothing to be 59 minutes old against.
   - `ginkgo.It("keeps a baseline inside its directory", ...)` — `row := FlaggedRow{Vault: "private-personal", Kind: "task", Name: "../../escape"}` (its `Key()` is `private-personal/task/../../escape`), `NewParkedStore(parkedDir)`, `Save` one baseline, then assert the parked directory lists exactly one entry whose name matches `^[0-9a-f]{64}\.json$` and that no file exists outside it. Print `entries=1 name=<hex>.json outside=0`.
   - Add these three cases in the same file, one spec each: a corrupt baseline file is treated as missing and the row is held once; a closer with no `⏰ Ends:` line is not held; a closer whose text changed since the baseline was recorded starts a new baseline and is held.

10. Before finishing, re-run `<verification>` and confirm it passes, then walk each numbered requirement against the code you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- **`MAX_AGE_S = 3600 * time.Second` is a package-level constant in `pkg/parked_baseline.go`**, not a config key, with no per-class variant. The path and the name are frozen.
- **`NewParkedGate` keeps its five parameters and its `ParkedHold` return.** `Logf` is set by the constructor, never passed in.
- **`StateDirs` is the only resolver the daemon uses**, and it must call the same helper `sessionStore.sessionsRoot` calls. Two independent resolutions is the defect this spec exists to remove.
- Do NOT prune baseline files. A stale baseline cannot match a later park, because it is keyed to the session id and the closer's own hash.
- Do NOT expire `parked-on-card`, `parked-on-pr` or `parked-on-host`. Those three wait on something a human or a machine will change; only the unprobeable class expires.
- **One baseline file per row**, named by the sha256 hex of the row key, under the daemon's state directory in a `parked/` sibling of `sessions/`.
- A probe that cannot be read holds the row, with the class as the skip reason and the cause as a second segment of the same entry.
- A `Save` failure is logged and never changes the hold decision.
- Do NOT change `claude-supervisor`'s `scripts/closer-verdict.py`, `scripts/who-needs-me.py` or `scripts/declared-wait.py` — read-only references. `declared-wait.py` states the same one-extra-hour rule this constant implements.
- Do NOT change `flagged-rows.py`, its eligibility predicate, or the fields it emits.
- Do NOT change the 15-minute tick interval, the 50-row per-tick budget, `recentlySpawnedWindow`, `predicateTimeout`, or any spawn cap.
- Do NOT add a config key or flag that disables the gate, and do NOT add a per-class tunable timeout.
- The closer is **untrusted model output**: it is hashed for the baseline key and parsed by prompt 1's classifier, never used as a path segment and never logged verbatim.
- Repo conventions: `github.com/bborbe/errors` wrapping (no `fmt.Errorf`, no bare `return err`), `ctx` threaded through every store and probe call, `libtime.CurrentDateTimeGetter` for the clock, GoDoc on every exported item.
- Tests are in-package ginkgo specs; import `ginkgo`/`gomega` qualified, never dot-imported. Use real temp directories and a real `events.jsonl`; only the card reader and the `gh` runner are fakes.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then run the focused specs and confirm exit 0 plus the printed evidence lines:

```
go test -v -count=1 ./pkg -args -ginkgo.focus='expires an unprobeable park' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='keeps a baseline inside its directory' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='resolves the state directories' -ginkgo.v
```
</verification>
