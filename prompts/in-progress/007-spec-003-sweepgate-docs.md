---
status: approved
spec: [003-sweep-gate-port]
created: "2026-11-18T00:35:03Z"
queued: "2026-11-18T01:08:39Z"
---

# sweepgate documentation: the boundary, the contracts and the refusal table

<summary>
- One tracked page records why the gate lives in this daemon, so the placement rests on a file a reviewer can read rather than on a gitignored one
- The page states each of the four modes' stdout, stderr and exit contracts, and the state files the gate reads and writes
- The page lists every refusal, suppression and degradation the gate performs, each with the reference line it came from and the test that carries it
- The list is derived by a recorded sweep with its hit count and its justified exclusions, so it cannot silently shrink when a guard is added
- A test reads the page and fails if a listed refusal has no test behind it, or if the table's size disagrees with the recorded derivation
- The pinned reference copies are recorded as permanent, so every cited line number stays checkable
</summary>

<objective>
Write `docs/sweep-gate.md` as the durable home of the gate's contracts and its refusal list, and add the test that mechanically binds the list to the tests that carry it. This satisfies AC3 and completes AC8's documentation half.
</objective>

<context>
Read `docs/eligibility-predicate.md` for this repo's documentation voice: a table of "where it lives", the fields consumed, and the measured reason each rule exists. Follow that shape.

Read the package as it stands: `pkg/sweepgate/*.go` and `pkg/sweepgate/*_test.go`. The tests you will cite are named `It("refuses <name>", ...)` and `It("degrades <name>", ...)`.

Read both reference copies at `testdata/sweepgate/reference/`:
- `sweep-gate.py` — the gate itself. The line numbers you cite are at this exact revision.
- `box-table.py` — the renderer the Go port replaces; cite its line numbers when the guard is a renderer guard (e.g. the truncation and padding rules).

Both are frozen and **retained permanently** — `testdata/sweepgate/reference/README.md` records their origins, that `sweep-gate.py` is the vault's revision `1eb24d6d` (2026-11-16, 3 337 lines), that `box-table.py` is 217 lines, and that neither is deleted when the vault's own copy goes away. Deleting them would delete the only record of what was ported and invalidate every line number this page cites. Compute the pinned revision as the full sha256 hex of `sweep-gate.py`.

Also read the spec's Acceptance Criteria AC3, AC8 and AC10, and the Constraints section's boundary sentence.

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. **Create `docs/sweep-gate.md`.** It is a tracked file; `CLAUDE.md` in this repo is gitignored and cannot enter a PR, so this page is the only written boundary a reviewer can read. Sections, in this order (order matters — see requirement 6):
   - **Header.** Title `# The sweep gate`, then a two-column metadata block: `Reference` = `testdata/sweepgate/reference/sweep-gate.py`, `Table renderer reference` = `testdata/sweepgate/reference/box-table.py`, `Pinned revision` = the full sha256 hex of `sweep-gate.py`, `Ported` = the date, `Package` = `pkg/sweepgate`, `Owner` = `claude-wrapper`. Add one line stating that both reference copies are retained permanently, so every line number on the page stays checkable.
   - **`## Why it lives here`.** State the boundary in one paragraph: this repo owns "spawn/control **and periodic fleet ticks**"; the gate renders a fleet table and produces a periodic tick, so it is a fleet tick rather than a ranker or a renderer belonging to the attention service. Name the two candidates that exclude it by their own stated boundary (the attention service is explicitly "no ranking, no arms or renderers"; this repo's phase-1 description names spawn/list/inspect/answer/stop and did not name a change gate), and name the parent goal's clause that supplies the missing one. One short paragraph each, no more.
   - **`## Modes`.** A table of the four modes: `Mode | stdout | stderr | exit`. `--check` → `CHANGE <reason>` or `NOCHANGE`; 0 or 10. `--render` → the frame; 0 or 10. `--tick` → the frame and `VERDICT change (<reason>)` ONLY on a change; the tick file always written; 0 or 10. `--invariant` → the card payload only when the card fires; 0 ALWAYS. Every mode prints `park: <basis>` to stderr after a successful resolve, and every failure path writes to stderr only — stdout is a caller's event stream, so a `CHANGE` line on an unreadable subject would wake a model on every tick. Name the exit constants as the code names them (`ExitNoChange` 0, `ExitChange` 10, `ExitUsage`/`ExitFailed` 2).
   - **`## State files`.** A table with the five rows from the spec's AC8: purpose, path, notes — digest state `<StateDir>/<slug>.json`; tick file `<StateDir>/<slug>.tick.txt` (the operator-facing table, link-free, mode 0600); card payload `<StateDir>/<slug>.card.json` (written by `--tick` too); cadence arm `<StateDir>/<branch>--<slug>.cadence` (branch prefix); settled ask `<SettledDir>/<slug>.json` (a separate directory). State the slug transform verbatim (`re.sub(r"[^a-z0-9]+", "-", topic.lower()).strip("-")`) and why it is part of the contract (subjects carry spaces). List the six `SWEEP_GATE_*` overrides and say a second host relies on `STATE_DIR`.
   - **`## Divergences from the reference`.** Exactly these, each with its reason: the box table is rendered in Go rather than by a second Python process (the reason the parity comparison is byte-level); the card payload is compact JSON rather than the Python's default spacing (a whitespace-only difference; the caller posts the payload, it does not diff it); the parity test normalises the marker's leading `HH:MM` on both sides (the Python reads the wall clock and the port reads the injected clock, so an un-normalised comparison fails whenever the minute changes); the parity goldens carry a provenance marker line the Python does not emit; and the live-vault capture under `testdata/sweepgate/live/` is preserved only as the post-deploy diff reference, not as a parity golden (it cannot be reproduced from the fixture vault). Add one line recording that both reference copies are kept permanently — `generate.sh` stays re-runnable, and the line numbers cited above stay checkable after the vault's own copies are retired.
   - **`## Refusals`** — the LAST section in the document (see requirement 6), containing the sweep record and the table.

2. **Derive the refusal list by a recorded sweep.** Run exactly this command from the repository root and record its output count:
   ```
   grep -nE 'return (None|EXIT_FAILED|EXIT_USAGE|LIVENESS_NONE|""|\[\]|False)\b|raise (ValueError|RuntimeError)|print\(f?"(fail-open|skip|suppress|refuse)' testdata/sweepgate/reference/sweep-gate.py
   ```
   In the `## Refusals` section, record, in this exact shape:
   - the command above in a fenced block;
   - `unfiltered hits: <U>` — the number of lines the command prints;
   - an exclusion list: each excluded CLASS with its count and one-line justification (for example, a `return False` that is a predicate's positive control rather than a refusal), and `excluded: <E>` = the sum of the class counts;
   - `raw hits: <R>` where `R = U - E` — **exactly one occurrence of the string `raw hits:` in the whole document**.
   The label `raw hits:` carries the DERIVED count, not the unfiltered one, because the acceptance criterion's own evidence command greps for `raw hits: <n>` and compares it to the table's row count. Record the unfiltered number separately as `unfiltered hits:` so nothing is lost.

3. **The refusal table.** Columns `Guard | Line | Test | Why`. One row per retained guard, and the number of DATA rows must equal `R`.
   - `Line` is the reference line number at the pinned revision — `sweep-gate.py`'s, or `box-table.py`'s for a renderer guard, written so the file is unambiguous (e.g. `box-table.py:141`).
   - `Test` is the exact `It(...)` name from `pkg/sweepgate/*_test.go` (e.g. `refuses the park cell to an in-flight row`). Read the test files and copy the names; never invent one.
   - `Why` is one clause naming what the guard prevents.
   - Cover the guards the earlier prompts implemented, at minimum: the fail-open subject path, the three state-load failures, the tick-file write failure, the card write failure, the state-save failure, the held/deferred/approval/external/declared action silences, the five park predicates, the role carve-out, the stuck suppression, the blocked-upstream rule, the unreadable hold store / registry record / heartbeat store / feed / malformed JSON / malformed feed line degradations, and the four invariant withholdings (no owner, existing card, settled ask, not blocked).
   - If a retained guard has NO test behind it, add the test to `pkg/sweepgate` in this same prompt following the `It("refuses <name>", ...)` / `It("degrades <name>", ...)` convention, then cite it. A row whose `Test` cell names nothing that exists fails the test in requirement 5.

4. **Formatting that the acceptance criterion depends on — follow it exactly.**
   - The table's HEADER row and separator row must NOT start with the two characters `| ` (write them as `|Guard|Line|Test|Why|` and `|-|-|-|-|`). The criterion's evidence counts every line in the `## Refusals` range that starts with `| `, so a header written with a leading space is counted as a data row and the arithmetic never balances. Data rows DO start with `| `.
   - `## Refusals` is the last `## ` section in the document. The criterion's evidence reads `sed -n '/^## Refusals/,/^## /p'`, and any later `## ` heading with its own table would be swept into the count.
   - No other table may appear inside the `## Refusals` section.

5. **The binding test.** Add `Describe("carries every refusal", ...)` to `pkg/sweepgate` — this exact text is what the spec's verification greps for. It must:
   - read `../../docs/sweep-gate.md` (the test's working directory is the package directory) and fail with a clear message if the file is missing;
   - locate the `## Refusals` section and collect every line that starts with `| ` as a data row;
   - extract the `Test` cell (third column) from each row, and assert that some `pkg/sweepgate/*_test.go` file contains the literal `It("<test name>"` for it;
   - assert the row count equals the number parsed out of the single `raw hits:` line;
   - assert `unfiltered hits - excluded == raw hits`;
   - print, via `ginkgo.GinkgoWriter.Printf`, exactly `refusal=%s test=%s` per row (guard name, test name).
   This test is the mechanism that keeps the document from silently shrinking: adding a guard without a row, or a row without a test, fails it.

6. **AC3's evidence commands must pass unchanged.** Verify them yourself before finishing:
   - `printf 'rows '; sed -n '/^## Refusals/,/^## /p' docs/sweep-gate.md | grep -c '^| '`
   - `printf 'derived '; grep -oE 'raw hits: [0-9]+' docs/sweep-gate.md`
   Both must print the same number.

7. **`CHANGELOG.md`.** Add one bullet under `## Unreleased`:
   `- docs: docs/sweep-gate.md records the gate's boundary, its four mode contracts, its state-file layout and its refusal table, with a test that binds every listed refusal to the test carrying it`
   Do not create a second `## Unreleased` heading.

8. **Self-check before finishing.** Re-run `<verification>` and confirm it passes. Walk requirements 1-7. Re-read the document top to bottom and confirm every claim matches the code as it stands, not as the spec describes it.
</requirements>

<constraints>
- `CLAUDE.md` in this repo is gitignored and cannot enter a PR. The boundary must be recorded in `docs/sweep-gate.md`, which is tracked — landing the code in a repo whose only written boundary excludes it recreates the defect this change exists to end.
- Every guard cited must have a test behind it, and every test cited must exist with that exact name. The table is not aspirational.
- The refusal list is derived by the recorded sweep, never written from memory; the recorded count and the table's size must agree.
- The document states the mode contracts and the state-file formats as they are implemented, not as the Python implemented them, with the divergences named in their own section.
- Do not edit `testdata/sweepgate/reference/sweep-gate.py` or `testdata/sweepgate/reference/box-table.py`; the pinned line numbers depend on both being frozen, and both are retained permanently for exactly that reason.
- `github.com/bborbe/errors` only; do not introduce `fmt.Errorf`.
- Ginkgo v2 + Gomega, in-package tests, qualified imports.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Additional checks (all container-executable):
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='carries every refusal' -ginkgo.v` — exits 0 and prints one `refusal=<name> test=<name>` line per table row (AC3).
- `printf 'rows '; sed -n '/^## Refusals/,/^## /p' docs/sweep-gate.md | grep -c '^| '` and `printf 'derived '; grep -oE 'raw hits: [0-9]+' docs/sweep-gate.md` — both print the same number.
- `grep -c 'raw hits:' docs/sweep-gate.md` — prints `1`.
- `grep -c '^## Refusals' docs/sweep-gate.md` — prints `1`, and `awk '/^## /{print NR": "$0}' docs/sweep-gate.md | tail -1` names `## Refusals` as the last section.
- `grep -q 'reference/box-table.py' docs/sweep-gate.md` — the page names the renderer reference too, not only the gate.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
</verification>
