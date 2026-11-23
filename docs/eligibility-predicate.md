# The eligibility predicate

`claude-wrapper`'s flagged reopen tick does not decide which rows are eligible.
It asks **vault-api** for them — the eligible set is computed by vault-api's own
index and flag/due filters — and then resolves each row's branch and provenance in
Go (`pkg/flagged_rows_vaultapi.go`).

This page is the contract for the fields the tick consumes and the rules it
applies to them. It exists because a field renamed on the vault-api side would
decode to a Go zero value **silently**, opening or refusing the wrong rows with no
error.

## Where the predicate lives

| | |
|---|---|
| Eligible rows | vault-api's list projection, filtered by `flag=true&status=in_progress&due=now` |
| Per-row enrichment | vault-api's record endpoint (`role`, `mode`, `flag_set_by`, `defer_date`, `blocked_by`) |
| Branch + provenance rules | `pkg/flagged_rows_vaultapi.go`, `pkg/flagged_branch.go`, `pkg/flagged_refused.go` |
| Read cost | one list request + one record read per eligible task |
| Ownership | vault-api owns the index; claude-wrapper owns the rules |

The tick bounds the read with a timeout (`predicateTimeout`, 10 minutes) and
abandons a hung read rather than stalling every later tick.

## Fields the tick consumes

The source returns one row per eligible page. These are the fields the tick
reads; everything else is ignored.

| Field | Meaning | How the tick uses it |
|---|---|---|
| `kind` | `task`, `goal` or `topic` | The row's identity in the log line, and the never-opened gate: only a `task` is opened fresh |
| `vault` | The Obsidian vault the row lives in | Passed to the spawner, which resolves the launcher from it |
| `name` | The row's title | Passed to the spawner as the tab label |
| `role` | `agent`, `manager` or `human` | `human` rows are ordered first; the role's window is named `Direct` |
| `mode` | `headless` or `interactive` | Passed verbatim to `CheckWorker` — **never defaulted**, because an empty mode takes no mode sub-cap at all |
| `session_id` | The row's recorded session, or null | Resume target, and the key for the ledger join and the log line |
| `liveness` | `LIVE`, `ABSENT`, `UNKNOWN` or `none` | `ABSENT` is resumed; `none` with an **empty** `session_id` on a `task` is opened fresh (never-opened, since 2026-11-15); every other value is skipped |
| `branch` | `jump`, `resume`, `create`, `direct`, `open-manager` or `skip` | `jump` rows are skipped (already reachable); a never-opened task must read `create` or `direct` |
| `blocked_by` | Unresolved blockers, as a list | A non-empty list skips the row |
| `refused` | The provenance verdicts this source computed | A non-empty list refuses the row |
| `provenance` | `operator`, `agent` or `unrecorded` | Counted into `provenance=` on the tick line, so one tick names all three states |

## The provenance rule — three states, not two

`flag_set_by` folds into exactly three states (`ProvenanceClass` in
`pkg/flagged_refused.go`):

```
OPENING_ACTORS   = ("operator", "legacy")     → operator-set  (admitted)
UNRECORDED       = ("", "unknown", "none")    → unrecorded    (refused, with remedy)
anything else                                 → agent-set     (refused)
```

- **operator-set** admits the row. `legacy` is the marker the 270 pre-field rows
  were converted to on 2026-09-30, so an absent field can refuse without silently
  dropping the operator's own historic sets.
- **agent-set** refuses the row with `agent-set-flag`. The 2026-09-28 ruling
  stands: an agent-set flag is not approval, at every tier (task, goal, topic).
- **unrecorded** refuses the row with `flag-provenance-unrecorded` **and names the
  one command that clears it**: `vault-cli task set "<name>" flag true --by
  operator`. `unknown` is what `vault-cli` stamps when a flag write declares no
  actor (`--by` defaults to `unknown`), so admitting it would let any agent
  self-approve by omission. It is not agent-set either — nobody recorded an actor —
  and folding it into `agent-set-flag` was the defect: a legitimate operator row
  read exactly like an agent-set one and was refused forever with no remedy.

## The three refusal tokens — `REFUSAL_REASONS`

```
REFUSAL_REASONS = ("agent-set-flag", "flag-provenance-unrecorded", "manager-row")
```

- `agent-set-flag` — an agent set the flag; the row is not approval.
- `flag-provenance-unrecorded` — the flag is set but no actor was recorded; the
  reason carries the one-command remedy, and only the operator runs it.
- `manager-row` — from the branch partition, for a row that belongs to a
  manager's own branch.

All arrive in the row's `refused` list. The tick does not distinguish them: any
non-empty `refused` list means the row is not opened, and the first entry is what
the log line reports. The unrecorded entry carries `: <remedy>` after the token,
which `reasonClass` cuts off so `refused_by_reason=` still counts it under the
bare token.

## What breaks if this drifts

A renamed field decodes to its Go zero value and **no error is raised**. The
consequences are asymmetric and both bad:

- `liveness` renamed → every row reads as not-`LIVE` → the tick reopens rows
  that are already running, creating duplicate tabs.
- `refused` renamed → provenance checks vanish → an agent-set flag is opened as
  if the operator had set it.
- `mode` renamed → every row passes `""` to `CheckWorker` → no mode sub-cap
  binds at all.

The tick's `recently-spawned` guard limits the first case to a 90-minute window;
the other two have no backstop. When the predicate changes, change this page and
the Go struct in the same pass, and check that the tick's fixture still decodes
non-zero for every field above.

## Never-opened rows

A flagged **task** that has never had a session reads `liveness: none`, an empty `session_id` and `branch: create` (or `direct` for `role: human`). It is opened fresh. Until 2026-11-15 only `ABSENT` was admitted, so such a row was skipped on every tick. Two limits keep this safe:

- **An empty `session_id` is required.** Under `--no-liveness` the predicate reports `none` for every row, including rows that do have a session; opening those fresh would start a second conversation beside a live one.
- **Tasks only.** For a goal or topic the predicate returns `create` whenever the manager chain is live *or unreadable*, so a goal `none`+`create` row is not proof that no manager exists.

The recently-spawned guard covers a fresh open by **label** (the task name the ledger records), plus an in-process record of this daemon's own opens. Without the in-process record, a tab spawn that registers after its 8-second deadline, or a ledger outage, would leave nothing to find, and the next tick would open the row again. The in-process record does not survive a daemon restart, so a restart that coincides with a missing ledger row can still open a row twice.

## See also

[`parked-waits.md`](parked-waits.md) is the page for the tick's other decision: whether an eligible row is held because its last session parked on something outside its control.
