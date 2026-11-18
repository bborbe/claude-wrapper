---
status: completed
spec: [003-parked-observable-reopen-gate]
summary: Added bounded per-class park probes (card, PR, host, clock) with fingerprints and fail-closed error semantics, plus three ginkgo specs
execution_id: claude-wrapper-parked-observable-exec-002-spec-003-parked-probes
dark-factory-version: v0.198.17
created: "2026-11-17T12:00:00Z"
queued: "2026-11-17T20:40:50Z"
started: "2026-11-17T23:32:19Z"
completed: "2026-11-17T23:35:40Z"
---

# Observe each of the four park classes

<summary>
- Each of the four wait classes gets one way of observing whether its wait has ended.
- The pull-request observation runs the fleet's existing command runner with a fixed argument list.
- That argument list is derived from the declaration, so two different pull requests produce two different commands.
- The card observation reads only the existing open-card list, and an unconfigured store is an error rather than "no card".
- The host observation opens a TCP connection, writes nothing, and is bounded to two seconds.
- The clock observation compares the declared time against the injected clock, never a sleep.
- A probe that cannot be read reports an error instead of guessing, so the caller can hold the row.
- Every probe is bounded, so a hung external call costs freshness and never a stuck tick.
- Nothing here writes a file, posts a card, or answers one.

</summary>

<objective>
Give the park gate one observable per class: the row's session still owning an open card, a pull request's head/review/state, a host's TCP reachability, and a declared clock. Each probe returns a fingerprint the gate can record and compare, plus whether the observable is satisfied on its own. A probe that cannot be read must return an error, because the caller's only safe response is to hold the row.
</objective>

<context>
Read these before writing anything:

- `pkg/parked_closer.go` (written by the previous prompt) — `ParkedClass`, `ParkedDeclaration`, `PullRequestRef`. Note that its classifier already defaults `Port` to 22 when a host declaration names none, so `declaration.Port` is never 0 for a host park.
- `pkg/flagged_rows_source.go` — `CommandRunner` (`Run(ctx, name string, args ...string) ([]byte, error)`) and `ExecCommandRunner`. The PR probe takes a `CommandRunner`; the production runner is `ExecCommandRunner{}`.
- `pkg/attention_post.go` — `OpenCardReader.OpenSessionIDs(ctx) (map[string]struct{}, error)`. An error is a store that could not be read; the caller must fail closed.
- `pkg/flagged_tick.go` — `FlaggedRow`, and `FlaggedTick.now()` for the `libtime.CurrentDateTimeGetter` idiom.
- `pkg/flagged_tick_test.go` — `recordingRunner` (records the argv it was handed), `fixedNow`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-security-linting.md`
</context>

<requirements>
1. Create `pkg/parked_probe.go`, package `pkg`, with the standard BSD header.

2. Declare the bounds as named constants with GoDoc. There are exactly two:

   ```go
   // parkedProbeTimeout bounds one probe that shells out or reads the store.
   const parkedProbeTimeout = 10 * time.Second
   // parkedDialTimeout bounds one TCP dial of a declared host.
   const parkedDialTimeout = 2 * time.Second
   ```

   Do **not** declare a default-port constant: the classifier in `pkg/parked_closer.go` already sets `Port` to 22 when the declaration names none, so a second default here would be a second home for the same rule and would also be an unused symbol (`unused` is enabled, so `make lint` fails on it).

3. Declare the probe contract:

   ```go
   // ParkedObservation is one probe's reading of its observable.
   type ParkedObservation struct {
       // Fingerprint identifies the observable's current state. The gate records
       // it and re-probes on a later tick; a changed fingerprint means the
       // observable moved.
       Fingerprint string
       // Satisfied reports that the observable the closer waits on has happened,
       // independent of any recorded fingerprint.
       Satisfied bool
   }

   // ParkedProbe observes one class's observable.
   //
   // An error means the observable could not be read. The caller holds the row
   // on an error and never opens it.
   type ParkedProbe interface {
       Probe(
           ctx context.Context,
           row FlaggedRow,
           declaration ParkedDeclaration,
       ) (ParkedObservation, error)
   }
   ```

4. `CardProbe` — whether the row's own session still owns an open attention card.

   ```go
   type CardProbe struct {
       Cards OpenCardReader
   }
   ```

   - `Cards == nil` returns an error (`errors.Errorf(ctx, ...)`) naming that the attention store is not configured. A nil reader must never read as "no card".
   - `OpenSessionIDs(ctx)` error is wrapped with `errors.Wrapf` and returned.
   - `Satisfied` is true when the returned set does **not** contain `row.SessionID`; `Fingerprint` is `"open"` or `"answered"`.
   - A row with an empty `SessionID` returns an error: there is no card to look for, and holding on that basis would be a guess.

5. `PRProbe` — the pull request's head, review decision and state.

   ```go
   type PRProbe struct {
       Runner CommandRunner
   }
   ```

   - `Runner == nil` returns an error.
   - Run exactly this argv through `Runner.Run(ctx, "gh", "pr", "view", strconv.Itoa(declaration.PR.Number), "--repo", declaration.PR.Owner+"/"+declaration.PR.Repo, "--json", "headRefOid,reviewDecision,state")`. No shell, no `sh -c`, no interpolation beyond those three separate elements.
   - Bound the call with `context.WithTimeout(ctx, parkedProbeTimeout)`.
   - Decode stdout as a struct with the three JSON fields `headRefOid`, `reviewDecision`, `state`.
   - `Fingerprint` is `headRefOid + "|" + reviewDecision + "|" + state`.
   - `Satisfied` is true when `state` is `"MERGED"` or `"CLOSED"`. Every other state is compared by the gate against the recorded fingerprint.
   - A non-zero runner exit, an undecodable body, or a timeout returns an error. It never returns a satisfied observation.

6. `HostProbe` — whether the declared host accepts a TCP connection.

   ```go
   type HostProbe struct{}
   ```

   - No fields: the dial bound is the package constant `parkedDialTimeout`, and there is no per-class tunable timeout anywhere in this build. Do not add a `DialTimeout` field, and do not add a parameter that would let a caller vary it.
   - Dial `net.JoinHostPort(declaration.Host, strconv.Itoa(declaration.Port))` through a `*net.Dialer` carrying the constant, so `ctx` is honoured and the bound stays a constant:

     ```go
     dialer := &net.Dialer{Timeout: parkedDialTimeout}
     conn, err := dialer.DialContext(ctx, "tcp", address)
     ```

     `net.DialTimeout` takes no `ctx`; do not use it here.
   - Close the connection when the dial succeeds.
   - `Fingerprint` is `"reachable"` or `"unreachable"`; `Satisfied` is true only when reachable.
   - A refused or timed-out dial is **not** an error: an unreachable host is the declared state, and the gate holds on the unsatisfied observation. Return a nil error.
   - An empty `Host` returns an error.

7. `ClockProbe` — whether the declared clock has passed.

   ```go
   type ClockProbe struct {
       Now libtime.CurrentDateTimeGetter
   }
   ```

   - `Now == nil` returns an error.
   - `declaration.Deadline == nil` returns `ParkedObservation{Fingerprint: "unprobeable"}` with a nil error and `Satisfied` false. The one-hour expiry for an unprobeable declaration is the gate's rule, not the probe's.
   - Otherwise `Fingerprint` is the deadline in `time.RFC3339` and `Satisfied` is `!now.Before(*deadline)`, reading `now` from the injected getter (`Now.Now().Time()`), never `time.Now()`.

8. Keep each `Probe` small enough for `funlen` (80 lines / 50 statements) and `gocognit` (20). `dupl` is enabled for non-test code: the four probes must not be copy-paste twins of each other.

9. Write `pkg/parked_probe_test.go` with exactly these three specs, using qualified `ginkgo` / `gomega`. `<verification>` permits no spec beyond this list, so a probe left untested here stays untested.

   - `ginkgo.It("records the pull request command from the declaration", ...)` — run `PRProbe` twice with a `recordingRunner` (from `pkg/flagged_tick_test.go`), first for a declaration carrying `bborbe/claude-wrapper#412`, then for one carrying `bborbe/other-repo#9`. Assert the recorded argv, after the binary name, is exactly `pr view 412 --repo bborbe/claude-wrapper --json headRefOid,reviewDecision,state` and then exactly `pr view 9 --repo bborbe/other-repo --json headRefOid,reviewDecision,state`, and that the binary is `gh`. Print `argv=%s` per case. Then assert that a runner error, and a runner returning a non-zero-shaped failure, both produce an error and a zero-value observation.
   - `ginkgo.It("reports the card observable and fails closed", ...)` — a table spec over `CardProbe` with a fake `OpenCardReader`. Name the fake `parkedProbeCards` in this file so it cannot collide with the card fake a later prompt adds. Cases, each printed as `case=<name> satisfied=<bool> fingerprint=%s err=<bool>`:
     - `nil store` — `Cards: nil` → an error, and the zero observation.
     - `store error` — the fake returns an error → a wrapped error, and the zero observation.
     - `card open` — the fake's set contains the row's session id → `Satisfied` false, `Fingerprint` `"open"`.
     - `card answered` — the fake's set does not contain it → `Satisfied` true, `Fingerprint` `"answered"`.
     - `no session id` — the row's `SessionID` is empty → an error.

     Assert the nil-store and empty-session-id cases are **errors** rather than an `answered` observation: that is the fail-closed contract.
   - `ginkgo.It("dials the declared host and reads the declared clock", ...)` — a real `net.Listener` on `127.0.0.1:0` yields reachable and satisfied; a port taken from a listener that was opened and then closed yields unreachable and not satisfied **with a nil error**; a deadline one hour in the future is not satisfied; a deadline in the past is satisfied. Print `case=<name> reachable=<bool> satisfied=<bool>` per case.

10. Before finishing, re-run `<verification>` and confirm it passes, then walk each numbered requirement against the code you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- **No model call anywhere in this path.**
- **The PR probe's argv is frozen**: `pr view <N> --repo <owner>/<repo> --json headRefOid,reviewDecision,state`, through the existing `CommandRunner`. The three fields are the observable; nothing else is read.
- **The card probe uses the existing `OpenCardReader.OpenSessionIDs`**, keyed by producer id, which for a session's own card is its session id. Do not post, answer or read any card beyond that call.
- **The dial writes no bytes**, is bounded at two seconds, and runs at most once per row per tick (the gate calls one probe per row per tick; do not add a retry loop here).
- A probe that cannot be read returns an error and holds the row; it never returns a satisfied observation.
- Do NOT change `flagged-rows.py`, its eligibility predicate, or the fields it emits.
- Do NOT change the 15-minute tick interval, the 50-row per-tick budget, `recentlySpawnedWindow`, `predicateTimeout`, or any spawn cap.
- Do NOT add a config key or flag, and do NOT add a per-class tunable timeout. `parkedProbeTimeout` and `parkedDialTimeout` are the only bounds this file declares, and both are constants.
- The declaration is **untrusted model output**: the PR reference is three separate argv elements and never a shell, and the host is a validated `host:port` with no bytes written.
- Repo conventions: `github.com/bborbe/errors` wrapping (no `fmt.Errorf`, no bare `return err`), `ctx` threaded through every probe, `libtime.CurrentDateTimeGetter` for the clock, GoDoc on every exported item.
- Tests are in-package ginkgo specs; import `ginkgo`/`gomega` qualified, never dot-imported. Use a real `net.Listener` for the host cases — only the `gh` runner and the open-card reader are fakes.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then run the probe specs and confirm exit 0 plus the printed evidence lines:

```
go test -v -count=1 ./pkg -args -ginkgo.focus='records the pull request command' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='reports the card observable' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='dials the declared host' -ginkgo.v
```
</verification>
