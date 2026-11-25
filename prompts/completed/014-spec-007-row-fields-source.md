---
status: completed
spec: [007-bug-flagged-tick-reopens-a-stopped-row]
summary: Added Stopped, StopDate and FlagSetAt to FlaggedRow and populated them from the task record frontmatter in VaultAPIFlaggedRowsSource, with a source test covering both bool shapes and absent/unparseable stamps.
execution_id: 42f58933-d4aa-5cba-93d0-aa45e8c4b180-exec-014-spec-007-row-fields-source
dark-factory-version: dev
created: "2026-11-25T07:30:00Z"
queued: "2026-11-25T06:39:22Z"
started: "2026-11-25T06:39:23Z"
completed: "2026-11-25T06:45:05Z"
branch: dark-factory/bug-flagged-tick-reopens-a-stopped-row
---

<summary>
- A flagged row the operator deliberately stood down is reopened by the fleet tick because the tick never reads the stand-down marker.
- This first step adds the marker and both timestamps to the row the tick decides on, so a later step can compare them.
- The row source already receives the task page's whole frontmatter map; it simply never lifted these three values out of it.
- The marker is read as a boolean, and both timestamps are read as instants — never as raw strings and never via an approval-date fallback.
- A timestamp that is missing or cannot be parsed becomes "no readable instant", which a later step treats as fail-closed.
- Only tasks carry the marker; goal and topic rows are left untouched.
- A new test proves the source populates all three values from the page, in both the quoted and unquoted YAML boolean forms, and leaves the instants empty when a stamp is absent or unparseable.
- No reopen decision changes in this step — the tick still behaves exactly as it does today.
</summary>

<objective>
Extend the tick's row representation with the operator's stand-down marker (`stopped`), the instant the stand-down was written (`stop_date`) and the raw flag instant (`flag_set_at`), and populate them from the task record page's frontmatter in `VaultAPIFlaggedRowsSource`. This is the plumbing half of spec 007: it makes the two facts the reopen decision needs available, without yet changing the decision.
</objective>

<context>
Read these before writing:

- `docs/eligibility-predicate.md` § *Fields the tick consumes* — the contract for the fields the tick reads. This prompt does NOT edit it (prompt 3 does); read it so the new fields are named consistently.
- `pkg/flagged_rows_source.go` — the `FlaggedRow` struct, its `Key()` and `OperatorNamed()` methods, and the doc comment stating it is the Go half of a cross-repo contract. You add the three fields here.
- `pkg/flagged_rows_vaultapi.go` — `rowForItem` (the `kind == "task"` branch already reads `role`, `mode`, `flag_set_by`, `defer_date`, `blocked_by`, `managed_by`, `goals` off `page.Frontmatter`) and the `frontmatterString` helper next to which you add `frontmatterTime`.
- `pkg/vaultapi_client.go` — `VaultAPIPage.Frontmatter map[string]any` (the raw YAML map the record endpoint serves).
- `pkg/flagged_rows_vaultapi_test.go` — the `fakeVaultAPI`, `taskRow` and `taskPage` fixtures and the existing `VaultAPIFlaggedRowsSource` Describe block; add the new test here.
- `pkg/flagged_tick.go` (`endedAtBySession`) — the established `time.Parse(time.RFC3339, ...)` idiom for parsing a stored instant; follow it.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega conventions this repo uses.
</context>

<requirements>
1. **Add three fields to `FlaggedRow` in `pkg/flagged_rows_source.go`.** Add the `"time"` import. The fields are populated only by `VaultAPIFlaggedRowsSource`, never by the exec/Python source, so tag them `json:"-"`:

   ```go
   // Stopped is the operator's stand-down marker (`stopped`), written by
   // /supervisor:stop. Populated by VaultAPIFlaggedRowsSource from the task
   // record's frontmatter; the exec source never sets it.
   Stopped bool `json:"-"`
   // StopDate is the instant the stand-down was written (`stop_date`), parsed
   // from the page's frontmatter. nil when the key is absent or unparseable —
   // both mean "no readable stop instant", which the decision treats as
   // fail-closed.
   StopDate *time.Time `json:"-"`
   // FlagSetAt is the raw `flag_set_at` instant, parsed from the page's
   // frontmatter. nil when absent or unparseable. It is the raw flag instant,
   // never an `approved_at` fallback.
   FlagSetAt *time.Time `json:"-"`
   ```

   Adding fields to a struct that is only ever built with keyed literals is source-compatible; do not reorder or rename existing fields.

2. **Add `frontmatterTime` next to `frontmatterString` in `pkg/flagged_rows_vaultapi.go`:**

   ```go
   // frontmatterTime reads one timestamp out of the record endpoint's frontmatter
   // map, returning nil when the key is absent or the value does not parse. nil is
   // the "no readable instant" signal the reopen decision treats as fail-closed.
   func frontmatterTime(frontmatter map[string]any, key string) *time.Time {
       raw := frontmatterString(frontmatter, key)
       if raw == "" {
           return nil
       }
       at, err := time.Parse(time.RFC3339, raw)
       if err != nil {
           return nil
       }
       return &at
   }
   ```

   `time.Parse(time.RFC3339, ...)` accepts a fractional-second input such as `2026-11-24T19:36:58.408271+01:00` even though the layout omits it — do not switch to a custom layout.

3. **Populate the three values in `rowForItem` for tasks only.** Declare the variables before the `if kind == "task"` block so they are in scope for the returned `FlaggedRow` literal, then set them inside that block from the page:

   ```go
   var stopped bool
   var stopDate, flagSetAt *time.Time
   if kind == "task" {
       page, err := s.Client.Page(ctx, kind, item.ID)
       // ... existing reads ...
       stopped = frontmatterString(page.Frontmatter, "stopped") == "true"
       stopDate = frontmatterTime(page.Frontmatter, "stop_date")
       flagSetAt = frontmatterTime(page.Frontmatter, "flag_set_at")
   }
   ```

   Include `Stopped: stopped, StopDate: stopDate, FlagSetAt: flagSetAt` in the returned `FlaggedRow{...}`. Goals and topics keep the zero values: only a task carries the marker and only the task record endpoint serves frontmatter.

   `frontmatterString` already coerces both shapes: a quoted `"true"` hits its `string` case and is trimmed, and an unquoted YAML bool `true` hits its default case and `fmt.Sprint(true)` yields `"true"`. `stopped: false` therefore coerces to `"false"` and compares false. Do not read `approved_at` anywhere — `flag_set_at` is the raw instant, never an approval fallback.

4. **Add the source test.** Add a new top-level `ginkgo.Describe("populates the stand-down marker from the record frontmatter", ...)` in `pkg/flagged_rows_vaultapi_test.go`, driving `source(client, nil).Rows(ctx)` with the existing `fakeVaultAPI` / `taskRow("t1", "Alpha")` / `taskPage(...)` fixtures. For each case, print

   ```
   stopped=%t stop_date=%s flag_set_at=%s
   ```

   where each nil stamp prints as the empty string and a non-nil stamp prints `Format(time.RFC3339Nano)` (so a zero-nanosecond instant renders without a fractional part). Assert each printed line exactly:

   - **all three keys, quoted forms** — page frontmatter `{"flag_set_by": "operator", "stopped": "true", "stop_date": "2026-11-24T18:05:33+01:00", "flag_set_at": "2026-11-24T19:36:58.408271+01:00"}` → prints `stopped=true stop_date=2026-11-24T18:05:33+01:00 flag_set_at=2026-11-24T19:36:58.408271+01:00`.
   - **unquoted YAML bool** — page frontmatter `{"flag_set_by": "operator", "stopped": true, "stop_date": "2026-11-24T18:05:33+01:00", "flag_set_at": "2026-11-24T19:36:58.408271+01:00"}` → prints `stopped=true`.
   - **none** — page frontmatter `{"flag_set_by": "operator"}` → prints `stopped=false stop_date= flag_set_at=`.
   - **unparseable `stop_date`** — `{"flag_set_by": "operator", "stopped": "true", "stop_date": "not-a-date", "flag_set_at": "2026-11-24T19:36:58.408271+01:00"}` → `stopped=true`, `stop_date=` empty (nil), `flag_set_at` printed.
   - **unparseable `flag_set_at`** — `{"flag_set_by": "operator", "stopped": "true", "stop_date": "2026-11-24T18:05:33+01:00", "flag_set_at": "not-a-time"}` → `stopped=true`, `stop_date` printed, `flag_set_at=` empty (nil).

   The two unparseable cases are the source half of the spec's Failure Mode *"Both stamps present but one unparseable → Row is skipped"*. They matter: if the source stored a zero `time.Time` instead of nil, an unparseable `stop_date` (year 1) would compare **before** the flag and reopen the row — the exact permissive failure the rule exists to prevent.

5. **Stay out of the decision.** Do not touch `decision()`, add a skip reason, or change `pkg/flagged_tick.go`. Prompt 2 adds the rule. Do not edit `docs/eligibility-predicate.md` — prompt 3 does that.

6. **Self-check before finishing:** re-run `<verification>`, confirm it passes, and walk each item above against the change.
</requirements>

<constraints>
- **The comparison later parses both stamps to instants.** Both are written local-time ISO with an offset and the offsets need not agree across a DST boundary, so instants — never lexicographic strings — are the right representation. Store `*time.Time`, not `string`.
- **The flag instant is the raw `flag_set_at`, never an `approved_at`-first fallback.** That fallback answers "when was this approved", a different question.
- **No producer change.** `stopped`, `stop_date` and `flag_set_at` already arrive in the frontmatter map. `VaultAPIPage.Frontmatter` is `map[string]any`, and `frontmatterString` coerces a YAML bool and a quoted `"true"` identically.
- **Only a task carries the marker.** Do not read these fields for goal or topic rows.
- Do NOT commit — dark-factory handles git.
- Do NOT add a CHANGELOG entry — spec 007 has no CHANGELOG acceptance criterion.
- Existing tests must still pass unchanged; adding struct fields must not perturb any current assertion.
</constraints>

<verification>
Run inside the container:

```
make precommit
go test -v -count=1 ./pkg -args -ginkgo.focus='populates the stand-down marker from the record frontmatter' -ginkgo.v
```

Expected: `make precommit` exits 0. The focused run exits 0 and prints one `stopped=... stop_date=... flag_set_at=...` line per case, including `stopped=true stop_date=2026-11-24T18:05:33+01:00 flag_set_at=2026-11-24T19:36:58.408271+01:00` and the two unparseable cases with an empty stamp.
</verification>

## Acceptance criteria mapping

| AC | Requirement |
|---|---|
| AC1 `make precommit` exits 0 | Verification |
| AC3 source populates the marker and both instants from the record frontmatter (both bool shapes, and all-empty when absent) | Req 1, 2, 3, 4 |
