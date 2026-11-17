---
status: completed
spec: [003-parked-observable-reopen-gate]
summary: Added docs/parked-waits.md documenting the four park classes, the Ends-only rule, the Next exclusion, MAX_AGE_S and the shared vocabulary, and linked it from docs/eligibility-predicate.md; CHANGELOG left untouched because the unreleased entry is already present.
execution_id: claude-wrapper-parked-observable-exec-005-spec-003-parked-waits-docs
dark-factory-version: v0.198.17
created: "2026-11-17T12:00:00Z"
queued: "2026-11-17T20:40:53Z"
started: "2026-11-17T23:46:37Z"
completed: "2026-11-17T23:50:00Z"
---

# Document the four park classes

<summary>
- A new page names the four wait classes and what each one observes.
- The page states that only a declaration naming an end-observable counts as a park.
- The page states that a forward pointer alone is not a park.
- The page names the one-hour expiry and why it exists.
- The page states where the vocabulary comes from, so it does not read as a competing taxonomy.
- The page is reachable from the neighbouring contract page, which has no "See also" section today and gains one.
- No code changes, and no second changelog entry.

</summary>

<objective>
Give the park gate a durable home. Specs are archived once the work lands, and the four class tokens are part of a cross-repo vocabulary: without a page stating that these names are `claude-supervisor`'s `parked-wait` class refined for one decision, a later reader will treat them as a competing taxonomy and either duplicate or replace them.
</objective>

<context>
Read these before writing anything:

- `docs/eligibility-predicate.md` — the shape and tone of the existing contract page in this repo. Note that it ends with prose and has **no "See also" section today**; you will append one.
- `pkg/parked_closer.go` (prompt 1) — the four class constants and the classifier's precedence.
- `pkg/parked_probe.go` (prompt 2) — what each class observes.
- `pkg/parked_baseline.go` (prompt 3) — `MAX_AGE_S` and its rationale.
- `pkg/pane_guard.go` — `machineWaitEnds`, the one home of the `⏰ Ends:` literal.
- `CHANGELOG.md` — confirm the `## Unreleased` entry from the tick prompt is present; do not add a second one.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
1. Create `docs/parked-waits.md`. It starts with an H1 title, then prose, in the same register as `docs/eligibility-predicate.md` (present tense, states why the page exists, names the owner of anything it does not own).

2. The page must contain each of these literals, because the verification greps for every one of them:

   - `parked-on-card`
   - `parked-on-pr`
   - `parked-on-host`
   - `parked-until`
   - `⏰ Ends:`
   - `⏰ Next:`
   - `MAX_AGE_S`

3. Cover, as sections or as a table with prose around it:

   - **What a park is.** The reopen tick reopens a flagged row whose session reads ABSENT. A session that parked on something outside its control also reads ABSENT, so the tick reads that session's last closer from its own event stream and holds the row while the declared observable is unchanged.
   - **The four classes**, one row each: the token, what it observes, when it counts as satisfied, and whether it expires.

     | Class | Observes | Satisfied when | Expires |
     |---|---|---|---|
     | `parked-on-card` | whether the row's session still owns an open attention card | the session no longer owns one | no |
     | `parked-on-pr` | a pull request's head, review decision and state | the state is `MERGED` or `CLOSED`, or any of the three fields changed since the recorded fingerprint | no |
     | `parked-on-host` | whether a host accepts a TCP connection | the host is reachable | no |
     | `parked-until` | a declared clock, or the age of the park when no clock is named | the clock has passed, or the park is older than `MAX_AGE_S` | only the unprobeable form |

   - **The `⏰ Ends:`-only rule.** Only a closer carrying `⏰ Ends:` is classified. The marker has exactly one home, `machineWaitEnds` in the pane guard, and this page must not present a second definition of it.
   - **The `⏰ Next:` exclusion.** `⏰ Next:` is the general forward pointer, not a park. A closer carrying only `⏰ Next:` is not held.
   - **`MAX_AGE_S`.** `MAX_AGE_S = 3600` seconds. State that it mirrors `claude-supervisor/scripts/declared-wait.py`'s rule for a declaration naming no future clock — one extra hour, never an indefinite pin — that it is a package constant rather than a config key, and that there is no per-class variant.
   - **The adversarial cases.** A bare `#412` (no owner/repo) and a bare `sun` (no port) are not probeable here, so they classify as `parked-until` and expire; a classifier that treated every declaration as probeable would send a bare number to `gh` and a bare word to the network. The same rule rejects an owner beginning with `-`, a pull-request number of zero, and a port or IPv4 octet out of range.
   - **A probe that cannot be read holds the row.** The skip reason is the class, with the cause appended after it. A hold is a skip, not a refusal: it does not spend a reservation and does not end the tick's opening pass.
   - **Where the vocabulary comes from.** The four class names are `claude-supervisor`'s `parked-wait` vocabulary refined for this one decision, not replaced. `scripts/closer-verdict.py`, `scripts/who-needs-me.py` and `scripts/declared-wait.py` remain the owners of that vocabulary; this page describes what `claude-wrapper` does with it and nothing more.

4. Append one "See also" section to `docs/eligibility-predicate.md`. That page has no such section today, so add one at the end, in the page's own prose register — a short `## See also` heading with a single sentence pointing at `parked-waits.md` as the page for the tick's other decision. Change nothing else in that file: no reordering, no edits to existing sections.

5. Do not touch `CHANGELOG.md` unless the park-gate entry is genuinely absent. The tick prompt adds one `- fix:` bullet under a new `## Unreleased` heading above `## v0.30.0`, and a second bullet would duplicate it. So:

   - First check whether the entry is already there, scoped to the unreleased section only — `parked` also appears in released sections, so a bare count over the whole file is not a signal:

     ```
     awk '/^## Unreleased/{f=1;next} /^## /{f=0} f' CHANGELOG.md | grep -cF 'parked'
     ```

   - A non-zero count means the entry is present: change nothing in `CHANGELOG.md`.
   - Only when the count is zero, add exactly one `- fix:` bullet under `## Unreleased`, creating that heading directly above `## v0.30.0` if it does not exist.
   - Never edit `## v0.30.0` or any other released section, and never create a second `## Unreleased` heading.

6. No Go code changes in this prompt. If you find yourself editing a `.go` file, stop: the behaviour is already implemented and this prompt is documentation only.

7. Before finishing, re-run `<verification>` and confirm it passes.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- **`docs/parked-waits.md` is this contract's durable home.** A later reader must be able to tell, from the page alone, that the four class names belong to `claude-supervisor`'s `parked-wait` vocabulary and are refined here rather than replaced.
- Do NOT restate or redefine the `⏰ Ends:` marker as a second definition; point at `machineWaitEnds` in `pkg/pane_guard.go`.
- Do NOT document a config key, flag or per-class timeout: none exists, and the page must not imply one does.
- Do NOT touch `claude-supervisor`'s `scripts/closer-verdict.py`, `scripts/who-needs-me.py` or `scripts/declared-wait.py` — they are read-only references.
- Do NOT change `flagged-rows.py`, its eligibility predicate, or the fields it emits.
- **The CHANGELOG entry belongs to the tick prompt.** Edit that file only when the unreleased section carries no `parked` bullet, and never inside a released section.
- Markdown only: no Go, no Makefile, no config changes.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then confirm every token the page must name is present (each count must be non-zero):

```
for t in parked-on-card parked-on-pr parked-on-host parked-until '⏰ Ends:' '⏰ Next:' MAX_AGE_S; do printf '%s ' "$t"; grep -cF "$t" docs/parked-waits.md; done
```

Then confirm the page is linked from its neighbour:

```
grep -cF 'parked-waits.md' docs/eligibility-predicate.md
```

Then confirm the changelog was not duplicated. There is at most one `## Unreleased` heading, and it is the only heading this prompt could have created:

```
grep -cE '^## Unreleased$' CHANGELOG.md
```

That prints `0` or `1`; a `2` means a second heading was created and must be removed.
</verification>
