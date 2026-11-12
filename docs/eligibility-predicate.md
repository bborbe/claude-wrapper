# The eligibility predicate

`claude-wrapper`'s flagged reopen tick does not decide which rows are eligible.
It reads that decision from `flagged-rows.py`, which lives in **another repo** —
`claude-supervisor` — and is exec'd, never modified.

This page is the contract between the two. It exists because the spec that
introduced the tick is not a durable home for it: specs are archived once the
work lands, and a field renamed on the Python side would then decode to a Go
zero value **silently**, opening or refusing the wrong rows with no error.

## Where the predicate lives

| | |
|---|---|
| Source | `scripts/flagged-rows.py`, in the `claude-supervisor` repo |
| Deployed copy | `scripts/flagged-rows.py` under the claude-supervisor marketplace clone |
| Invocation | `<python3> <script> --json`; `--flagged-rows-script` sets the path |
| Cost | several minutes — it walks every vault's task pages, so it scales with fleet size. Measured over 2 minutes on 2026-11-12. |
| Ownership | `claude-supervisor` — read-only from this repo |

The tick bounds the read with a timeout (`predicateTimeout`, 10 minutes) and
abandons a hung read rather than stalling every later tick.

## Fields the tick consumes

`flagged-rows.py --json` emits `{"rows": [...]}`. These are the ten fields the
tick decodes; everything else in a row is ignored.

| Field | Meaning | How the tick uses it |
|---|---|---|
| `kind` | `task`, `goal` or `topic` | Only for the row's identity in the log line |
| `vault` | The Obsidian vault the row lives in | Passed to the spawner, which resolves the launcher from it |
| `name` | The row's title | Passed to the spawner as the tab label |
| `role` | `agent`, `manager` or `human` | `human` rows are ordered first; the role's window is named `Direct` |
| `mode` | `headless` or `interactive` | Passed verbatim to `CheckWorker` — **never defaulted**, because an empty mode takes no mode sub-cap at all |
| `session_id` | The row's recorded session, or null | Resume target, and the key for the ledger join and the log line |
| `liveness` | `LIVE`, `ABSENT` or `none` | `LIVE` rows are skipped |
| `branch` | `jump`, `resume` or `create` | `jump` rows are skipped (already reachable) |
| `blocked_by` | Unresolved blockers, as a list | A non-empty list skips the row |
| `refused` | The predicate's own provenance verdicts | A non-empty list refuses the row |

## The provenance rule — `OPENING_ACTORS`

A **task** refuses a flag an agent set. The predicate computes that into the
row's `refused` list, and the tick reads the list rather than re-deriving the
rule — the predicate is the single home of "who may set a flag", and a second
copy in Go would drift from it.

```
OPENING_ACTORS = ("operator", "legacy")
```

A task whose `flag_set_by` is outside that set — including an absent field — is
refused with the token `agent-set-flag`. `legacy` exists for rows filed before
the field did; no live row should be missing it.

A **goal or topic** accepts any provenance. Applying the task rule to them would
wrongly refuse every goal whose flag the fleet set.

## The two refusal tokens — `REFUSAL_REASONS`

```
REFUSAL_REASONS = ("agent-set-flag", "manager-row")
```

- `agent-set-flag` — the per-kind provenance rule above.
- `manager-row` — from the branch partition, for a row that belongs to a
  manager's own branch.

Both arrive in the row's `refused` list. The tick does not distinguish them: any
non-empty `refused` list means the row is not opened, and the first token is what
the log line reports.

## What breaks if this drifts

A renamed field decodes to its Go zero value and **no error is raised**. The
consequences are asymmetric and both bad:

- `liveness` renamed → every row reads as not-`LIVE` → the tick reopens rows
  that are already running, creating duplicate tabs.
- `refused` renamed → provenance checks vanish → an agent-set flag is opened as
  if the operator had set it.
- `mode` renamed → every row passes `""` to `CheckWorker` → no mode sub-cap
  binds at all.

The tick's `recently-spawned` guard limits the first case to a 60-minute window;
the other two have no backstop. When the predicate changes, change this page and
the Go struct in the same pass, and check that the tick's fixture still decodes
non-zero for every field above.
