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

The tick bounds the read with a timeout (`predicateTimeout`, 25 minutes) and
abandons a hung read rather than stalling every later tick.

## The row cache and the served-set bound

The read is expensive (measured 18.8 minutes wall against 75 seconds of CPU on
2026-11-12), so it does not run on the tick's critical path. `refreshLoop`
(`pkg/flagged_tick.go`) keeps the row set warm and the tick serves the most
recent successful read. A failed read never discards the last good set; it costs
freshness, not the tick.

**The read is phase-locked to the tick.** It is started `serveGrace` (30
seconds) *plus the last read's own measured duration* before each tick, so a
read of the observed cost finishes about the grace before the tick and the set a
tick decides on was read shortly before it, not one interval earlier. The lead
adapts because the predicate's cost has ranged from sub-second (vault-api) to
18.8 minutes (the old subprocess probes); a fixed 30-second lead would land a
slower read after the tick it was meant to feed. It is capped at half the
interval. Before 2026-11-24 the read and the tick shared one
period, and a row flagged just after a read waited for the next read *and* the
tick after it — up to two intervals. Measured: a row flagged at 13:19:41 was
absent from the 13:30:46 tick because the read behind it started at ~13:15:48.

**Maximum served-set age: one tick interval plus `serveGrace`**
(`servedSetMaxAge`). The bound is a **log threshold, not a refusal**: a read
slower than the grace still lands late, and the tick serves the previous set
rather than skipping. The tick line therefore carries `rows_age=`, and a set
older than the bound also logs:

```
flagged-tick stale rows: served set read <age> ago, over the <bound> bound
```

`rows_age=` is the served set's age; `read_duration=` is how long the read took.
They answer different questions — a fast read can still serve an old set.

A row set persisted by a previous process is separately bounded by
`cacheMaxAge` (`2 × ReadTimeout + interval`, 65 minutes today) when it is
restored at start; an older one is refused and logged as `cache refused`.

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
  one command that clears it**: `vault-cli task set '<name>' flag true --by
  operator`. `unknown` is what `vault-cli` stamps when a flag write declares no
  actor (`--by` defaults to `unknown`), so admitting it would let any agent
  self-approve by omission. It is not agent-set either — nobody recorded an actor —
  and folding it into `agent-set-flag` was the defect: a legitimate operator row
  read exactly like an agent-set one and was refused forever with no remedy.

  The title is **single-quoted for the shell** (`shellQuote`), not
  double-quoted. A vault task title is attacker-influenced — any session may file a
  task — and a title carrying `"`, `$(…)` or a backtick would otherwise close a
  double-quoted interpolation and run as shell the moment the operator pasted the
  remedy. Inside single quotes `$`, backtick and backslash are literal, so the only
  escape needed is the quote itself: `'` renders as `'\''`.

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

## Foreign-vault rows

The predicate answers "which rows are eligible" across **every** configured vault, so its row set includes rows only another daemon can serve. The tick drops those before any of the guards above: a row whose vault family differs from the daemon's own is `skipped` with reason `foreign-vault` and never reaches the open batch. That reason is a selection-level skip, never one of the predicate's `refused` tokens — `refused` is a provenance verdict on the row, while a foreign row is perfectly eligible and merely belongs to a different daemon. See [`open-endpoint.md`](open-endpoint.md) § Vault-family guard for why the check cannot live only at open time.

## See also

[`parked-waits.md`](parked-waits.md) is the page for the tick's other decision: whether an eligible row is held because its last session parked on something outside its control.
