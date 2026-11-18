---
status: approved
spec: [003-sweep-gate-port]
created: "2026-11-18T00:35:03Z"
queued: "2026-11-18T01:08:39Z"
---

# sweepgate invariant: the open-card payload and its re-arm guard

<summary>
- A subject whose every open row is waiting on the operator, and which has no open card of its own, produces one card payload
- The payload is emitted only when the card actually fires; every other path prints nothing and still exits successfully
- An unreadable subject is a silent success, not a broken install, because the caller reads any non-zero exit as "the script is unavailable"
- A card is withheld when the subject resolves no owner, when the board already carries one for it, and when the ask was already posted and its row set has not changed
- The re-arm guard is a row-set identity, so a row joining, leaving or changing state re-posts while a mere reorder does not
- The exit code is one code per command: this mode never invents a third status
</summary>

<objective>
Port the covered-subject invariant into `pkg/sweepgate` and make it executable as the invariant mode: the open-card read, the attribution rules, the blocked-on-operator predicate, the settled-ask re-arm guard, the payload builder and the always-zero exit contract. This is the prompt that satisfies AC6 and the caller contract the plugin's step file depends on.
</objective>

<context>
Read the package as it stands: `pkg/sweepgate/config.go`, `task.go`, `snapshot.go`, `state.go`, `bucket.go`, `holds.go`, `liveness.go`, `parks.go`, `prepare.go`, `tick.go`.

Read the reference at `testdata/sweepgate/reference/sweep-gate.py`. The functions you are porting are `open_cards`, `manager_subject`, `cards_for_subject`, `subject_has_manager`, `cached_verdicts`, `subject_blocked_on_operator`, `row_set_key`, `invariant_card`, `load_settled`, `save_settled`, `write_card`, plus the constants `OPERATOR_BUCKETS` (`{waiting, approval}`), `OPERATOR_VERDICTS` (`{needs-you, below-bar}`) and `WORKER_MANAGER_DIR` / `PREDISPATCH_DIR`.

The caller contract is stated in the reference's own comments and is the whole reason this mode never returns a non-zero code: the plugin step that invokes it treats exit `3` as "another layer already holds it" and ANY other non-zero exit as "the vault-side script is unavailable", reported once per round. That is why this mode returns 0 on every path — and why the port must never invent a 3. The step file itself is not in the container mount; the contract above is what you implement against, and `sweep-gate.py:3214-3224` records the measured cost of getting it wrong (1487 + 851 + 239 fail-open ledger rows reporting a working install as broken).

Two shape facts to carry over:
- `bucket` applies the hold overlay, so a row's KEY is the hold-independent classification while its LABEL may be `⏸️ HELD — …`. Read keys through `Bucket(now, prepared.Holds, t)`; read the hold overlay through `HeldLabel(now, prepared.Holds, t.Session)`. Neither is available from a bare `[]Task`.
- The reference's `main()` computes the card from the SAME enriched `tracked` list the frame is rendered from, after every `apply_*`. This prompt therefore takes `Prepared`, not a fresh read.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
</context>

<requirements>
1. **`pkg/sweepgate/card.go` — the open-card read and attribution.**
   - `func OpenCards(ctx context.Context, cfg Config) ([]FeedRecord, error)` — the same three sources in the same order as `ReadFeed`, with NOTHING collapsed: the store when it answers, then `<sid>.events.jsonl` (stamped `Replayed`), then legacy `<sid>.needs.json` appended for sessions the primary did not cover, each such record given `ItemID = "needs:<sid>"` (the prefix keeps it from colliding with a real item id and from matching a subject slug in the attribution test). Filter the result through `IsOpenGate`. A subject holding three open cards must count as three; a subject whose card is not its session's preferred item must still count.
   - `func ManagerSubject(ctx context.Context, cfg Config, producerSessionID string) (string, bool)` — read `<WorkerManagerDir>/<sid>.json` and return its `subject`. Absent, unreadable or malformed → `("", false)`, never a guess: a wrong attribution either suppresses a card that is owed or raises one that is not.
   - `func CardsForSubject(ctx context.Context, cfg Config, subject string, cards []FeedRecord) []FeedRecord` — both signals, in order: (1) the card's producer session is registered as this subject's manager; (2) the card's `ItemID` carries this subject's slug as one of its `:`-separated segments. Both are needed; either alone fails one of the reference's three pinned cases.
   - `func SubjectHasManager(ctx context.Context, cfg Config, subject string) bool` — walk the worker-manager directory for any registration naming this subject. Absent or unreadable directory → false (emit only when an owner exists). Declared ownership counts; do not require a live session.
   - `func CachedVerdicts(ctx context.Context, cfg Config, subject string) (map[string]string, error)` — read `<PredispatchDir>/<slug>.verdicts.json` as `{task name: {verdict: "..."}}`; an absent or unreadable cache yields an empty map and a nil error (a missing cache must never turn every row into "blocked on the operator").

2. **`pkg/sweepgate/invariant.go` — the predicate, the identity, the payload.**
   - `func SubjectBlockedOnOperator(ctx context.Context, cfg Config, prepared Prepared, subject string) bool` — over the non-terminal rows only (`Bucket(now, prepared.Holds, t)` key != `done`, with `now := cfg.Now.Now().Time()` read once), EVERY row must qualify by one of: its bucket key is `waiting` or `approval`; it carries the hold overlay (`HeldLabel(now, prepared.Holds, t.Session)` found — checked on the label, because a hold keeps its ordinary key); or its cached verdict is `needs-you` or `below-bar`. No non-terminal rows → false. Any row that is none of those → false. That last clause is the control that stops the invariant firing on every subject with nothing in flight. Take `Prepared` rather than `[]Task`: the hold store and the clock both come from it, and a bare task list cannot answer either clause.
   - `func RowSetKey(ctx context.Context, cfg Config, prepared Prepared, subject string) string` — the identity of the ask: for every non-terminal row, the tuple `[name, bucket key, held, verdict]`, the list sorted, marshalled as compact JSON (`encoding/json` with `json.Marshal` over `[][]any` is acceptable — the exact byte layout need only be stable within this package), sha256 hex truncated to 16 characters. Order-free, so a reorder is not a change; a row joining, leaving or changing bucket/held/verdict IS a change. Set-change, never a tick count. It takes `Prepared` for the same reason as the predicate.
   - `func InvariantPayload(ctx context.Context, cfg Config, prepared Prepared, stderr io.Writer) ([]byte, bool, error)` — the conjunction, in this order, each early return writing its stderr line to the `stderr` writer it was given:
     1. `CardsForSubject` non-empty → `(nil, false, nil)`.
     2. `SubjectHasManager` false → write `skip <subject>: resolves no owner — no manager registration names it` to `stderr`, return `(nil, false, nil)`.
     3. `SubjectBlockedOnOperator` false → `(nil, false, nil)`.
     4. `LoadSettled` record's `RowKey` equals `RowSetKey` → write `suppress <subject>: settled ask unchanged — already answered` to `stderr`, return `(nil, false, nil)`.
     5. otherwise build the card, `SaveSettled`, and return the marshalled bytes with `true`.
     The card's fields, exactly: `dedup_key` = `open-card-invariant:<slug>`; `payload` = `<subject> is blocked on you, and the board carries no card for it.`; `context` = `All <n> non-terminal row(s) of <subject> are waiting on the operator, and the attention board holds no open card attributable to it — so the subject reports itself operator-blocked with nothing for the operator to answer.`; `options` = `["Look at the blocked rows now", "Leave it — I know about this"]`; `recommend` = `Look at the blocked rows now`.
     `stderr` is a REQUIRED parameter — the function never writes to `os.Stderr` itself. The mode passes the process stderr; the tick's card builder passes a `cfg.Logf`-backed writer, because a daemon tick's stdout belongs to nobody.
   - The payload is compact single-line JSON. The Python emits `json.dumps`'s default spacing; a compact encoding is a whitespace-only difference that any JSON parser normalises, and the caller posts the payload rather than diffing it. Record that divergence in `docs/sweep-gate.md` when the documentation prompt runs.

3. **`pkg/sweepgate/mode_invariant.go` — the mode.**
   ```go
   // Invariant prints the card payload when the invariant fires, and nothing otherwise.
   // It returns 0 on EVERY path.
   func Invariant(ctx context.Context, cfg Config, stdout, stderr io.Writer) int
   ```
   - Unresolvable or unreadable subject (`Prepare` returns an error): `fail-open: subject unreadable/unparseable: <err>` to stderr, NOTHING on stdout, return `ExitNoChange` (0). The honest answer for a subject that cannot be shown to be operator-blocked is "nothing", said with the no-fire code; a non-zero exit makes every retired topic read to the caller as a broken install.
   - After a successful `Prepare`, write `park: <prepared.ParkBasis>` to stderr BEFORE deciding anything — every mode announces the threshold basis, armed or not, because a correct run that prints nothing is uncitable. stderr is the right channel: stdout belongs to the caller.
   - Call `InvariantPayload(ctx, cfg, prepared, stderr)` and pass the SAME `stderr` writer the mode was given, so the `skip` / `suppress` lines land on the caller's stderr rather than the process's.
   - Payload fires: the payload plus one trailing newline on stdout, return 0.
   - Nothing fires: empty stdout, return 0.
   - Never return `3`. That code belongs to the consumer's own claim command.

4. **AC6 test.** `Describe("prints the payload only when the card fires", ...)` must appear verbatim — the spec's verification greps for it. Cases, each printing via `ginkgo.GinkgoWriter.Printf` exactly `case=%s stdout_bytes=%d exit=%d`:
   - `fires` — every non-terminal row waiting on the operator, an owner registered, no card on the board, no settled record → payload on stdout, exit 0.
   - `unreadable` — a subject with no page in `24 Goals/` or `23 Topics/` → 0 stdout bytes, exit 0.
   - `owned-by-another` — the board holds a card for ANOTHER subject → payload fires (the count is over attributable cards, not all cards).
   - `already-open` — the board holds this subject's own card → 0 stdout bytes, exit 0.
   - `no-owner` — no manager registration names the subject → 0 stdout bytes, exit 0.
   - `settled` — the settled record's row key matches the current row set → 0 stdout bytes, exit 0.
   - `row-set-changed` — the settled record exists but a row changed bucket → payload fires (the positive control for the re-arm guard).
   Assert `exit == 0` on every case, and assert the payload parses as JSON with the five keys and the two options.

5. **The refusal tests (AC3's test half, continued).** Same naming convention as the previous prompt:
   - `refuses a card to a subject with no owner`
   - `refuses a card while the subject already has one`
   - `refuses a re-post of a settled ask`
   - `refuses a card to a subject that is not operator-blocked`
   - `degrades an unreadable worker-manager record to no subject`
   - `degrades an unreadable verdict cache to no verdicts`
   Each drives the real function against a real fixture directory and asserts the specific outcome.

6. **`CHANGELOG.md`.** Add one bullet under `## Unreleased`:
   `- feat: pkg/sweepgate ports the covered-subject invariant — open-card attribution, the blocked-on-operator predicate, the row-set re-arm guard, and an always-zero exit contract`
   Do not create a second `## Unreleased` heading.

7. **Self-check before finishing.** Re-run `<verification>` and confirm it passes. Walk requirements 1-6. Confirm every case of the invariant mode returns 0, and confirm a `3` appears nowhere in the package.
</requirements>

<constraints>
- The invariant mode returns 0 on EVERY path, including an unreadable subject (AC6). The caller treats any non-zero exit other than its own reserved `3` as a broken install, and the port must not invent a `3`.
- The attention store and session registry are READ, never written. The only writes are the settled record and the card file, both under the state directories.
- Parsed JSON from disk is untrusted: a malformed record reads as "no record", never a fatal error.
- `slug()` is applied before every path join and before the dedup key; a subject that slugs to empty or escapes the state directory is an error, never a path.
- The vault is `Config.Vault`; no hardcoded vault path (AC7).
- The clock is `libtime.CurrentDateTimeGetter`; `recorded_at` uses it. No `time.Now()`.
- `github.com/bborbe/errors` only; do not introduce `fmt.Errorf`.
- Ginkgo v2 + Gomega, in-package tests, qualified imports, no counterfeiter mocks.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Additional checks (all container-executable):
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='prints the payload only when the card fires' -ginkgo.v` — exits 0 and prints one `case=<name> stdout_bytes=<n> exit=0` line per case.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='refuses a card to a subject with no owner' -ginkgo.v` — exits 0.
- `grep -q 'stderr io.Writer' pkg/sweepgate/invariant.go` — the payload builder takes its writer rather than writing to the process's.
- `! grep -rn 'ExitCode(3)\|return 3' pkg/sweepgate/*.go` — the port never invents the consumer's reserved code.
- `! grep -q 'Obsidian/Personal' pkg/sweepgate/*.go` — no hardcoded vault path.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
</verification>
