---
status: approved
spec: [007-bug-flagged-tick-reopens-a-stopped-row]
created: "2026-11-25T07:30:00Z"
queued: "2026-11-25T06:45:01Z"
branch: dark-factory/bug-flagged-tick-reopens-a-stopped-row
---

<summary>
- The tick now skips a row the operator stood down unless it was re-flagged afterwards.
- The contract page that lists the fields the tick reads does not yet mention the three fields that make that decision possible.
- This step adds the stand-down marker and the two timestamps to that list, in the same section that documents the other fields.
- It states the ordering rule plainly, so a field the source serves but the tick ignores is a visible gap rather than a silent one.
- The fail-closed rule — no readable stamp means the row is skipped — is written down beside the ordering rule.
- Only documentation changes here; no code moves.
</summary>

<objective>
Document the three new fields the tick now consumes (`stopped`, `stop_date`, `flag_set_at`) and the ordering rule they implement in `docs/eligibility-predicate.md` § *Fields the tick consumes*, so the contract page names the stand-down and how it decides a reopen. This is the documentation half of spec 007.
</objective>

<context>
Read these before writing:

- `docs/eligibility-predicate.md` — § *Fields the tick consumes* holds the `| Field | Meaning | How the tick uses it |` table you extend; read the surrounding sections for voice and formatting. § *What breaks if this drifts* shows the style for "a renamed field decodes silently".
- `pkg/flagged_tick.go` — `decision()` (the guard just added) and `reasonStopped`; this is the shipped rule the doc describes.
- `pkg/flagged_rows_vaultapi.go` — `rowForItem` and `frontmatterTime`, where the three fields are read from the task record frontmatter. Only tasks carry them.
- `pkg/flagged_rows_source.go` — the `Stopped`, `StopDate` and `FlagSetAt` fields and their doc comments.
</context>

<requirements>
1. **Extend the table in § *Fields the tick consumes*.** Add three rows in the existing `| Field | Meaning | How the tick uses it |` format, matching the surrounding rows' voice:

   - `stopped` — the operator's stand-down marker, written by `/supervisor:stop`. Read as true when the frontmatter value coerces to `"true"` (the quoted `"true"` and the unquoted YAML bool `true` both do); `stopped: false` or an absent marker is not stopped.
   - `stop_date` — the instant the stand-down was written. **This row's "How the tick uses it" cell must state the ordering rule using the words "newer than":** a row whose `stop_date` is newer than its `flag_set_at` is skipped with reason `stopped`.
   - `flag_set_at` — the raw flag instant, never an `approved_at` fallback. Compared with `stop_date`: only a `flag_set_at` strictly newer than `stop_date` reopens the row.

2. **State the fail-closed rule beside the ordering rule.** A `stopped: true` row with no readable `stop_date` or no readable `flag_set_at` is skipped — absent evidence never reopens a retired row.

3. **State that the guard runs before the liveness guards**, so a stopped row reports `stopped` rather than `not-absent`, `already-live` or `recently-spawned`.

4. **Scope the fields to tasks.** Note that only a task carries the marker and only the task record endpoint serves these fields; goal and topic rows leave them zero and are unaffected.

5. **Do not change any other section, and do not touch code.** This prompt is documentation only.

6. **Self-check before finishing:** re-run `<verification>`, confirm it passes, and quote the matched `stop_date` line.
</requirements>

<constraints>
- The page is the contract for the fields the tick consumes; a field the source serves but the tick ignores is exactly the silent gap this page exists to surface, so the three fields and the rule must be present and unambiguous.
- Match the existing table format and section voice; do not restructure the page.
- Do NOT commit — dark-factory handles git.
- Do NOT edit `CHANGELOG.md` — spec 007 has no CHANGELOG acceptance criterion.
</constraints>

<verification>
Run inside the container:

```
sed -n '/## Fields the tick consumes/,/^## /p' docs/eligibility-predicate.md | grep -n 'stop_date'
sed -n '/## Fields the tick consumes/,/^## /p' docs/eligibility-predicate.md | grep 'stop_date' | grep -q 'newer than'
sed -n '/## Fields the tick consumes/,/^## /p' docs/eligibility-predicate.md | grep -q 'flag_set_at'
sed -n '/## Fields the tick consumes/,/^## /p' docs/eligibility-predicate.md | grep -q 'stopped'
make precommit
```

Expected: the first command prints at least one line (the `stop_date` row). The second exits 0 only if that line names the ordering rule ("newer than"). The third and fourth exit 0, proving all three fields are documented in the section. `make precommit` exits 0 (documentation-only change, nothing broken).
</verification>

## Acceptance criteria mapping

| AC | Requirement |
|---|---|
| AC1 `make precommit` exits 0 | Verification |
| AC4 the contract doc names the fields and the rule, inside § Fields the tick consumes | Req 1, 2, 3, 4; Verification |
