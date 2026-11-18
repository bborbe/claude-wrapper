# The sweep gate

| | |
|---|---|
| Reference | `testdata/sweepgate/reference/sweep-gate.py` |
| Table renderer reference | `testdata/sweepgate/reference/box-table.py` |
| Pinned revision | `5beb8bf6c8df3acdbed387b0d9c7bba7bff0785f911c5b99462346fdf0b262a3` |
| Ported | 2026-11-18 |
| Package | `pkg/sweepgate` |
| Owner | `claude-wrapper` |

Both reference copies are retained permanently: `testdata/sweepgate/reference/README.md`
records their origins, and every line number on this page is cited against the
pinned revision above. Deleting either copy would delete the only record of what
was ported and invalidate those line numbers.

## Why it lives here

This repo owns "spawn/control **and periodic fleet ticks**". The sweep gate renders
a fleet table and produces a periodic tick, so it is a fleet tick rather than a
ranker or a renderer belonging to the attention service. The two candidate homes
exclude it by their own stated boundary: `attention-controller`'s `CLAUDE.md`
reads *"What it is not. No ranking …, no arms or renderers"*, and this repo's own
phase-1 description names spawn/list/inspect/answer/stop and did not name a change
gate. The parent goal's clause — "and periodic fleet ticks" — supplies the missing
one, and this tracked page is where that boundary is recorded. `CLAUDE.md` in this
repo is gitignored and cannot enter a PR, so the boundary cannot rest on editing
it.

## Modes

| Mode | stdout | stderr | exit |
|---|---|---|---|
| --check | `CHANGE <reason>` or `NOCHANGE` | park: <basis> after a successful resolve; every failure path writes here only | 0 (`ExitNoChange`) or 10 (`ExitChange`); 2 (`ExitUsage`/`ExitFailed`) on a failure |
| --render | the frame | park: <basis> after a successful resolve; every failure path writes here only | 0 or 10; 2 on a failure |
| --tick | the frame and `VERDICT change (<reason>)` ONLY on a change; nothing on a no-change tick | park: <basis> on every tick; every failure path writes here only | 0 or 10; 2 on a failure |
| --invariant | the card payload ONLY when the card fires | park: <basis>; skip / suppress lines | 0 ALWAYS |

Every mode prints `park: <basis>` to stderr after a successful resolve, so a
correct run that prints nothing on stdout is still citable. Every failure path
writes to stderr only: stdout is a caller's event stream, so a `CHANGE` line on an
unreadable subject would wake a model on every tick. The exit constants are named
in `pkg/sweepgate/exit.go` as `ExitNoChange` (0), `ExitChange` (10) and
`ExitUsage`/`ExitFailed` (2); no mode returns a bare integer.

`--invariant` is the exception to the exit contract: the caller treats any non-zero
exit other than its own reserved `3` as "the vault-side script is unavailable", so
this mode answers an unreadable subject with `0` and no stdout rather than a
failure. The port never invents the caller's `3`.

## State files

| Purpose | Path | Notes |
|---|---|---|
| digest state | `<StateDir>/<slug>.json` | `topic`, `digest`, `members`, `busy_since`, `recorded_at` |
| tick file | `<StateDir>/<slug>.tick.txt` | the operator-facing table; link-free; mode 0600 |
| card payload | `<StateDir>/<slug>.card.json` | written by `--tick` too, not only `--invariant` |
| cadence arm | `<StateDir>/<branch>--<slug>.cadence` | the branch prefix keeps a topic and a goal with one name from colliding |
| settled ask | `<SettledDir>/<slug>.json` | a separate directory, `<SettledDir>` |

The slug transform is part of the contract, not an implementation detail:

```
re.sub(r"[^a-z0-9]+", "-", topic.lower()).strip("-")
```

Subjects carry spaces — today's live one is `Stop Nil-Slice → Null Crashes on MDM`,
which slugs to `stop-nil-slice-null-crashes-on-mdm` — so the transform is the only
thing between a vault-authored subject and a file name. `Slug` additionally refuses
an empty result, `.`/`..`, and a path separator rather than normalising a traversal.

Six `SWEEP_GATE_*` environment variables are preserved as overrides:
`SWEEP_GATE_STATE_DIR`, `SWEEP_GATE_SETTLED_DIR`, `SWEEP_GATE_FEED_DIR`,
`SWEEP_GATE_HOLDS`, `SWEEP_GATE_PREDISPATCH_DIR` and `SWEEP_GATE_WORKER_MANAGER_DIR`.
A second host relies on `STATE_DIR`; the rest keep the fixture suite able to point
every read at a temporary tree.

## Divergences from the reference

- **The box table is rendered in Go**, not by a second Python process. The
  reference resolves `BOX_TABLE` to a marketplace clone and shells out to it; the
  port draws the same bytes in `pkg/sweepgate/boxtable.go`. This is why the parity
  comparison is byte-level: there is no second process whose output could be
  normalised.
- **The card payload is compact JSON**, not the Python's default `, ` / `: `
  spacing. It is a whitespace-only difference; the caller posts the payload and
  does not diff it.
- **The parity test normalises the marker's leading `HH:MM` on both sides.** The
  Python reads the wall clock and the port reads the injected clock, so an
  un-normalised comparison fails whenever the minute changes. Nothing else is
  normalised.
- **The parity goldens carry a provenance marker line** (`# generated by
  sweep-gate.py@<sha>`) that the Python does not emit. It proves the golden came
  from the reference rather than from the port.
- **The live-vault capture under `testdata/sweepgate/live/` is preserved only as
  the post-deploy diff reference**, not as a parity golden: it cannot be reproduced
  from the fixture vault.

Both reference copies are kept permanently. `generate.sh` stays re-runnable against
them, and the line numbers cited in this page stay checkable after the vault's own
copies are retired.

## Refusals

The list below is derived by one recorded sweep of the pinned reference. Run from
the repository root:

```
grep -nE 'return (None|EXIT_FAILED|EXIT_USAGE|LIVENESS_NONE|""|\[\]|False)\b|raise (ValueError|RuntimeError)|print\(f?"(fail-open|skip|suppress|refuse)' testdata/sweepgate/reference/sweep-gate.py
```

unfiltered hits: 65

Excluded classes — sweep hits that are not refusals:

- **predicate absence controls** (18): `return False` / `return None` for an absent input (`if not rec`, `if not sid`, an end-of-predicate default) is the predicate's negative answer, not a refusal.
- **duplicate park-predicate conditions** (2): the phase and open-box conditions at `sweep-gate.py:2296` and `:2298` repeat predicates already carried by the idle-stuck rows.
- **renderer spawn failure** (1): `sweep-gate.py:2865` is the Python's `box-table` subprocess failure; the port renders in Go and has no subprocess to fail.
- **paired failure return** (2): `sweep-gate.py:3314` and `:3329` are the `return EXIT_FAILED` halves of handlers already counted at their `print(f"fail-open: …")` line.
- **unreadable owner directory** (1): `sweep-gate.py:1358` reads an unreadable worker-manager directory as "no owner", the same fail-safe the record guard already carries.

excluded: 24

raw hits: 41

|Guard|Line|Test|Why|
|-|-|-|-|
| topic page with no ## Goals | `sweep-gate.py:553` | refuses a topic page with no Goals section | a topic with no declared membership must fail open rather than resolve to an empty set |
| unreadable task file | `sweep-gate.py:631` | returns false with no error for an unreadable task | one unreadable task must not stop the sweep |
| task with no frontmatter | `sweep-gate.py:634` | returns false when there is no frontmatter | a page without frontmatter is not a task row |
| unreadable heartbeat store | `sweep-gate.py:859` | degrades an unreadable heartbeat store to not live | a store that cannot be read must not report a dead worker live |
| store item with no kind | `sweep-gate.py:1089` | drops a store item whose mechanism has no kind | an unknown answer mechanism is not a gate |
| answered record as a gate | `sweep-gate.py:1207` | refuses an answered record as a gate | an answered item is not a raised gate |
| idle record as a gate | `sweep-gate.py:1209` | refuses an idle record as a gate | the dominant idle record is not a gate |
| rendered Stop panel as a gate | `sweep-gate.py:1228` | refuses a rendered Stop panel as a gate | a rendered closer panel is not a parked gate |
| unreadable worker-manager record | `sweep-gate.py:1306` | degrades an unreadable worker-manager record to no subject | a malformed registration must not attribute a card |
| row that is not the operator's | `sweep-gate.py:1418` | refuses a card to a subject that is not operator-blocked | one non-operator row means the subject is not blocked on the operator |
| existing card for the subject | `sweep-gate.py:1463` | refuses a card while the subject already has one | a second card would duplicate an open ask |
| subject with no owner | `sweep-gate.py:1471` | refuses a card to a subject with no owner | a card for an unowned subject is addressed to nobody |
| subject not blocked on the operator | `sweep-gate.py:1473` | refuses a card to a subject that is not operator-blocked | the invariant fires only when every non-terminal row is the operator's move |
| settled ask unchanged | `sweep-gate.py:1482` | refuses a re-post of a settled ask | an unchanged row set is an answered ask, not a new one |
| external wait on a non-Stop closer | `sweep-gate.py:1528` | detects an external wait only on a Stop closer | only a stopped worker's declaration is a machine-watched wait |
| headless session with no fresh heartbeat | `sweep-gate.py:1559` | falls through to the heartbeat for a headless worker | a headless worker without a fresh stamp is not live |
| parked row for the stuck flag | `sweep-gate.py:1624` | refuses a parked row the stuck flag | a parked row is waiting, not stalled |
| non-execution row for the stuck flag | `sweep-gate.py:1626` | refuses a non-execution row | only an execution row can be stuck |
| row with no open box for the stuck flag | `sweep-gate.py:1628` | refuses a row with no open box | a row with nothing open has no work to stall |
| row with no mtime | `sweep-gate.py:1631` | refuses a row with no mtime | an absent mtime cannot prove a freeze |
| missing state file | `sweep-gate.py:1817` | reports first run when the state file is missing | a first run is a full sweep, not a change |
| unreadable state file | `sweep-gate.py:1822` | reports unreadable for a directory in place of the file | an unreadable state forces a full sweep rather than a silent no-change |
| malformed state JSON | `sweep-gate.py:1826` | reports a parse error for malformed JSON | a parse error forces a full sweep |
| state object with no digest key | `sweep-gate.py:1828` | reports a parse error when the digest key is absent | a state without a digest cannot be trusted |
| non-object hold entry | `sweep-gate.py:2035` | treats a non-object entry as not held | a malformed hold must not suppress a wake |
| unreadable hold stamp | `sweep-gate.py:2052` | renders an unreadable stamp as a question mark | a malformed stamp must not read as a hold set seconds ago |
| terminal row for the park cell | `sweep-gate.py:2294` | refuses the park cell to a terminal row | a completed or aborted row is not parked |
| working row for the park cell | `sweep-gate.py:2303` | refuses the park cell to a working row | a file that moved inside two intervals is working |
| registered gate for the park cell | `sweep-gate.py:2305` | refuses the park cell to a registered gate | a feed row is already the report |
| ownerless row for the park cell | `sweep-gate.py:2307` | refuses the park cell to an ownerless row | no live owner is an orphan or stuck, not a park |
| in-flight row for the park cell | `sweep-gate.py:2309` | refuses the park cell to an in-flight row | a mid-tool-call session is working, not parked |
| registry waiting status as in-flight | `sweep-gate.py:2355` | trusts the registry's waiting status | a waiting registry record is decisive against in-flight |
| close-line spawn failure | `sweep-gate.py:2933` | maps close-line.py exits to a line, a decline and an error | a failed close-line spawn is an error, never a silent close |
| close-line non-zero exit | `sweep-gate.py:2938` | maps close-line.py exits to a line, a decline and an error | an unexpected exit is an error; only exit 1 declines |
| ambiguous subject | `sweep-gate.py:3162` | refuses an ambiguous subject naming both paths | a subject resolving both a goal and a topic is a caller error |
| subject that resolves no page | `sweep-gate.py:3167` | refuses a subject that resolves no page | a missing subject page is a caller error |
| unreadable subject | `sweep-gate.py:3213` | prints nothing for an unreadable subject | a fail-open line goes to stderr; stdout must stay silent |
| invariant on an unreadable subject | `sweep-gate.py:3230` | prints nothing for an unreadable subject | `--invariant` returns 0 on every path, including an unreadable subject |
| card file write failure | `sweep-gate.py:3287` | degrades a card write failure to a named stderr line | a failed side-channel write is named, never read as a verdict |
| tick file write failure | `sweep-gate.py:3313` | refuses a tick it cannot write the tick file for | an unwritable tick file must not wake the model on every tick |
| state save failure | `sweep-gate.py:3328` | refuses a tick it cannot record its state | a tick that rendered nothing must not send the host down the change-wake path |

Guards whose reference form the recorded regex does not match — a `continue`, a
positive return, or a dict assignment — are not rows above, but they are carried by
tests and belong to the same contract: the held, deferred, approval, external and
declared action silences (`refuses action lines to held, deferred, approval, external and declared rows`);
the role carve-out (`refuses a spawn offer to a non-spawnable role`); stuck
suppression (`refuses the problem cell to a waiting row`); the blocked-upstream rule
(`refuses blocked-upstream to a row whose blockers all completed`); and the
unreadable hold store, registry record, feed and malformed feed line degradations
(`degrades an unreadable hold store to nothing held`, `degrades an unreadable registry record to no record`,
`degrades an unreadable feed to the event-log fallback`, `degrades a malformed feed line to a skipped line`).
