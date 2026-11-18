#!/usr/bin/env python3
"""sweep-gate.py — the manager sweep's change gate, evaluated OUTSIDE the model.

Why this exists
---------------
A manager sweep that finds nothing still wakes the model, and waking the model
re-sends the whole conversation. Measured 2026-09-20 across two manager sessions:
cache-read ran **133x** the genuinely-new tokens (133.7x on the Manager Layer
session, 133.4x on the Fleet Manager), and the per-turn cache-read was within 1%
across two sessions with wildly different tracked-set sizes (348,416 vs 345,344)
— so the cost tracks conversation length, not the set being swept. The sweep is
not the cost; the wake-up is.

This script is the gate that decides whether a tick is worth a wake. It runs as
the command of a `Monitor`: stdout is the event stream, so an empty stdout emits
no event and costs no model turn. `ScheduleWakeup` / `CronCreate` cannot host a
gate at all — they enqueue a prompt unconditionally.

Contract
--------
  sweep-gate.py --topic <name> --check    # verdict only; exit 0 = no change, 10 = change
  sweep-gate.py --topic <name> --render   # the frame (marker + table) to stdout
  sweep-gate.py --topic <name> --tick     # the frame -> tick file; stdout ONLY on change

The write path — the operator's sink decision, 2026-09-20 12:15 (`y` on option 1)
------------------------------------------------------------------------------
Every tick WRITES the marker + full table to
`~/.claude/state/sweep-gate/<topic>.tick.txt` and, on a no-change tick, prints
NOTHING to stdout. The two halves are load-bearing together: stdout is the
Monitor's event stream, so an empty stdout is exactly what makes an empty sweep
free, while the file is where the table still reaches the operator on every tick
(the operator's own 2026-09-15 rule: *"I like this table every time"*).

Why a FILE and not the chat, because this is the part that looks arbitrary and is
not: context is re-read wholesale every turn. Measured 2026-09-20, cache-read runs
133x the genuinely-new tokens, so N tokens written into a manager's chat cost N x
every turn that follows — a 500-token table over a thousand remaining turns is
half a million extra cache-read tokens. A file has no turn.

The tick file lives under `~/.claude/state/`, never inside a repo: a file in a
repo tree trips dirty-tree checks during somebody else's commit.

Exit codes
  0   digest equal to the stored one — nothing changed, do not wake the model
  10  digest differs, OR a STATE fail-open case fired — run the full sweep
  2   the tick could not do its job: the subject did not resolve, or the tick
      file could not be written. Also argparse's own usage-error code.
      ⚠️ NEVER 10 for these — see "Fail-open" below.

Fail-open, by design
--------------------
THREE cases force a full sweep rather than reporting "no change": a missing state
file (first-ever run), an unreadable state file, and a parse error. A gate that
silently reports "no change" when it cannot tell makes a manager blind to its own
topic, which is the one failure worth spending a wake to avoid.

⚠️ A SUBJECT that cannot resolve, and a failed tick-file write, are deliberately
NOT in that set — though this file carried both as `CHANGE` until 2026-10-12, and
the `--tick` case was commented as intentional ("a change you cannot parse IS a
change worth waking on"). The distinction is **transient versus permanent**. The
three cases above are state: they clear as soon as the file is readable again.
An unresolvable subject never clears — a phantom name, or a page renamed or moved,
reports `CHANGE` on every tick forever — and stdout is a `Monitor`'s event stream,
so the wake repeats with no table behind it. A gate that reports `CHANGE` forever
on a configuration error trains its reader to ignore `CHANGE`, which destroys the
one signal it exists to carry. Both now print to **stderr only** and return
`EXIT_FAILED`, which the tick host's `*)` branch already maps to `FAILED` and
alerts once per failure streak rather than once per tick.

⚠️ `--invariant` keeps its own `EXIT_NOCHANGE` carve-out for an unresolvable
subject, and that is not an oversight: its consumer reads `3` as "another layer
already holds it" and ANY other exit as "the vault-side script is unavailable",
reported once per round — so a non-zero exit there reports a working install as
broken, which is the measured defect that carve-out was added to fix.

Digest inputs (what SC2's fixture mutates)
  per tracked task: name, status, phase, claude_session_id, a hash of its
  `# Progress` section, its Session-column liveness word, and its stuck verdict
  plus the sorted member-name list (so a task added to or removed from the
  tracked set moves the digest even if every remaining task is untouched)

The digest is what the sweep would RENDER, plus the Progress signal. A `# Progress`
entry added while status and phase stay put is a change the sweep reports
("progressing") and must move the digest — the pre-2026-09-20 digest missed it,
because it hashed only the three frontmatter fields. The digest still deliberately
does NOT include mtime as a raw input: a touch with no content change is not a change
the sweep reports, and including it would wake the model on every unrelated rewrite.
It reaches the digest through `stuck` alone, and that is deliberate rather than a
leak — branch (b) of the `stuck` rule reads the task file's mtime, because that IS the
rule's "task file unchanged" measure, so for a row the rule measures a rewrite is not
unrelated: it resets the very clock the rule reads. Every other row is unaffected,
since `apply_stuck` leaves `stuck` False for it and its mtime never enters the digest.

Liveness, model-free — what is reachable and what is not
--------------------------------------------------------
The Session column (`[<sid8>]` live / `(<sid8>)` not live / `—` no id) and the
liveness-dependent buckets are filled from plain files, with no model and no
`ListAgents` (an MCP tool a script cannot call):

  REACHABLE — `~/.claude/sessions/<pid>.json` (the session registry) is the
  liveness probe. Measured 2026-09-20: 31 records, **31 with a live pid**, so the
  registry is pruned on exit and presence is a POSITIVE signal. It carries
  `sessionId`, `pid`, `status` (busy / idle / waiting / shell) and `name` (the
  task title). Absence is NOT proof of death — see the blind spot below.

  REACHABLE — `~/.claude/state/attention/*.needs.json` (the attention feed) is the
  park probe, using the feed's OWN rule (who-needs-me.py's `is_open_gate` core): a
  park is a raised gate — `kind` in {permission, question}, `state` != "answered",
  detail not a `later (on <trigger>):` parked wait. ⚠️ `state: "open"` ALONE is not
  a park: 18 of 35 records are `kind: idle, state: open` ("turn ended, waiting for a
  prompt"), and reading those as parks would mark most live sessions parked — a
  wrong liveness word, which is worse than a missing one.

  REACHABLE — ⚠️ problem (the runbook's `stuck`) is derivable, and this is the one
  place the gate needs cross-tick memory. The registry carries the session's
  CURRENT status but not how long it has held it: `statusUpdatedAt` is rewritten on
  every tool call (measured 2026-09-20 — statusAge == updAge for all six busy
  sessions), so busy-age is observed across ticks and stored. ⚠️ fires only when the
  session is continuously busy >= 30 min AND the task's `# Progress` section has not
  moved, which is the runbook's rule.

  NOT REACHABLE — the spawn ledger (`~/.local/state/claude-supervisor/sessions/`)
  is NOT a liveness source and is deliberately not read. Measured 2026-09-20: 125
  entries marked `running`, of which **18** were registry-live — the rest include
  interactive sessions spawned 2026-09-14 that cannot be alive. A `done` entry is a
  trustworthy positive; `running` is not a negative and not a positive. It carries
  no fact the registry does not carry better.

  BLIND SPOT, stated rather than papered over — a **headless** worker has no
  registry record of its own (measured: 3 of 66 ledger headless ids present), so a
  live headless worker renders `—`, not `live`. This is a false NEGATIVE, and it
  matches the runbook: `ListAgents` cannot see headless workers either, and the
  runbook renders `—` for an id absent from the roster. `—` means "no live session
  found", never "not live".

  STILL SHORT OF THE FRAME — the runbook's two-section split (`Essential (first
  iteration)` / `Optional (phase 2)`) is NOT implemented, and this is a *measured
  refusal* rather than an omission. The declared-optional set is read semantically
  from the topic page, and the runbook names a literal match a known-failing
  implementation. Two mechanical readings were tried and **both false-positive on
  this vault's real pages**: "the page mentions `optional`" fires on any prose
  about optionality (it fired on this task's own SC4 clause); and "a tracked
  task's name sits on a line carrying `optional`" still fires, because a topic
  page's `# Success Criteria` entries are single enormous list items — measured
  2026-09-20 on `23 Topics/Manager Layer.md:66`, one `- [ ]` bullet carrying the
  whole SC4 text, containing both `optional` and wikilinks to two tracked tasks,
  resolving them as declared-optional on a page that declares nothing optional.
  The gate therefore renders **one ungrouped box** and says nothing about
  optionality: the semantic read is the manager's, and the manager must not arm
  this gate on a topic that declares optional scope until the split lands.
  Recorded in `65 Runbooks/Manager Session.md` § Sweep output.

Renderer
--------
The frame is rendered by the supervisor plugin's `box-table.py`, read by absolute
path and never modified. The runbook owns the frame spec; this script only feeds
it. Nothing here ships: no PR, no release, no plugin install.
"""

from __future__ import annotations

import argparse
import glob
import hashlib
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import date, datetime, timezone

DEFAULT_VAULT = "/Users/bborbe/Documents/Obsidian/Personal"
BOX_TABLE = os.path.expanduser(
    "~/.claude/plugins/marketplaces/claude-supervisor/scripts/box-table.py"
)
# The close line's ONE producer — `action_lines()` renders through it at `:2200`,
# and the two manager print steps (`commands/manager-loop.md`, `commands/manager-status.md`)
# render through it too. Same resolved-absolute-path pattern as BOX_TABLE above, and
# for the same reason (`:191-192`): this file is vault-local and does not inherit
# ${CLAUDE_PLUGIN_ROOT}, so a path it must hand a *runnable* command has to be
# resolved rather than deferred to the reader's shell. Wired 2026-10-07 so this gate
# stops hand-assembling the line — a bare close instruction is correct at the moment
# it is printed and can decay to false before the operator reads it.
CLOSE_LINE = os.path.expanduser(
    "~/.claude/plugins/marketplaces/claude-supervisor/scripts/close-line.py"
)
# The runnable verdict's SECOND half. The registry answers *is the process alive*;
# it cannot answer *has the turn finished* — a session that has run its own close
# stays live while its process holds the socket. Same resolved-absolute-path pattern
# as BOX_TABLE and CLOSE_LINE, for the same reason (`:191-192`).
REAP_CLOSER = os.path.expanduser(
    "~/.claude/plugins/marketplaces/claude-supervisor/scripts/reap-closer.py"
)
# SWEEP_GATE_STATE_DIR lets a second host (the detached launchd loop) keep its own
# digest, so it never consumes a change the live manager's gate has not seen yet.
STATE_DIR = os.path.expanduser(
    os.environ.get("SWEEP_GATE_STATE_DIR", "~/.claude/state/sweep-gate")
)
# The settled-ask memory: one file per subject recording the identity of the ask
# that was already posted, so an ANSWERED ask is not re-posted while its blocked
# row set is unchanged. Its own override, added in the same change that adds the
# input — `sweep-gate-fixture.py` monkeypatches the constant (it loads this module
# via `importlib`, so an import-time env var is unreachable by it), but a second
# host must be able to point it elsewhere for the same reason as STATE_DIR above.
SETTLED_DIR = os.path.expanduser(
    os.environ.get("SWEEP_GATE_SETTLED_DIR", "~/.claude/state/sweep-gate-settled")
)
REGISTRY_DIR = os.path.expanduser("~/.claude/sessions")
# SWEEP_GATE_FEED_DIR pins the attention feed directory, so a fixture can exercise
# `read_feed()` without reading the operator's LIVE attention state. It is not
# cosmetic: `sweep-gate-fixture.py` pins only STATE_DIR, and a fixture that reads
# the real feed would pass or fail on whatever the operator's fleet happens to be
# doing at that moment. Every input the reader reads gets an override, in the same
# change that adds the input.
FEED_DIR = os.path.expanduser(
    os.environ.get("SWEEP_GATE_FEED_DIR", "~/.claude/state/attention")
)
# The drive leg's shipped in-flight check, reused verbatim rather than re-derived
# (`agents/manager-drive.md:100-108`, whose own line 108 reads *"Reuse that shipped
# reading; do not add a fourth definition of 'idle'."*). Two limbs, in that order:
# the session's open tool marker, then its transcript mtime against `LIVE_WINDOW`.
# Both roots are overridable for the same reason FEED_DIR is (:168-171) — a fixture
# that read the real directories would pass or fail on whatever the live fleet
# happens to be doing at that moment. ⚠️ Inline, never a subprocess into the plugin:
# this file is vault-local and does not inherit ${CLAUDE_PLUGIN_ROOT} (:175-178).
TOOL_MARKER_DIR = os.path.expanduser(
    os.environ.get("SWEEP_GATE_TOOL_MARKER_DIR", "~/.claude/state/attention")
)
TRANSCRIPT_DIR = os.path.expanduser(
    os.environ.get("SWEEP_GATE_TRANSCRIPT_DIR", "~/.claude/projects")
)
# `scripts/who-needs-me.py`'s LIVE_WINDOW, verbatim (`:46`): 5 * 60.
LIVE_WINDOW = 5 * 60
# The drive leg's marker-age cap (`agents/manager-drive.md:100-108`): an `open` tool
# marker older than this is the STUCK path's signal, not proof of work. The value is
# `scripts/who-needs-me.py`'s `--stuck-min` default (20), which that bullet names as
# the owner of the reading — mirrored here so the two renderers agree.
STUCK_MARKER_SECONDS = 20 * 60
# The attention store — `read_feed()`'s PRIMARY source. Read over HTTP rather than by
# shelling out to the plugin's `who-needs-me.py`, for the same reason HOLDS_PATH is
# read inline: this file is vault-local and does NOT inherit ${CLAUDE_PLUGIN_ROOT},
# so a subprocess dependency on a plugin path is a fail-open.
STORE_URL = (
    os.environ.get("SWEEP_GATE_STORE_URL")
    or os.environ.get("ATTENTION_STORE_URL")
    or "http://localhost:18080"
).rstrip("/")
STORE_TIMEOUT = float(os.environ.get("SWEEP_GATE_STORE_TIMEOUT", "3"))
# The store names the answering mechanism; the classifiers below read `kind`. The map
# is `who-needs-me.py`'s `KIND_FROM_MECHANISM` verbatim — a store item whose mechanism
# has no kind here (`ack`) is an acknowledgement, not a gate, and is dropped rather
# than rendered as a block that does not exist.
KIND_FROM_MECHANISM = {"permission": "permission", "message": "question"}
# The operator's session holds (skills/hold, claude-supervisor). Read here rather
# than by calling the plugin script: this file is vault-local and does NOT inherit
# ${CLAUDE_PLUGIN_ROOT}, so a subprocess dependency on a plugin path is a fail-open
# — and this gate's own docstring calls a silent fail-open its worst failure mode.
# The duplicated reader is deliberate; the plugin's SKILL.md names this file as the
# worked example of it.
HOLDS_PATH = os.path.expanduser(
    os.environ.get("SWEEP_GATE_HOLDS", "~/.claude/state/session-holds.json")
)
# The open-card invariant's two extra inputs (SC1), both read inline for the same
# reason HOLDS_PATH is (:222-230) — this file is vault-local and does NOT inherit
# ${CLAUDE_PLUGIN_ROOT}, so a subprocess into the plugin is a fail-open.
#
#   * `manager-predispatch.py` caches one audit verdict per task, per subject slug:
#     `<slug>.verdicts.json` -> `{task name: {verdict: "needs-you" | ...}}`. SC1's
#     "blocked on the operator" predicate names `needs-you` / `below-bar`, and those
#     verdicts exist ONLY here — `bucket()` cannot see them, because a row's audit
#     verdict is the manager layer's judgement and not a property of the task file.
#   * `worker-manager/<producer-session-id>.json` -> `subject` is SC1's FIRST
#     attribution signal: it says which subject a card's producer session belongs to.
#     The second signal is the subject slug inside the card's dedup key.
#
# ⚠️ Both get an override in the same change that adds the input, per this file's
# own rule at :177-182 — a fixture that read the operator's LIVE verdict cache or
# registry would pass or fail on whatever the fleet happens to be doing.
PREDISPATCH_DIR = os.path.expanduser(
    os.environ.get(
        "SWEEP_GATE_PREDISPATCH_DIR", "~/.claude/state/manager-predispatch"
    )
)
WORKER_MANAGER_DIR = os.path.expanduser(
    os.environ.get("SWEEP_GATE_WORKER_MANAGER_DIR", "~/.claude/state/worker-manager")
)
# SC1's predicate, as two literal sets. `OPERATOR_BUCKETS` are the bucket KEYS whose
# next move is the operator's; the `HELD` overlay is a LABEL change that keeps its
# row's ordinary key (`progressing`), so it is detected by `is_held()` rather than by
# a key here — see `subject_blocked_on_operator`.
OPERATOR_BUCKETS = frozenset({"waiting", "approval"})
OPERATOR_VERDICTS = frozenset({"needs-you", "below-bar"})


HEADER = ["Topic / Goal / Task", "Session", "Status", "Phase", "Met"]
# ⚠️ Status is 34, not 30, not 19, since 2026-09-30. The 19 was sized for the longest
# label the set held then (`⏸️ blocked/upstream`); the `HELD` overlay renders
# `⏸️ HELD — <reason> · <age>` — 9 cells of prefix, the reason, then a 4-cell ` · Nd`
# suffix. The Status cell is the ONLY place either the reason or the age renders —
# `action_lines()` deliberately emits nothing for a held row — so truncating here
# hides the operator's own words AND the age of their own policy, which are the whole
# point of the mark. ⚠️ 30 truncates the AGE away, not the reason: measured 2026-09-30
# through `box-table.py`, `⏸️ HELD — operator parked it · 3d` at width 30 renders
# `⏸️ HELD — operator parked it ·…` — the reason survives whole and the age, the thing
# this cell exists to add, is what gets eaten. ⚠️ Do not "restore" 30 or 19: both
# predate the cell. The runbook's § Sweep output "Columns and widths" line is the
# other half of this change.
WIDTHS = [42, 10, 34, 9, 17]

EXIT_NOCHANGE = 0
EXIT_CHANGE = 10
EXIT_USAGE = 2
# The subject-unresolvable / tick-write-failed exit. It shares `EXIT_USAGE`'s value
# because both mean "this tick could not do its job, and that is not a change
# signal". ⚠️ Deliberately NOT `EXIT_CHANGE`: stdout is a `Monitor`'s event stream,
# so a `CHANGE` line here wakes the model on every tick for a subject that can
# never resolve — see the module docstring's "Fail-open" section. And deliberately
# not a NEW number: `2` is already this script's non-change failure code, while `3`
# is taken by the `--invariant` consumer, which reads 3 as "another layer already
# holds it" and any other exit as "the vault-side script is unavailable".
EXIT_FAILED = EXIT_USAGE

STUCK_SECONDS = 30 * 60


# --------------------------------------------------------------------------- #
# frontmatter
# --------------------------------------------------------------------------- #

_FM = re.compile(r"^---\n(.*?)\n---", re.S)
_PROGRESS = re.compile(r"^# Progress\s*\n(.*?)(?=\n# |\Z)", re.S | re.M)


def split_frontmatter(text: str) -> str | None:
    m = _FM.match(text)
    return m.group(1) if m else None


def fm_scalar(fm: str, key: str) -> str:
    m = re.search(rf"^{re.escape(key)}:\s*(.*)$", fm, re.M)
    return m.group(1).strip() if m else ""


# YAML's null spellings. `fm_scalar()` is a raw regex with no YAML decoding, so a
# bare `null` reaches it as the four-character string "null" — which is truthy.
_YAML_NULLS = frozenset({"", "~", "null", "Null", "NULL"})


def fm_session_id(fm: str, key: str) -> str:
    """A recorded session id, or "" when the field holds no id.

    A null-ish scalar is NO id, not an id. Read raw, `claude_session_id: null`
    yields the string "null", which `session_cell()` renders `(null)` — a liveness
    claim about an id that does not exist, and a cell the runbook's Session bullet
    (`—` = no id recorded in either place) and `agents/manager-sweep-reader.md`
    (which reads the same field with YAML semantics) both forbid. Quoted spellings
    collapse here too: surrounding quotes are stripped first, so a quoted "null"
    is the same no-id rather than a recorded string.
    """
    value = fm_scalar(fm, key).strip("'\"")
    return "" if value in _YAML_NULLS else value


def fm_metrics_session_ids(fm: str, key: str = "metrics_sessions") -> list[str]:
    """Every `session_id` recorded under `metrics_sessions`, in file order.

    The block is a list of maps (`- session_id: …` / `started_at: …`), so neither
    `fm_list` nor `fm_wikilinks` reads it — both look for `[[…]]` or a comma list.
    """
    m = re.search(rf"^{re.escape(key)}:\s*\n((?:[ \t]+.*\n?)*)", fm, re.M)
    if not m:
        return []
    ids = []
    for raw in re.findall(r"^[ \t]+-?\s*session_id:\s*(.*)$", m.group(1), re.M):
        v = raw.strip().strip("'\"")
        if v and v not in _YAML_NULLS:
            ids.append(v)
    return ids


def session_id_set(fm: str) -> list[str]:
    """The row's WHOLE id set, in the reader's own order.

    `agents/manager-sweep-reader.md:78–80` reads exactly two places — the
    `claude_session_id` key and each `session_id` under `metrics_sessions` — and the
    runbook's Session bullet defines `—` over those same two. The gate must not read a
    narrower set than the cell's own definition names.
    """
    out, seen = [], set()
    own = fm_session_id(fm, "claude_session_id")
    for sid in ([own] if own else []) + fm_metrics_session_ids(fm):
        if sid not in seen:
            seen.add(sid)
            out.append(sid)
    return out


def fm_wikilinks(fm: str, key: str) -> list[str]:
    """All [[...]] under `key:`, whether inline (goals: ['[[X]]']) or block.

    All four shapes the vault writes must resolve: inline flow list, block list,
    quoted-wikilink scalar, and the **bare-name scalar** (`goals: Some Goal`) —
    plus empty/absent, which is legitimately `[]`. Block form is `key:` then
    indented `- '[[X]]'` lines; inline is `key: ['[[X]]', '[[Y]]']`.

    ⚠️ **The bare-name scalar is the one that used to return `[]`, and returning
    `[]` there is a SILENT drop, not an empty set.** `read_task` stores this
    result as the task's `goals`, and `resolve_tracked` admits a task only when
    its goals intersect the member set — so a task declaring its goal without
    brackets vanished from the tracked set entirely, with no error and no row.
    That is the same silent-drop class `fm_list`'s docstring records for
    `blocked_by`; the two functions now read a scalar the same way.

    ⚠️ **The block-list branch ALSO accepts a bare `- Name` item, and that
    reverses an earlier reading recorded here.** This docstring used to say the
    bracketed form was the only one the vault wrote and that a bare `- Name`
    was "far more likely a typo than a declaration, deliberately left returning
    `[]` rather than guessed at". That was wrong on the population: measured
    2026-11-02, `25 Tasks/The Fleet Manager Died at Midday With No Exit Record
    and No Known Killer.md` declares `goals:` then
    `    - No Session Sits Idle Unnoticed and a Lapsed Manager Is Detected` —
    no brackets, no quotes, a real declaration. The cost was the silent-drop
    class again: the snapshot's membership read 60 against the caller's 61, so
    `manager-predispatch.py --compare-tracked` reported a divergence it could
    never resolve from the plugin side, because this file builds the snapshot.

    ⚠️ **This file is the FORK, and `scripts/manager-predispatch.py` is its
    port.** The plugin cannot be imported here (`${CLAUDE_PLUGIN_ROOT}` is not
    set in this context, so a subprocess or import into it is a fail-open), so
    the two readers are kept in lockstep by hand. The plugin's side lives in
    `scripts/goals-declaration.py`; a change to one belongs in the other in the
    same session.
    """
    m = re.search(rf"^{re.escape(key)}:(.*)$", fm, re.M)
    if not m:
        return []
    rest = m.group(1)
    if rest.strip() and rest.strip() != "[]":
        found = re.findall(r"\[\[(.+?)\]\]", rest)
        if found:
            return found
        return [v.strip().strip("'\"") for v in rest.split(",") if v.strip()]
    block = re.search(
        rf"^{re.escape(key)}:\s*\n((?:[ \t]+-.*\n?)*)", fm, re.M
    )
    if not block:
        return []
    out: list[str] = []
    for line in block.group(1).splitlines():
        hit = re.search(r"\[\[(.+?)\]\]", line)
        if hit:
            out.append(hit.group(1))
            continue
        item = _bare_list_item(line)
        if item:
            out.append(item)
    return out


def _bare_list_item(line: str) -> str:
    """A `goals:` block item carrying no brackets, reduced to the page name.

    `    - Name` → `Name`, `    - - Name` → `Name`, `    - 'Name'` → `Name`.

    ⚠️ **Strips until stable, which is what makes the nested form work.** One pass
    leaves `- 'Name'` as `'Name'` and a second is needed for the quotes; a single
    `lstrip("-")` (what `fm_list` does) leaves the stray dash on a `- - Name` item,
    which then matches no page and reads as UNMET.
    """
    value = line.strip()
    previous = None
    while previous != value:
        previous = value
        value = value.strip()
        while value.startswith("-"):
            value = value[1:].strip()
        while len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
            value = value[1:-1].strip()
    return value.split("|")[0].split("#")[0].strip()


def fm_list(fm: str, key: str) -> list[str]:
    """A frontmatter list under `key:`, accepting BOTH shapes the vault writes.

    `blocked_by` entries are names or `[[wikilinks]]`, never paths — and both forms
    are live on disk: `- '[[Record the 2026-09-12 Baseline in the Rollup]]'` and
    `- Build Long-Running Identity Agent Type`. `fm_wikilinks` cannot serve here: it
    returns `[]` for the bare form, which reads as "no blocker" and would silently
    reproduce the exact defect the blocked-upstream bucket exists to remove.
    """
    m = re.search(rf"^{re.escape(key)}:(.*)$", fm, re.M)
    if not m:
        return []
    rest = m.group(1).strip()
    if rest and rest != "[]":
        found = re.findall(r"\[\[(.+?)\]\]", rest)
        if not found:
            found = [v.strip().strip("'\"") for v in rest.split(",")]
        return [v for v in found if v]
    block = re.search(rf"^{re.escape(key)}:\s*\n((?:[ \t]+-.*\n?)*)", fm, re.M)
    if not block:
        return []
    out: list[str] = []
    for line in block.group(1).splitlines():
        item = line.strip().lstrip("-").strip().strip("'\"")
        hit = re.match(r"\[\[(.+?)\]\]", item)
        out.append(hit.group(1) if hit else item)
    return [v for v in out if v]


def progress_hash(text: str) -> str:
    """A hash of the task's `# Progress` section — the sweep's change signal.

    The runbook's `progressing` bucket is "the task advanced, or a new dated
    `# Progress` entry appeared". Hashing the section is what lets the digest move
    on a Progress write that leaves status and phase alone, and what lets ⚠️ (stuck)
    honestly assert "no Progress entry" rather than "no frontmatter change".
    """
    m = _PROGRESS.search(text)
    return hashlib.sha256((m.group(1) if m else "").encode()).hexdigest()[:16]


# --------------------------------------------------------------------------- #
# tracked set
# --------------------------------------------------------------------------- #

_LIST_ITEM = re.compile(r"^\s*-\s*\[\[(.+?)\]\]")


def declared_members(topic_path: str) -> list[str]:
    """Read the topic page's `## Goals` list — declared membership, never inferred.

    Only list items count. Prose wikilinks inside the section are NOT members:
    `23 Topics/Manager Layer.md` names [[Unattended Execution]] inside a bullet's
    prose ("distinct from `# Non-goals`' work running unattended, which
    [[Unattended Execution]] still owns"), and a naive `[[...]]` sweep over the
    section admits it as a member.
    """
    with open(topic_path, encoding="utf-8") as fh:
        text = fh.read()
    # The section ends at the next `##` or `#` — NOT at a `###`. A topic page may
    # carry `###` prose notes or phase sub-headings INSIDE `## Goals` (measured
    # 2026-10-06: `23 Topics/Unattended Execution.md` splits its membership into
    # `### Phase 1` / `### Phase 2`), and terminating on `###` silently truncated
    # the member list — 52 of 59 entries on `Manager Layer`, and 0 of 4 on
    # `Unattended Execution`. Mirrored in `manager-predispatch.py` and `reset.py`
    # (claude-supervisor); all three must change together.
    m = re.search(r"^## Goals\s*\n(.*?)(?=\n## |\n# |\Z)", text, re.S | re.M)
    if not m:
        raise ValueError(f"no `## Goals` section in {topic_path}")
    members = []
    for line in m.group(1).splitlines():
        hit = _LIST_ITEM.match(line)
        if hit:
            members.append(hit.group(1).strip())
    return members


_CHECKBOX = re.compile(r"^\s*-\s*\[([ xX/])\]", re.M)
_STRUCK_ROW = re.compile(r"^\s*-\s*\[ \]\s*~~\[\[[^\]]+\]\]~~")


def checkbox_count(text: str, section: str | None = None) -> str:
    """`n/m` ticked-vs-total checkboxes, optionally scoped to a `# <section>`.

    The Met column reads `n/m` at every level (runbook § Sweep output): a task
    counts all its checkboxes, a goal its `# Success Criteria`, and the topic
    two labelled sets. A section with no checkboxes returns `—` rather than
    `0/0`, which would read as a real reading of nothing.

    ⚠️ **A struck row is excluded from every count** — a row whose content BEGINS with a
    struck wikilink, the goal page's convention for a task deliberately removed from the
    tracked set. It is still a checkbox item carrying a wikilink, so a literal count
    reads it as an OUTSTANDING subtask, in the direction that reports work where there is
    none. Measured 2026-09-24 on `24 Goals/a separate task.md`: **10 rows = 7 ticked + 3
    struck**, so a literal count scores **7/10** where the truth is **7/7**.

    ⚠️ **Trailing prose is allowed and the box must be UNTICKED — both measured, not
    assumed** (2026-10-08, whole vault). Of **33** struck-wikilink rows, **0** are the
    bare `- [ ] ~~[[Task]]~~` the rule's own example shows: **10** are
    `- [ ] ~~[[Task]]~~ — <reason>` and **23** are `- [x] ~~[[Task]]~~ — <reason>`. An
    anchor requiring `\\s*$` matches **nothing**, so the bug survives it untouched. The
    `[ ]` requirement is what keeps those **23 ticked** struck rows counted — a struck row
    that was already ticked is a met box, and dropping it would hide completed work.

    ⚠️ **Never key on the presence of `~~`.** A TICKED criterion carrying inline
    strikethrough over a clause that was later narrowed is still a live, met box: an
    exclusion keyed on `~~` anywhere in the line dropped a ticked `- [x]` and reported
    `13/14` where the truth was **14/15** — off by one, in the direction that hides
    completed work (measured 2026-09-22).

    ⚠️ **The box must be `[ ]`: a `- [/]` struck row is COUNTED**, because `/` means
    in-progress rather than removed from the tracked set. The 33-row census above measured
    only `[ ]` and `[x]` shapes, so this is a deliberate choice rather than a measurement.

    ⚠️ **Lines are split on a newline only — never `str.splitlines()`**, which also splits
    on the vertical-tab, form-feed, file/group/record-separator, NEL and Unicode
    line-separator characters that `re.M`'s caret does not anchor on; a struck row carrying
    one of those would split in two and escape the exclusion.
    """
    body = text
    if section:
        m = re.search(
            rf"^# {re.escape(section)}\s*\n(.*?)(?=\n# |\Z)", text, re.S | re.M
        )
        if not m:
            return "—"
        body = m.group(1)
    boxes = [
        hit.group(1)
        for line in body.split("\n")
        if (hit := _CHECKBOX.match(line)) is not None and not _STRUCK_ROW.match(line)
    ]
    if not boxes:
        return "—"
    return f"{sum(1 for b in boxes if b in 'xX')}/{len(boxes)}"


def read_task(path: str) -> dict | None:
    try:
        with open(path, encoding="utf-8", errors="replace") as fh:
            text = fh.read()
            # `fstat` on the descriptor we just read, never a second `stat(path)`: the
            # path could be replaced between the two calls, and the idle clock branch
            # (b) reads has to describe the file whose contents we actually hashed.
            mtime = os.fstat(fh.fileno()).st_mtime
    except OSError:
        return None
    fm = split_frontmatter(text)
    if fm is None:
        return None
    return {
        "name": os.path.basename(path)[:-3],
        "status": fm_scalar(fm, "status"),
        "phase": fm_scalar(fm, "phase"),
        # An operator-set flag IS the approval (ruling 2026-11-15, the supervisor's
        # `flag_provenance.is_approved()`): `flag: true` + `flag_set_by` operator/legacy.
        # Strict YAML-boolean read, matching that predicate's task rule.
        "operator_flagged": (fm_scalar(fm, "flag") or "").strip() in
        ("true", "True", "TRUE", "yes", "Yes", "YES", "on", "On", "ON", "y", "Y")
        and (fm_scalar(fm, "flag_set_by") or "").strip("\"'") in ("operator", "legacy"),
        # `role:` is a spawn-PERMISSION gate, not a spawn-mechanism choice (`mode:`).
        # Absent returns the empty string, the same shape `status` and `phase` use —
        # never a non-spawnable default. See NON_SPAWNABLE_ROLES.
        "role": fm_scalar(fm, "role"),
        # A null-ish scalar is no id, not the string "null" — see fm_session_id.
        # `session` stays the CURRENT owner alone: the hold store keys on it, and
        # `manager-sweep-reader.md:97` forbids widening that key to the whole set.
        "session": fm_session_id(fm, "claude_session_id"),
        # Every id the row records — what `—` is defined over, and what liveness is
        # probed across. Distinct from `session` above, deliberately.
        "session_ids": session_id_set(fm),
        "goals": fm_wikilinks(fm, "goals"),
        # `blocked_by` names prerequisite tasks. Read here, resolved to a verdict in
        # apply_blocked_upstream — the blocker may sit OUTSIDE this topic's tracked
        # set, so the status lookup is vault-wide. `fm_list`, not `fm_wikilinks`:
        # both the `[[Name]]` and the bare `Name` shapes are live on disk.
        "blocked_by": fm_list(fm, "blocked_by"),
        # `defer_date` drives the `deferred` overlay in `bucket()`. Quoted here, so
        # the dict carries the bare date whatever shape the vault wrote — the quoted
        # `"YYYY-MM-DD"` and the unquoted RFC3339 both land as the same string. See
        # `is_deferred`; the overlay is rendering-only and never reclassifies.
        "defer_date": fm_scalar(fm, "defer_date").strip("'\""),
        # The declared-wait CONTRACT. A worker deliberately waiting on a named
        # external event writes `declared_wait: <what it waits on>`, and the park cell
        # must not then ask it whether it is parked. A CONTRACT, not a text heuristic
        # over `# Progress` — the same reasoning `EXTERNAL_WAIT_MARKER` records for
        # `⏰ Ends:`. Absent reads as "" and the flag stays false; `apply_declared_waits`
        # normalises this raw string to a bool before `_classify` sees it.
        "declared_wait": fm_scalar(fm, "declared_wait").strip("'\""),
        "progress_hash": progress_hash(text),
        "met": checkbox_count(text),
        # The idle clock branch (b) of the runbook's `stuck` rule reads, carried on the
        # row so `apply_stuck` needs no path threaded into it. `read_task` is the only
        # place the path is in hand.
        "mtime": mtime,
    }


# Bumped whenever the entry shape changes; a version mismatch is treated as "no
# index", so an upgrade costs one full scan and never a wrong membership.
TASKS_INDEX_VERSION = 1


def tasks_index_path(vault: str) -> str:
    """Per-vault cache of `25 Tasks/` selection metadata — never of the row itself.

    ⚠️ The loop runs this script as a SEPARATE PROCESS PER SUBJECT, so an in-process
    cache cannot survive from one subject to the next. The index is therefore a FILE,
    keyed by the vault, and every run validates each entry by (mtime, size) before
    trusting it. A missing, unreadable or stale-version index means "read everything",
    which is the old behaviour — the cache can only ever save work, never change an
    answer.
    """
    key = hashlib.sha256(os.path.abspath(vault).encode()).hexdigest()[:12]
    return os.path.join(STATE_DIR, f"tasks-index-{key}.json")


def load_tasks_index(vault: str) -> dict:
    try:
        with open(tasks_index_path(vault), encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, json.JSONDecodeError):
        return {}
    if not isinstance(data, dict) or data.get("version") != TASKS_INDEX_VERSION:
        return {}
    files = data.get("files")
    return files if isinstance(files, dict) else {}


def save_tasks_index(vault: str, files: dict) -> None:
    """Best-effort: a cache that cannot be written must never fail the sweep."""
    try:
        os.makedirs(STATE_DIR, exist_ok=True)
        path = tasks_index_path(vault)
        tmp = path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump({"version": TASKS_INDEX_VERSION, "files": files}, fh)
        os.replace(tmp, path)
    except OSError:
        pass


def resolve_tracked(vault: str, members: list[str]) -> list[dict]:
    """A member may be a goal (admits tasks whose `goals:` name it) or a task.

    Vault-only and pure: it never touches the registry, so the fixture can run it
    against a throwaway vault and get the same values the live run gets.

    ⚠️ The SELECTION is indexed; the ROW is not. `25 Tasks/` holds 11,512 files / 166 MB
    (measured 2026-11-12), and reading every one of them per subject — twice per subject
    across 127 subjects — was ~2.9 M file reads and ~42 GB of I/O per loop pass, which
    is why a pass took ~106 min against a 900 s interval. The index caches only
    `{goals}` per file, enough to answer "is this file a member?", so a resolution reads
    the selected rows (~145) instead of the whole tree. Everything `read_task` returns
    still comes from a fresh read of the file, so a row can never be served stale.
    """
    tasks_dir = os.path.join(vault, "25 Tasks")
    member_set = set(members)
    index = load_tasks_index(vault)
    fresh: dict = {}
    tracked: list[dict] = []
    try:
        entries = sorted(os.listdir(tasks_dir))
    except OSError:
        return []
    for entry in entries:
        if not entry.endswith(".md"):
            continue
        path = os.path.join(tasks_dir, entry)
        try:
            st = os.stat(path)
        except OSError:
            continue
        cached = index.get(entry)
        if (
            isinstance(cached, list)
            and len(cached) == 3
            and cached[0] == st.st_mtime
            and cached[1] == st.st_size
        ):
            fresh[entry] = cached
            # `None` goals marks a file with no usable frontmatter: it can never be a
            # member, and caching it stops it being re-read on every subject.
            goals = cached[2] or []
            if entry[:-3] in member_set or member_set.intersection(goals):
                task = read_task(path)
                if task is not None:
                    tracked.append(task)
            continue
        task = read_task(path)
        if task is None:
            fresh[entry] = [st.st_mtime, st.st_size, None]
            continue
        fresh[entry] = [st.st_mtime, st.st_size, task["goals"]]
        if task["name"] in member_set:  # declared directly as a task entry
            tracked.append(task)
        elif member_set.intersection(task["goals"]):
            tracked.append(task)
    save_tasks_index(vault, fresh)
    return sorted(tracked, key=lambda t: t["name"])


def digest_of(tracked: list[dict]) -> str:
    """What the sweep would render, plus the Progress signal it reports.

    `liveness` and `stuck` are set by the caller before this runs; both default to
    absent so a caller that has not enriched them (the fixture) still gets a stable
    digest.
    """
    h = hashlib.sha256()
    for t in sorted(tracked, key=lambda x: x["name"]):
        h.update(
            (
                f"{t['name']}|{t['status']}|{t['phase']}|{t['session']}"
                f"|{t.get('progress_hash', '')}|{t.get('liveness', LIVENESS_NONE)}"
                f"|{int(bool(t.get('stuck')))}\n"
            ).encode()
        )
    h.update(b"--members--\n")
    for t in sorted(tracked, key=lambda x: x["name"]):
        h.update(f"{t['name']}\n".encode())
    return h.hexdigest()


# --------------------------------------------------------------------------- #
# liveness — model-free, from the registry and the attention feed
# --------------------------------------------------------------------------- #

LIVENESS_LIVE = "live"
LIVENESS_PARKED = "parked"
LIVENESS_NONE = "none"

# The headless-worker heartbeat store, written by the supervisor server that owns each worker
# (`server/heartbeat.mjs` in claude-supervisor) and read here.
#
# A headless worker is an in-process SDK `query()` with no registry record, so before this
# store existed every probe this gate has returned NONE for one. NONE is not neutral
# downstream: `session_cell` renders it as `—` ("no session"), and `bucket()` reads a `next`
# task with NONE as 🚀 ready — so a task whose headless worker was genuinely mid-turn invited
# a DUPLICATE spawn onto live work. That is the dangerous direction, and it is what the
# heartbeat closes. It is also the only channel a non-spawning process has: the spawning
# server's own `agents` Map answers correctly and only for its own workers.
HEARTBEAT_DIR = os.environ.get("SUPERVISOR_HEARTBEAT_DIR") or os.path.join(
    os.environ.get("XDG_STATE_HOME")
    or os.path.join(os.path.expanduser("~"), ".local", "state"),
    "claude-supervisor",
    "live",
)
# Mirrors HEARTBEAT_TTL_MS in the server. Two copies of one number, paid knowingly: this gate
# runs model-free and must not depend on a checkout being present, so it cannot import the
# constant. Changing one without the other is the failure mode, and it fails in the safe
# direction — a longer TTL here reads a dead worker live, which suppresses a close line
# rather than inviting a duplicate spawn.
HEARTBEAT_TTL_SECONDS = 60


def heartbeat_live(sid: str, now: float | None = None) -> bool | None:
    """True / False / None for one session id. None is "could not read", never "not live".

    ⚠️ The verdict is the stamp's AGE against the TTL, never the file's existence. A server
    killed with `kill -9` never clears its stamps, so a file that exists but has stopped
    being refreshed is a DEAD worker — reading existence would report a live worker for
    exactly the case the store exists to catch.

    None and False stay distinct for the same reason `read_registry` keeps them distinct: a
    caller that folds an unreadable store into "not live" turns an I/O error into a confident
    negative, and a negative here is what invites the second writer.
    """
    now = time.time() if now is None else now
    try:
        age = now - os.stat(os.path.join(HEARTBEAT_DIR, f"{sid}.json")).st_mtime
    except FileNotFoundError:
        return False
    except OSError:
        return None
    return age < HEARTBEAT_TTL_SECONDS

PARKED_VERBS = ("later (on ",)
# The `⏰ Ends:` slot of the Async State Closer's 🟡 WAITING panel — the worker's OWN
# declaration that a MACHINE will end its wait, not that a person will.
#
# ⚠️ Deliberately NOT `⏰ Next:`, which is the general-purpose forward pointer and
# appears on rows that are waiting on nothing at all. Measured 2026-10-04 over
# 21,349 `Stop` records: `⏰ Ends:` on **4,967**, `⏰ Next:` on 843, and the two are
# disjoint in meaning — `~/.claude/CLAUDE.md` § Async State Closer scopes `⏰ Ends:`
# to the 🟡 WAITING panel alone, the one state whose validity rule is "a machine
# produces the change". So this is a contract, not a text heuristic.
#
# ⚠️ The `👤 You:` line is NOT usable here. It looks like the natural partner — and
# the measured case's closer carried `👤 You: nothing` — but the `Stop` record's
# `detail` holds ONE line, so `👤 You: nothing` and `⏰ Ends:` never co-occur in it
# (48 records carry the former, 4,967 the latter), and `👤 You: nothing` alone is
# also what a plainly CLOSED session writes (`👤 You: nothing — session closed`).
# Keying on it would file every closed session as waiting on a mechanism.
EXTERNAL_WAIT_MARKER = "⏰ Ends:"
_ENTITY = re.compile(r"&(?:nbsp|#160|#xa0);", re.I)


def read_registry() -> dict[str, dict]:
    """`~/.claude/sessions/<pid>.json` -> {sessionId: {pid, status, name, alive}}.

    The registry is pruned on exit — measured 2026-09-20, 31 records and 31 live
    pids — so a record with a live pid is a positive liveness signal. A missing
    record proves nothing: a headless worker has none.
    """
    out: dict[str, dict] = {}
    for path in glob.glob(os.path.join(REGISTRY_DIR, "*.json")):
        try:
            with open(path, encoding="utf-8") as fh:
                d = json.load(fh)
        except (OSError, json.JSONDecodeError):
            continue
        sid = d.get("sessionId")
        if not sid:
            continue
        pid = d.get("pid")
        alive = False
        if isinstance(pid, int):
            try:
                os.kill(pid, 0)
                alive = True
            except PermissionError:  # exists, but not ours -> alive
                alive = True
            except OSError:
                alive = False
        out[sid] = {
            "pid": pid,
            "status": d.get("status", ""),
            "name": d.get("name", ""),
            "alive": alive,
        }
    return out


class StoreUnreachable(Exception):
    """The attention store did not answer — the event log carries the read instead."""


FEED_INDEX_VERSION = 1


def feed_index_path() -> str:
    """Per-vault cache of each event log's ALREADY-PARSED fold.

    ⚠️ It caches the fold, not a narrowed version of it. An earlier design kept only
    the currently-open items and dropped answered ones; that is NOT equivalent, and
    the measurement said so (cold `feed_open` 809 against a 796 baseline) — the
    answered records are part of `_log_items`' contract, so this cache reproduces
    them exactly and the saving comes from not re-parsing 243,941 lines per
    resolution, never from returning less.

    Each entry is `[mtime, size, byte_offset, opened, closed]`, where `opened` is
    `[[item_id, record], …]` and `closed` is the list of ids seen on a `close` line.
    A file whose `(mtime, size)` matches is not opened at all; a grown one is resumed
    at the cached offset, so only newly appended lines are parsed.
    """
    return os.path.join(STATE_DIR, "feed-index.json")


def _load_feed_index() -> dict:
    try:
        with open(feed_index_path(), encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, json.JSONDecodeError):
        return {}
    if not isinstance(data, dict) or data.get("version") != FEED_INDEX_VERSION:
        return {}
    files = data.get("files")
    return files if isinstance(files, dict) else {}


def _save_feed_index(files: dict) -> None:
    """Best-effort: a cache that cannot be written must never fail the sweep."""
    try:
        os.makedirs(STATE_DIR, exist_ok=True)
        path = feed_index_path()
        tmp = path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump({"version": FEED_INDEX_VERSION, "files": files}, fh)
        os.replace(tmp, path)
    except OSError:
        pass


_LOG_ITEMS_MEMO: list[dict] | None = None


def _log_items() -> list[dict]:
    """Fold each session's append-only event log into its currently-open items.

    ⚠️ MEMOISED per process. One subject's tick calls this 2–3 times (`read_feed`,
    `open_cards`) against identical inputs, and each call re-reads the whole index
    and re-stats every log — measured 2026-11-13 as the remaining cost once the
    parse itself was cached. A tick process is short-lived, so the fold cannot move
    underneath the calls it serves.

    `<sid>.events.jsonl` is append-only and never rewritten: an `open` line carries
    the item, a `close` line carries only the `item_id` it clears. An item is open
    exactly when its `item_id` has an `open` line and no matching `close`.

    `state` is reconstructed here rather than read from the record, so every
    downstream classifier keeps working unchanged against this format. A malformed
    line is skipped, not fatal: the hook appends while this reads, so a torn final
    line is expected rather than exceptional — only bytes up to the last newline are
    consumed, so a torn line is re-read whole next time.
    """
    global _LOG_ITEMS_MEMO
    if _LOG_ITEMS_MEMO is not None:
        return [dict(rec) for rec in _LOG_ITEMS_MEMO]
    out: list[dict] = []
    index = _load_feed_index()
    fresh: dict = {}
    # ⚠️ Written only when a log was actually re-read. `_log_items` runs several times
    # per subject (read_feed, open_cards), and serialising the whole index on every
    # call cost more than the parse it saves — measured 2026-11-13 at 15 s/subject
    # through the wrapper against 8.6 s for a bare `--tick`.
    dirty = False
    for path in glob.glob(os.path.join(FEED_DIR, "*.events.jsonl")):
        name = os.path.basename(path)
        try:
            st = os.stat(path)
        except OSError:
            continue
        cached = index.get(name)
        opened: dict[str, dict] = {}
        closed: set[str] = set()
        offset = 0
        valid = isinstance(cached, list) and len(cached) == 5
        if valid and cached[0] == st.st_mtime and cached[1] == st.st_size:
            for iid, rec in cached[3]:
                opened[iid] = rec
            closed = set(cached[4])
            fresh[name] = cached
            for iid, rec in opened.items():
                out.append(dict(rec, state="answered" if iid in closed else "open"))
            continue
        if valid and isinstance(cached[2], int) and 0 <= cached[2] <= st.st_size:
            offset = cached[2]
            for iid, rec in cached[3]:
                opened[iid] = rec
            closed = set(cached[4])
        try:
            with open(path, "rb") as fh:
                fh.seek(offset)
                raw = fh.read()
        except OSError:
            continue
        cut = raw.rfind(b"\n")
        chunk = raw[: cut + 1] if cut >= 0 else b""
        dirty = dirty or bool(chunk)
        for line in chunk.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                r = json.loads(line)
            except (json.JSONDecodeError, UnicodeDecodeError):
                continue
            iid = r.get("item_id")
            if not iid:
                continue
            if r.get("type") == "open":
                opened[iid] = r
            else:
                closed.add(iid)
        fresh[name] = [
            st.st_mtime,
            st.st_size,
            offset + len(chunk),
            [[iid, rec] for iid, rec in opened.items()],
            list(closed),
        ]
        for iid, rec in opened.items():
            out.append(dict(rec, state="answered" if iid in closed else "open"))
    if dirty or len(fresh) != len(index):
        _save_feed_index(fresh)
    _LOG_ITEMS_MEMO = out
    return out


def _store_items() -> list[dict]:
    """Open items from the attention store, or raise `StoreUnreachable`."""
    try:
        with urllib.request.urlopen(
            STORE_URL + "/api/1.0/attention", timeout=STORE_TIMEOUT
        ) as resp:
            return json.loads(resp.read().decode("utf-8") or "[]")
    except (urllib.error.URLError, OSError, ValueError) as exc:
        raise StoreUnreachable(str(exc)) from exc


def _normalize_store_item(item: dict, events: dict[str, dict]) -> dict | None:
    """One store item as the record shape `is_open_gate` already reads.

    The store is a THIRD producer of one shape, not a second code path: the park
    rule stays untouched, so the store and the event log cannot drift apart in
    classification. Returns None for an item whose mechanism has no `kind` here.

    The event-time fields (`pane`, `cwd`, `host`, `transcript`) belong to the event,
    not the session, so the store has no room for them — they are joined back from
    the log on `dedup_key`, which the watcher sets to the log's `item_id`.
    """
    kind = KIND_FROM_MECHANISM.get(item.get("answer_mechanism") or "")
    if kind is None:
        return None
    ev = events.get(item.get("dedup_key") or "") or {}
    rec = {
        "item_id": item.get("dedup_key") or item.get("item_id"),
        "store_item_id": item.get("item_id") or "",
        "session_id": item.get("producer_id") or "",
        "kind": kind,
        "detail": item.get("payload") or "",
        "state": "open" if item.get("state") == "open" else "answered",
        "ts": item.get("created_at") or 0,
        "source": "store",
    }
    # Absent stays absent: only fields the event actually carried are copied, so a
    # missing pane is missing rather than defaulted to something plausible.
    for key in ("pane", "cwd", "host", "transcript", "event", "options", "tool_name"):
        if ev.get(key) is not None:
            rec[key] = ev[key]
    return rec


def _keep(out: dict[str, dict], rec: dict) -> None:
    """One record per session, preferring a raised gate over any other item.

    A session can hold several items at once, and `is_open_gate()` reads a single
    record — so a session whose first-seen item is an `idle` record must still park
    if a later item is a real gate.
    """
    sid = rec.get("session_id")
    if not sid:
        return
    prev = out.get(sid)
    if prev is None or (not is_open_gate(prev) and is_open_gate(rec)):
        out[sid] = rec


def read_feed() -> dict[str, dict]:
    """{sessionId: record} from the SAME three sources, in the SAME order, as
    `who-needs-me.py`.

    ⚠️ WHY THIS READS THREE SOURCES AND NOT ONE. Until 2026-09-30 this function
    globbed `*.needs.json` ALONE. That format stopped being written fleet-wide
    around 2026-09-20 (measured 2026-09-30: 28 files, none newer, against 921 live
    `*.events.jsonl`), so the feed was EMPTY and the gate's only remaining park
    signal was the registry's `waiting` status. `liveness_of()` consults this feed
    in BOTH branches, but only the no-registry branch has no fallback — so a session
    with no live registry record (a headless worker) that held a real gate was
    structurally unreachable as PARKED and rendered `🔄 progressing`. The one surface
    built to say what needs the operator reported work-in-progress where a human was
    the blocker.

    ORDER IS SEMANTIC, not cosmetic — it is `who-needs-me.py`'s, re-derived from its
    source on 2026-09-30 (`needs_source()` / `load()` / `load_events()`):

      1. **the attention store** (`$SWEEP_GATE_STORE_URL`, default `localhost:18080`)
         — primary, and it WINS when it answers, because it resolves the producer's
         liveness server-side and drops dead askers as a side effect of the read.
      2. **`<sid>.events.jsonl`** — the store's FALLBACK when it is unreachable, and
         the source that merges open `idle` items the store never carries. The store
         pushes only `permission` and `question`; a store-only read would silently
         drop every gate that arrives as `idle`.
      3. **`<sid>.needs.json`** — the LEGACY one-file-per-session snapshot, merged
         LAST and only for sessions the primary source does not already cover. A
         legacy row for a covered session is a stale duplicate, not a second item:
         it holds one record per session, so merging it re-adds a row the store has
         already resolved.

    ABSENT-SOURCE BEHAVIOUR. An unreachable store is not an error and not an empty
    read: it falls through to the event log, and every record that fallback produced
    is stamped `replayed` so no caller can mistake it for a live store read. An
    unreadable or missing feed directory yields nothing from that source while the
    others still answer. A source that answers with nothing contributes nothing.
    """
    out: dict[str, dict] = {}
    events = {r["item_id"]: r for r in _log_items() if r.get("item_id")}
    covered: set[str] = set()

    try:
        items = _store_items()
    except StoreUnreachable:
        # The fallback ANNOUNCES itself in the record it produced: a replayed gate
        # that said nothing would be indistinguishable from a live one.
        for rec in events.values():
            stamped = dict(rec, replayed=True)
            _keep(out, stamped)
            covered.add(stamped.get("session_id"))
    else:
        for item in items:
            rec = _normalize_store_item(item, events)
            if rec is None:
                continue
            _keep(out, rec)
            covered.add(rec.get("session_id"))
        for ev in events.values():
            if ev.get("kind") == "idle":
                _keep(out, ev)

    for path in glob.glob(os.path.join(FEED_DIR, "*.needs.json")):
        try:
            with open(path, encoding="utf-8") as fh:
                d = json.load(fh)
        except (OSError, json.JSONDecodeError):
            continue
        if not d.get("session_id") or d.get("session_id") in covered:
            continue
        _keep(out, d)
    return out


def is_open_gate(rec: dict | None) -> bool:
    """The attention feed's own park rule — who-needs-me.py's `is_open_gate` core.

    `state: "open"` alone is NOT a park: the dominant record shape is
    `kind: idle, state: open`, which means "turn ended, waiting for a prompt".
    A park is a RAISED GATE the operator has not answered.
    """
    if not rec:
        return False
    if rec.get("state") == "answered":
        return False
    if rec.get("kind") not in ("permission", "question"):
        return False
    if rec.get("event") == "Stop":
        # A RENDERED CLOSER PANEL, not a parked gate — `who-needs-me.py`'s
        # `is_rendered_panel()`, mirrored here so the two readers cannot disagree
        # about which rows are blocks. `kind` cannot tell them apart: the `Stop`
        # panel path and the `Notification` elicitation path both write
        # `kind="question"` with an identical `cleared_by`. The writer is recorded in
        # `event`: `Stop` means the session ended its turn and rendered a line
        # describing what waiting looks like; anything else is a real parked call.
        # A record with NO `event` predates the marker and keeps the old behaviour —
        # counted as a gate — so nothing is silently reclassified by this rule.
        #
        # ⚠️ This became load-bearing on 2026-09-30, when `read_feed()` was widened to
        # the store and the event log. Measured the same day: of 16 live records the
        # widened feed classified as gates, 5 carried `event: "Stop"` — closer lines
        # (`pick — …`, `you run: …`, `review: …`). Without this rule the widening
        # would have parked every session that merely ended its turn, which is the
        # over-report this topic had already recorded, reintroduced by the fix for
        # the under-report.
        return False
    detail = " ".join(_ENTITY.sub(" ", rec.get("detail") or "").split())
    return not detail.startswith(PARKED_VERBS)


# --------------------------------------------------------------------------- #
# the open-card invariant (SC1)
# --------------------------------------------------------------------------- #

def open_cards() -> list[dict]:
    """EVERY open gate on the attention feed — deliberately NOT `read_feed()`.

    ⚠️ `read_feed()` cannot answer SC1's question. `_keep()` collapses the feed to
    ONE record per session on purpose (it prefers a raised gate over any other item),
    so a subject holding three open cards would count as one, and a subject whose
    card is not its session's *preferred* item would count as zero. SC1 asks how many
    open cards are attributable to a subject — a question about the CARD set, not the
    session set.

    Same three sources, same order, same fallback rules as `read_feed()`; the only
    difference is that nothing is collapsed. The store is still the primary and still
    WINS when it answers; an unreachable store still falls through to the event log
    and still stamps what it returns `replayed`.
    """
    out: list[dict] = []
    events = {r["item_id"]: r for r in _log_items() if r.get("item_id")}
    covered: set[str] = set()
    try:
        items = _store_items()
    except StoreUnreachable:
        for rec in events.values():
            out.append(dict(rec, replayed=True))
            covered.add(rec.get("session_id"))
    else:
        for item in items:
            rec = _normalize_store_item(item, events)
            if rec is None:
                continue
            out.append(rec)
            covered.add(rec.get("session_id"))
        for ev in events.values():
            if ev.get("kind") == "idle":
                out.append(ev)
    for path in glob.glob(os.path.join(FEED_DIR, "*.needs.json")):
        try:
            with open(path, encoding="utf-8") as fh:
                d = json.load(fh)
        except (OSError, json.JSONDecodeError):
            continue
        sid = d.get("session_id")
        if not sid or sid in covered:
            continue
        # A `*.needs.json` carries NO `item_id` — its writer emits `session_id`,
        # `kind`, `detail`, `pane` and no card identity at all. Appending it raw
        # made every such card collapse into one `None` key in any id-keyed
        # consumer (measured 2026-11-13: 7 of 7509 records on a frozen corpus).
        # One needs.json exists per session, so the session id IS its identity;
        # the prefix keeps it from colliding with a real `item_id` and from
        # matching a subject slug in `cards_for_subject()`'s `:`-segment test.
        out.append(dict(d, item_id=f"needs:{sid}"))
        covered.add(sid)
    return [r for r in out if is_open_gate(r)]


def manager_subject(producer_sid: str) -> str | None:
    """SC1's first attribution signal: the subject a producer session manages.

    Absent, unreadable or malformed -> None, never a guess. A wrong attribution is
    not symmetric: it either suppresses a card that is owed, or raises one that is
    not, and both are worse than falling through to the dedup-key signal.
    """
    if not producer_sid:
        return None
    path = os.path.join(WORKER_MANAGER_DIR, f"{producer_sid}.json")
    try:
        with open(path, encoding="utf-8") as fh:
            d = json.load(fh)
    except (OSError, json.JSONDecodeError):
        return None
    subject = d.get("subject") if isinstance(d, dict) else None
    return str(subject) if subject else None


def cards_for_subject(subject: str, cards: list[dict]) -> list[dict]:
    """SC1's attribution rule — both signals, in the order SC1 states them.

    1. the card's producer session is registered as this subject's manager; or
    2. the card's dedup key carries this subject's slug as one of its `:`-separated
       segments (`under-target:mdm-bugs:<digest>` is the live shape).

    ⚠️ This is what separates "count of cards attributable to the subject" from
    "count of all cards". SC1's fixture (ii) — a board holding one card for ANOTHER
    subject — is the case that pins it, and a count over all cards passes (i) and
    (iii) while failing (ii).
    """
    want = slug(subject)
    out: list[dict] = []
    for rec in cards:
        if manager_subject(rec.get("session_id") or "") == subject:
            out.append(rec)
            continue
        if want and want in (rec.get("item_id") or "").split(":"):
            out.append(rec)
    return out


def subject_has_manager(subject: str) -> bool:
    """Ownership test: does any manager registration name this subject?

    `manager_subject()` answers session -> subject; ownership needs the inverse, so
    this walks the registry. Deliberately distinct from `cards_for_subject()` above,
    which asks whether the subject has a card of its own — widening THAT test would
    suppress owned blocked subjects instead of unowned ones.

    A subject nothing registers is the defect this exists for: `sweep-gate` used to
    raise "blocked on you, and the board carries no card for it" for a subject with
    no manager at all, so the card was addressed to nobody. Absent or unreadable
    registry -> False, the conservative answer: emit only when an owner exists.

    ⚠️ Declared, not live: `session_in_flight()` needs a registry this call site does
    not carry, so a registration left behind by a dead manager still counts as an
    owner. That is a narrower fix than "a LIVE owner", and it is the one that keeps
    this function cheap enough for the per-subject tick path.
    """
    want = (subject or "").strip()
    if not want:
        return False
    try:
        names = os.listdir(WORKER_MANAGER_DIR)
    except OSError:
        return False
    for name in names:
        if not name.endswith(".json"):
            continue
        if manager_subject(name[: -len(".json")]) == want:
            return True
    return False


def cached_verdicts(subject: str) -> dict[str, str]:
    """The audit verdict cached for each of the subject's rows, by task name.

    Read inline rather than shelled out, for the same reason HOLDS_PATH is
    (:222-230). An absent or unreadable cache yields `{}`, which makes
    `subject_blocked_on_operator` fall back to its bucket and hold readings alone —
    the conservative direction, because a missing cache must never turn every row
    into "blocked on the operator".
    """
    path = os.path.join(PREDISPATCH_DIR, f"{slug(subject)}.verdicts.json")
    try:
        with open(path, encoding="utf-8") as fh:
            d = json.load(fh)
    except (OSError, json.JSONDecodeError):
        return {}
    if not isinstance(d, dict):
        return {}
    return {
        str(k): str((v or {}).get("verdict") or "")
        for k, v in d.items()
        if isinstance(v, dict)
    }


def subject_blocked_on_operator(tracked: list[dict], subject: str) -> bool:
    """SC1's predicate: EVERY non-terminal row's next move is the operator's.

    Three ways a row qualifies, and all three are needed:
      * its bucket key is `waiting` / `approval`;
      * it carries the operator's `HELD` overlay — a LABEL change that keeps its
        ordinary key (`progressing`), so `bucket()` alone cannot see it;
      * its cached audit verdict is `needs-you` / `below-bar` — which `bucket()`
        cannot see either, because an audit verdict is the manager layer's judgement
        and not a property of the task file.

    A subject with ANY row that is none of those — `progressing`, a startable
    `ready`, `problem`, `unregistered`, `upstream`, `hold`, `backlog` — is NOT
    blocked on the operator, and the invariant must not fire for it. That is the
    control keeping this from firing on every subject with nothing in flight.
    """
    rows = [t for t in tracked if bucket(t)[1] != "done"]
    if not rows:
        return False
    verdicts = cached_verdicts(subject)
    for t in rows:
        if bucket(t)[1] in OPERATOR_BUCKETS:
            continue
        if is_held(t.get("session") or ""):
            continue
        if verdicts.get(str(t.get("name") or "")) in OPERATOR_VERDICTS:
            continue
        return False
    return True


def row_set_key(subject: str, tracked: list[dict]) -> str:
    """The identity of the ask: every non-terminal row, and how each one reads.

    Order-free (sorted), so a reorder is not a change; a row joining, leaving, or
    moving between bucket / held / verdict IS a change. That is the invariant's
    bound — **set-change, never a tick count** (the under-target precedent's rule,
    `scripts/under-target-state.py`: a count would re-ask a declined question on a
    timer, which is the defect this removes). A fire-once marker that never clears
    cannot see a change and fails SC1(b).
    """
    verdicts = cached_verdicts(subject)
    rows = []
    for t in tracked:
        _, key = bucket(t)
        if key == "done":
            continue
        name = str(t.get("name") or "")
        rows.append(
            [name, key, is_held(t.get("session") or ""), verdicts.get(name) or ""]
        )
    rows.sort()
    blob = json.dumps(rows, separators=(",", ":"), ensure_ascii=True)
    return hashlib.sha256(blob.encode()).hexdigest()[:16]


def invariant_card(subject: str, tracked: list[dict], cards: list[dict]) -> dict | None:
    """SC1's card payload, or None when the invariant does not fire.

    Fires on the CONJUNCTION — the subject is blocked on the operator AND has zero
    open cards of its own. The zero is over `cards_for_subject`, never over all
    cards; SC1's fixture (ii) is what pins that difference.

    ⚠️ The settled-ask memory closes the re-arm: answering a card moves it from OPEN
    to ANSWERED, `cards_for_subject()` goes empty, and the conjunction re-fires on
    the very next tick — the same ask, re-posted (measured 2026-10-09: 29 posts in
    23.5 h). So before returning a card, the record of the ask last posted is read;
    if its row-set identity is unchanged the ask was answered and is withheld. The
    record is written when the card is produced, so deleting it re-posts (the
    negative control), and a changed row set re-posts (the positive control).
    """
    if cards_for_subject(subject, cards):
        return None
    if not subject_has_manager(subject):
        # The defect this fixes: a card for a subject nothing owns is addressed to
        # nobody, so every armed manager used to receive a gate it could not answer.
        print(
            f"skip {subject}: resolves no owner — no manager registration names it",
            file=sys.stderr,
        )
        return None
    if not subject_blocked_on_operator(tracked, subject):
        return None
    # The settled-ask read — inside `invariant_card()`, so both the `--tick` and the
    # `--invariant` (fleet loop) callers honour it.
    row_key = row_set_key(subject, tracked)
    if load_settled(subject).get("row_key") == row_key:
        print(
            f"suppress {subject}: settled ask unchanged — already answered",
            file=sys.stderr,
        )
        return None
    rows = [t for t in tracked if bucket(t)[1] != "done"]
    card = {
        "dedup_key": f"open-card-invariant:{slug(subject)}",
        "payload": f"{subject} is blocked on you, and the board carries no card for it.",
        "context": (
            f"All {len(rows)} non-terminal row(s) of {subject} are waiting on the "
            f"operator, and the attention board holds no open card attributable to "
            f"it — so the subject reports itself operator-blocked with nothing for "
            f"the operator to answer."
        ),
        "options": ["Look at the blocked rows now", "Leave it — I know about this"],
        "recommend": "Look at the blocked rows now",
    }
    save_settled(subject, row_key)
    return card


def is_external_wait(rec: dict | None) -> bool:
    """A live worker that STOPPED and declared a MACHINE-watched wait.

    The class this exists for, measured 2026-10-03 (tick 123): a worker whose turn
    had ended with a background watch armed, its task file frozen, its gate already
    cleared. It rendered `🔄 progressing` — because `session_in_flight()` cannot tell
    "mid tool call" from "turn ended, watcher armed": a `run_in_background` Bash task
    is an OPEN tool marker that never closes, so the row satisfied the in-flight
    branch, which by design returns `progressing`. The information that the turn had
    ENDED existed at classification time and was discarded at render time — the same
    shape as the ownerless-row defect, a cell whose label does not match the state it
    is used for.

    ⚠️ `event == "Stop"` is required, and it is the SAME discriminator `is_open_gate()`
    reads to reach the opposite conclusion. A `Stop` record is a RENDERED CLOSER
    PANEL, not a raised gate: the worker is asking the operator nothing. Without this
    guard, a genuine gate whose text quoted a `⏰ Ends:` line would be reclassified as
    a machine wait and lose its `⌛ waiting-on-human` cell — and a lost park is the
    under-report this file has already been bitten by once.

    ⚠️ RENDER-ONLY, like `unregistered_park`. The flag is not a `digest_of` input, so
    the digest stays byte-identical to the one this gate computed before the branch
    existed — which is what keeps `scripts/manager-predispatch.py` (the plugin's port,
    whose docstring requires the two not drift) in lockstep with no matching edit.
    """
    if not rec:
        return False
    if rec.get("event") != "Stop":
        return False
    return EXTERNAL_WAIT_MARKER in (rec.get("detail") or "")


# Distinguishes "read the store for me" from a caller passing its own verdict — including
# one that is `None`, which is a real answer ("could not read") and must not be mistaken for
# "you read it".
_HEARTBEAT_UNREAD = object()


def liveness_of(
    sid: str, registry: dict, feed: dict, heartbeat: bool | None = _HEARTBEAT_UNREAD
) -> str:
    """live / parked / none for one session id, from the registry PLUS the heartbeat.

    The registry answers for every session that holds a socket — every interactive one — and
    the heartbeat answers for the headless workers it structurally cannot see. Neither
    substitutes for the other, and `heartbeat` lets a caller that already read the store pass
    its verdict in rather than re-stat per task.
    """
    if not sid:
        return LIVENESS_NONE
    rec = registry.get(sid)
    if not rec or not rec["alive"]:
        # The headless case, and the reason this is no longer an immediate NONE. A headless
        # worker has no registry record at all, so "absent from the registry" was answering
        # "no session" for a worker that was mid-turn — which `session_cell` renders as `—`
        # and `bucket()` reads, on a `next` task, as 🚀 ready. A fresh heartbeat is the only
        # thing that can tell a live headless worker from an absent session here.
        beat = heartbeat_live(sid) if heartbeat is _HEARTBEAT_UNREAD else heartbeat
        if beat is not True:
            return LIVENESS_NONE
        return LIVENESS_PARKED if is_open_gate(feed.get(sid)) else LIVENESS_LIVE
    if is_open_gate(feed.get(sid)) or rec["status"] == "waiting":
        return LIVENESS_PARKED
    return LIVENESS_LIVE


def liveness_of_set(ids: list[str], registry: dict, feed: dict) -> str:
    """The row's verdict over its WHOLE id set, not just the current owner.

    The runbook's caller probes every id the row records and takes LIVE on ANY of them
    as a live owner; the reader collects the same set at step 3. Precedence is
    live > parked > none, so a dead `claude_session_id` cannot mask a live
    `metrics_sessions` id.
    """
    verdicts = {liveness_of(sid, registry, feed) for sid in ids}
    for want in (LIVENESS_LIVE, LIVENESS_PARKED):
        if want in verdicts:
            return want
    return LIVENESS_NONE


def enrich_liveness(tracked: list[dict], registry: dict, feed: dict) -> None:
    for t in tracked:
        ids = t.get("session_ids")
        if ids is None:
            ids = [t["session"]] if t.get("session") else []
        t["liveness"] = liveness_of_set(ids, registry, feed)


def open_box_count(met: str) -> int:
    """Open boxes from the `n/m` reading `checkbox_count` emits.

    `checkbox_count` returns `—` for a section holding no boxes. That is zero open
    boxes, not a parse failure, and the runbook's qualifier is `>=1 open box`, so both
    read the same way here.
    """
    if "/" not in met:
        return 0
    done, _, total = met.partition("/")
    try:
        return int(total) - int(done)
    except ValueError:
        return 0


def idle_stuck(t: dict, now_ts: float) -> bool:
    """Branch (b): idle past the threshold, in `execution`, holding an open box.

    Four gates, all required. The `phase: execution` and `>=1 open box` qualifiers are
    the runbook's own. The idle duration is the task file's mtime — the rule's
    *"task file unchanged"* measure, since a write to the file is precisely what makes
    it changed, and the reader's shipped branch (b) derives the same duration from the
    same signal.

    ⚠️ The parked guard is the one WAITING carrier this function can see, and it is
    here so the two renderers' `apply_stuck` bodies stay identical — the plugin's
    `manager-predispatch.py` carries the same guard. The other two carriers are NOT
    visible at this point and are handled downstream: `apply_blocked_upstream` runs
    AFTER `apply_stuck` in `main()`, and `_is_waiting` is what suppresses the ⚠️ cell
    for a blocked or deferred row. This function therefore sets `stuck` more broadly
    than `_classify` renders it, which is the existing division of labour, not a new
    one.
    """
    if t.get("liveness") == LIVENESS_PARKED:
        return False
    if t.get("phase") != "execution":
        return False
    if open_box_count(t.get("met", "—")) < 1:
        return False
    mtime = t.get("mtime")
    if mtime is None:
        return False
    return (now_ts - float(mtime)) >= STUCK_SECONDS


def apply_stuck(
    tracked: list[dict], registry: dict, prev_busy: dict, now_ts: float
) -> dict:
    """Set `t['stuck']` and return the busy-since map to persist.

    ⚠️ Both branches of the runbook's `stuck` rule, not just the busy one.

    **(a) busy > ~30 min** — the session is continuously busy AND the task's
    `# Progress` section has not moved. The registry gives the current status but not
    its duration, so the clock is observed across ticks; a Progress write resets it,
    which is what keeps a working worker off the ⚠️ row.

    **(b) idle > ~30 min in `phase: execution` with an open box** — see `idle_stuck`.
    It carries no persisted map, because its duration is the task file's mtime and an
    mtime is absolute: there is no clock to observe across ticks.

    The branches are disjoint — (b) is reached only when the session is not busy — so
    no row can be flagged by both.
    """
    new_busy: dict[str, dict] = {}
    for t in tracked:
        rec = registry.get(t["session"]) if t["session"] else None
        busy = bool(rec and rec["alive"] and rec["status"] == "busy")
        if not busy:
            t["stuck"] = idle_stuck(t, now_ts)
            continue
        prev = prev_busy.get(t["name"])
        if prev and prev.get("progress") == t["progress_hash"]:
            since = float(prev.get("since", now_ts))
            t["stuck"] = (now_ts - since) >= STUCK_SECONDS
            new_busy[t["name"]] = {"since": since, "progress": t["progress_hash"]}
        else:
            t["stuck"] = False
            new_busy[t["name"]] = {"since": now_ts, "progress": t["progress_hash"]}
    return new_busy


def blocker_statuses(vault: str) -> dict[str, str]:
    """Every task file's `status`, keyed by task name.

    Vault-wide, not tracked-set-wide: a `blocked_by` entry may name a task sitting
    outside this topic's tracked set, and resolving only within the set would read a
    genuine blocker as missing — which the counting rule below then treats as NOT
    completed, so the row would come out right for the wrong reason and the lookup
    would silently stop mattering. The runbook's own rule names this case: *"the
    blocker may sit outside the tracked set"*.
    """
    tasks_dir = os.path.join(vault, "25 Tasks")
    out: dict[str, str] = {}
    try:
        entries = sorted(os.listdir(tasks_dir))
    except OSError:
        return out
    for entry in entries:
        if not entry.endswith(".md"):
            continue
        try:
            with open(
                os.path.join(tasks_dir, entry), encoding="utf-8", errors="replace"
            ) as fh:
                fm = split_frontmatter(fh.read())
        except OSError:
            continue
        out[entry[:-3]] = fm_scalar(fm, "status") if fm else ""
    return out


def apply_blocked_upstream(tracked: list[dict], statuses: dict[str, str]) -> None:
    """Set `t['blocked_upstream']` by the runbook's canonical counting rule.

    Blocked while AT LEAST ONE named blocker is not `completed` — so an `aborted`
    blocker counts as non-terminal (it will never complete), and a blocker whose
    file is missing, unreadable or carries no parseable `status` counts as NOT
    completed. A task declaring no `blocked_by` satisfies the clause VACUOUSLY,
    which is why the empty list yields False rather than True — otherwise every
    blocker-less `next` task would stop being spawnable.
    """
    for t in tracked:
        unmet = [n for n in t.get("blocked_by") or [] if statuses.get(n) != "completed"]
        t["blocked_upstream"] = bool(unmet)
        t["blocked_by_unmet"] = unmet


# --------------------------------------------------------------------------- #
# stored state + fail-open
# --------------------------------------------------------------------------- #


def slug(topic: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", topic.lower()).strip("-")


def state_path(topic: str) -> str:
    return os.path.join(STATE_DIR, f"{slug(topic)}.json")


def tick_path(topic: str) -> str:
    """The operator-facing tick file — the table's home now that stdout is a wake."""
    return os.path.join(STATE_DIR, f"{slug(topic)}.tick.txt")


def card_path(topic: str) -> str:
    """The card payload `--tick` hands its wrapper — deliberately not on stdout.

    `--tick`'s stdout is the Monitor's event stream, so a JSON card there would be
    read as a verdict. A side file keeps the two channels apart and lets the wrapper
    consume the card from the run that already resolved the tree, instead of paying
    a second full resolution per subject — the second half of the loop's pass cost.
    """
    return os.path.join(STATE_DIR, f"{slug(topic)}.card.json")


def settled_path(subject: str) -> str:
    """The settled-ask record for one subject — the memory the invariant lacked."""
    return os.path.join(SETTLED_DIR, f"{slug(subject)}.json")


def load_settled(subject: str) -> dict:
    """The recorded ask identity, or {} — an unreadable record never raises.

    Same contract as `load_state()`: a missing or half-written file reads as "no
    memory", which re-posts. That is the safe direction — the defect being fixed is
    a re-post, never a silence.
    """
    try:
        with open(settled_path(subject), encoding="utf-8") as fh:
            data = json.loads(fh.read())
    except (OSError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def save_settled(subject: str, row_key: str) -> None:
    """Record the ask just posted. Atomic, so a reader never sees half a record."""
    os.makedirs(SETTLED_DIR, exist_ok=True)
    path = settled_path(subject)
    tmp = path + ".tmp"
    payload = {
        "subject": subject,
        "row_key": row_key,
        "recorded_at": datetime.now().astimezone().isoformat(timespec="seconds"),
    }
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2)
    os.replace(tmp, path)


def write_card(topic: str, card: dict | None) -> None:
    """Write the card, or REMOVE the file when the invariant does not fire.

    Removal rather than an empty file: a payload left from an earlier tick would
    re-post a card for a subject that has since been answered. Atomic, so a reader
    never sees half a payload.
    """
    path = card_path(topic)
    if card is None:
        try:
            os.remove(path)
        except FileNotFoundError:
            pass
        return
    os.makedirs(STATE_DIR, exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(card, fh, indent=2)
    os.replace(tmp, path)


def load_state(topic: str) -> dict:
    """The raw stored payload, or {} — used for the cross-tick busy-since map."""
    try:
        with open(state_path(topic), encoding="utf-8") as fh:
            data = json.loads(fh.read())
    except (OSError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def load_stored(topic: str) -> tuple[str | None, str | None]:
    """Return (digest, fail_reason). fail_reason set => force a full sweep."""
    path = state_path(topic)
    if not os.path.exists(path):
        return None, "first run — no state file"
    try:
        with open(path, encoding="utf-8") as fh:
            raw = fh.read()
    except OSError as exc:
        return None, f"state unreadable ({exc.strerror or exc})"
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        return None, f"state parse error ({exc.msg})"
    if not isinstance(data, dict) or "digest" not in data:
        return None, "state parse error (no digest key)"
    return str(data["digest"]), None


def save_stored(
    topic: str, digest: str, tracked: list[dict], busy: dict
) -> None:
    os.makedirs(STATE_DIR, exist_ok=True)
    payload = {
        "topic": topic,
        "digest": digest,
        "members": len(tracked),
        "busy_since": busy,
        "recorded_at": datetime.now().astimezone().isoformat(timespec="seconds"),
    }
    tmp = state_path(topic) + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2)
    os.replace(tmp, state_path(topic))


def strip_links(text: str) -> str:
    """Drop OSC 8 hyperlink escapes, keeping the text they wrapped.

    The jump link's URI carries the jump token, so anything written to disk must
    not carry the escape. `box-table.py` emits OSC8_OPEN + url + OSC8_ST + text +
    OSC8_OPEN + OSC8_ST, so removing the sequences leaves the visible table
    intact and the token absent.
    """
    return re.sub(r"\x1b\]8;[^\x07\x1b]*(?:\x07|\x1b\\)", "", text)


def write_tick(topic: str, frame: str) -> None:
    """Write the frame to the tick file. Atomic, so a reader never sees a half table.

    The on-disk copy is link-free and 0600, and both are set here rather than by a
    caller: the frame's OSC 8 jump links carry the jump token, and a tick file
    lives on disk to be copied, synced or pasted. Setting both at the write means
    no tick rewrite can re-expose the token or widen the mode. The printed frame
    keeps its links -- only this copy is stripped.
    """
    os.makedirs(STATE_DIR, exist_ok=True)
    path = tick_path(topic)
    tmp = path + ".tmp"
    # 0600 at creation rather than by a chmod after it: the file is never
    # world-readable, not even for the instant between the write and the chmod.
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(strip_links(frame).rstrip("\n") + "\n")
    os.replace(tmp, path)


# --------------------------------------------------------------------------- #
# render
# --------------------------------------------------------------------------- #

BUCKETS = {
    "completed": ("✅ done", "done"),
    # An aborted task is an overlay on `done`, not a bucket of its own: it keeps the
    # glyph and the tally, and carries a ` · aborted` suffix so its Status cell is never
    # byte-identical to a completed row's. See runbook § Step 4.
    "aborted": ("✅ done · aborted", "done"),
    "hold": ("⏸️ blocked/hold", "hold"),
    "backlog": ("—", "backlog"),
}

# `role:` values that are NOT spawn targets, copied from
# ${CLAUDE_PLUGIN_ROOT}/commands/manager-loop.md so one word means one thing on both
# paths: a manager does not spawn managers, and a human's work is not a delegate's.
#
# ⚠️ Absence is deliberately NOT in this set. An absent `role:` means "unknown —
# `/open` classifies and writes it", not `human`. Folding absence in here would strip
# READY TO START from every ordinary task, which is a worse defect than the one this
# closes. The gate only ever subtracts.
NON_SPAWNABLE_ROLES = frozenset({"human", "manager"})

GLYPHS = {
    "progressing": "\U0001f504",
    "waiting": "⌛",
    # `waiting-approval` shares the `⌛` glyph with `waiting-on-human` and is
    # discriminated by its label word — the shape `⏸️ blocked/hold` and
    # `⏸️ blocked/upstream` already have. The seven-icon legend is unchanged.
    "approval": "⌛",
    # `parked-on-unregistered-gate` shares the same `⌛` glyph, discriminated by its
    # label word — the third cell to do so. The seven-icon legend is unchanged.
    "unregistered": "⌛",
    # `waiting-external` is the FOURTH cell to share `⌛`, discriminated by its label
    # word, so the seven-icon legend is again unchanged. It is the "someone else must
    # act" family's third member — `waiting-on-human` and `waiting-approval` name a
    # person, and this one names a MECHANISM (a bot review, CI, a deploy, a scheduled
    # job). Measured 2026-10-03, tick 123: a worker whose turn had ended with a
    # background watch armed rendered `🔄 progressing`, which asserts work is
    # happening where the worker had stopped and was waiting on a third party.
    "external": "⌛",
    # `waiting-declared` is the FIFTH cell to share `⌛`, discriminated by its label
    # word, so the seven-icon legend is again unchanged. It names a row whose owner
    # has DECLARED a wait on a named external event — in its own `declared_wait:`
    # frontmatter, or on a manager-side open-items ledger — and which the park cell
    # would otherwise ask whether it is parked.
    "declared": "⌛",
    "hold": "⏸️",
    "problem": "⚠️",
    "ready": "\U0001f680",
    "yours": "\U0001f464",
    "done": "✅",
    "backlog": "◦",
    "other": "—",
    # ⚠️ `upstream` was MISSING here and from BUCKET_ORDER until 2026-09-28, while
    # `_classify` had returned it since the bucket was added. `marker()` builds its
    # parts list by iterating BUCKET_ORDER, so every `⏸️ blocked/upstream` row was
    # counted into `counts` and then silently dropped from the tick marker — the
    # tally undercounted by exactly the blocked rows, the direction that reads as
    # "nothing is parked". Fixed with the same edit that added `approval`; both
    # keys must stay in BUCKET_ORDER or the marker lies again.
    "upstream": "⏸️",
}

BUCKET_ORDER = [
    "progressing",
    "waiting",
    "approval",
    "unregistered",
    "external",
    # ⚠️ Must stay in BUCKET_ORDER or the marker lies — see the `upstream` note in
    # GLYPHS above: a key absent here is counted into `counts` and then silently
    # dropped from the tick marker.
    "declared",
    "hold",
    "problem",
    "ready",
    "yours",
    "done",
    "backlog",
    "upstream",
    "other",
]


# Keys the `deferred` overlay does NOT override. `done` / `hold` / `backlog` are
# definitive dispositions — a finished or explicitly-parked task is not "waiting
# until a date", and overriding one would misreport its real state. `waiting` is
# suppressed for a sharper reason: `⌛ waiting-on-human` is the cell that carries a
# raised gate to the operator, and a `defer_date` must never hide a worker blocked
# on a human — a silent loss in exactly the direction the legend exists to prevent.
DEFER_OVERLAY_EXEMPT = frozenset({"done", "hold", "backlog", "waiting"})


def _parse_defer_date(raw: str) -> date | None:
    """`defer_date` in either stored shape -> a `date`, or None if unparseable.

    Both shapes are live on disk (runbook § Step 4 condition 10): the quoted
    `"YYYY-MM-DD"` and the unquoted RFC3339 datetime vault-cli writes when the
    scalar is left unquoted (`2026-09-26T00:00:00Z`). A quoted-only parse leaves the
    second shape reading as absent — the exact defect this overlay exists to remove,
    since 28 in-progress tasks in this vault carry a `defer_date` and both spellings
    occur among them.
    """
    m = re.match(r"^(\d{4})-(\d{2})-(\d{2})(?:[T ].*)?$", raw.strip().strip("'\""))
    if not m:
        return None
    try:
        return date(int(m.group(1)), int(m.group(2)), int(m.group(3)))
    except ValueError:
        return None


def is_deferred(task: dict) -> bool:
    """True when `defer_date` names a date strictly in the future.

    Absent -> False. UNPARSEABLE -> False, and deliberately **not** because it reads
    as absent: condition 10 *blocks* the auto-resume on an unparseable date and
    reports it by name. The overlay stays off because the date cannot be shown to be
    future, so the row keeps its ordinary cell and the defect stays visible rather
    than being papered over as a defer.
    """
    day = _parse_defer_date(task.get("defer_date") or "")
    return day is not None and day > date.today()


def read_holds() -> dict:
    """The hold store, keyed on session id. `{}` on any read failure.

    Lock-free on purpose: the writer lands every change through `os.replace`, so a
    reader sees the whole old file or the whole new one and never a partial one.

    A missing or corrupt store reads as "nothing is held", and that direction is
    chosen rather than incidental. A hold is the operator's instruction to STOP, so
    the two failure modes are not symmetric: inventing a hold from an unreadable file
    would silently suppress wakes for sessions the operator never held — the exact
    silent suppression this feature exists to remove — while reading a real store as
    empty merely fails to honour a hold, loudly and on the next tick.
    """
    try:
        with open(HOLDS_PATH, encoding="utf-8") as handle:
            data = json.load(handle)
    except (OSError, ValueError):
        return {}
    holds = data.get("holds") if isinstance(data, dict) else None
    return holds if isinstance(holds, dict) else {}


def held_reason(session_id: str) -> str | None:
    """The hold's reason for this session, or None when it is not held."""
    if not session_id:
        return None
    entry = read_holds().get(session_id)
    if not isinstance(entry, dict):
        return None
    return entry.get("reason") or "held"


def _parse_held_at(value: str):
    """The store's `%Y-%m-%dT%H:%M:%SZ` stamp as an aware UTC datetime, or None.

    Duplicated from the writer's `parse_ts()` for exactly the reason `read_holds()` is
    duplicated above: this file is vault-local and does NOT inherit
    ${CLAUDE_PLUGIN_ROOT}, so importing the plugin's copy would be the same fail-open
    the reader's own docstring rejects.
    """
    try:
        return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(
            tzinfo=timezone.utc
        )
    except (TypeError, ValueError):
        return None


def held_age(held_at: str) -> str:
    """How long the operator's policy has stood — `12m` / `4h` / `3d`, `?` unreadable.

    Same units and thresholds as the writer's own `age()`, so the sweep and
    `session-holds.py --list` can never disagree about how old one hold is.

    `?` is a real answer, not a fallback. An entry whose `held_at` is missing or
    malformed is reported as unreadable rather than rendered as age zero, which would
    read as a hold the operator set seconds ago — the one reading that would invite a
    manager to treat an old hold as fresh.
    """
    started = _parse_held_at(held_at or "")
    if started is None:
        return "?"
    seconds = int((datetime.now(timezone.utc) - started).total_seconds())
    if seconds < 0:
        seconds = 0
    if seconds < 3600:
        return "%dm" % (seconds // 60)
    if seconds < 86400:
        return "%dh" % (seconds // 3600)
    return "%dd" % (seconds // 86400)


def held_label(session_id: str) -> str | None:
    """The full Status cell for a held session, or None when it is not held.

    `⏸️ HELD — <reason> · <age>` is the ONE render every consumer owes a held row —
    `session-holds.py`'s module docstring and `skills/hold/SKILL.md` both state it, and
    `session-holds.py --list` already renders it that way. The reason and the age are
    both here because both belong to the operator: the reason is what they said, the
    age is how long they have been saying it.

    ⚠️ The age is not decoration. Without it a hold set three weeks ago and one set
    this morning render identically, so the row cannot answer the only question an
    operator reading their own holds actually has — *is this still what I meant?*
    """
    if not session_id:
        return None
    entry = read_holds().get(session_id)
    if not isinstance(entry, dict):
        return None
    return "⏸️ HELD — %s · %s" % (
        entry.get("reason") or "held",
        held_age(entry.get("held_at") or ""),
    )


def is_held(session_id: str) -> bool:
    """True when this session carries a hold. Never raises."""
    return held_reason(session_id) is not None


def bucket(task: dict) -> tuple[str, str]:
    """One bucket per task, per the runbook's Status legend.

    `liveness` and `stuck` must be set first (enrich_liveness / apply_stuck).

    A future `defer_date` is an **overlay**, not a ninth bucket: it changes the label
    and leaves the key alone, the shape `aborted` already has on `done`. The
    classification is deliberately untouched — the orphan predicate keeps no defer
    exemption, because what holds a deferred orphan is auto-resume condition 10, not
    the bucket.
    """
    label, key = _classify(task)
    # The hold overlay, modelled on `deferred` below: it changes the LABEL and
    # leaves the key alone, so the classification, the counts and the orphan
    # predicate are all untouched. ⚠️ The row stays visible — suppressing it would
    # remove the evidence, and a row that disappears from a sweep is
    # indistinguishable from a row that got fixed. What the hold suppresses is the
    # ACT, and that happens in action_lines(), not here.
    #
    # Unlike `deferred` this overlay is NOT exempted for done/hold/backlog/waiting:
    # a hold is an operator statement about the session, and it must read as held
    # whatever else the row's status says. Rendering `✅ done` for a held session
    # would invite a reap; rendering `⌛ waiting-on-human` would invite an answer.
    # ⚠️ The key is `session`, NOT `claude_session_id`. `read_task()` normalises the
    # frontmatter field `claude_session_id` into the dict key `session` (:365), and this
    # dict never carries the raw field name. Reading `claude_session_id` here compiles,
    # runs, and is INERT — the key is absent on every task, `held_label("")` returns
    # None, and the overlay below never fires. Measured 2026-09-30: this shipped that
    # way, and nothing caught it because the gate's real output was never asserted on —
    # only `sweep-gate-fixture.py` builds tasks in this shape, and it had no hold case.
    held = held_label(task.get("session") or "")
    if held is not None:
        return held, key
    if key not in DEFER_OVERLAY_EXEMPT and is_deferred(task):
        return "⏸️ deferred", key
    return label, key


def _is_waiting(task: dict) -> bool:
    """True when something OUTSIDE the worker is holding the row — WAITING, not STALLED.

    The `stuck` proxy is *the task file's mtime, unchanged*, which is satisfied
    identically by a wedged worker and by one correctly parked on a human or a
    dependency. Without this test the `⚠️ problem` cell nudges a worker whose only
    correct next action is to keep waiting, at a cost of one turn each to
    re-adjudicate — and the runbook's own measured false positives were both of
    this shape (2026-09-27: one waiting on a human, one on a sibling-gated set).

    The discriminating input is whether anything outside the worker is holding it.
    Four carriers, all already computed by the time `_classify` runs:

      - the **parked** verdict — a gate open, or a `later (on <trigger>)` closer. A
        parked row already returns `⌛ waiting-on-human` ABOVE this test, so the
        clause is belt-and-braces; it is kept so the predicate reads as a whole.
      - an **unmet `blocked_by`** — `apply_blocked_upstream`'s own input. This is
        why the test lives here and not in `apply_stuck`: that runs BEFORE
        `apply_blocked_upstream` (main() calls it at 1276 vs 1279), so the flag is
        not yet set at that point and the test would silently never fire.
      - a **future `defer_date`** — `is_deferred`.
      - a **declared wait** — `apply_declared_waits`'s flag: the row's own
        `declared_wait:` frontmatter, or an OPEN entry naming it in a manager-side
        open-items ledger. A row whose owner has written down what it is waiting on
        is held from outside by definition, and its freeze is the wait, not a stall.
        ⚠️ This carrier is ALSO why `_classify` places its cell above `stuck`: the
        clause here keeps `t["stuck"]` false, and the cell keeps the row from falling
        through to `🔄 progressing` — which the runbook rejects for a stopped worker,
        because it asserts work is happening where none is.

    ⚠️ **Suppression only.** The row falls through to `🔄 progressing` and keeps its
    place in the table and in the tally — *"a row that disappears from a sweep is
    indistinguishable from a row that got fixed."* Only the `⚠️` cell and the nudge
    keyed off it are suppressed.

    ⚠️ **This does NOT implement branch (b) of the runbook's `stuck` rule.**
    `apply_stuck` requires the registry to report the session continuously `busy`,
    so an idle session is `stuck = False` there and an idle-but-stalled worker is
    flagged by nothing at all. That is a separate coverage gap, separately owned;
    suppressing a false positive and implementing a missing limb are different
    changes, and folding them together closes the gap in the wrong direction.
    """
    if task.get("liveness") == LIVENESS_PARKED:
        return True
    if task.get("blocked_upstream"):
        return True
    if is_deferred(task):
        return True
    if task.get("declared_wait"):
        return True
    return False


def park_threshold_seconds(topic: str, branch: str) -> tuple:
    """The park cell's staleness threshold — `(seconds, basis, armed)`.

    The rule is *"2 x the cadence interval"* (the runbook's wording), and the cadence
    is the one the manager recorded at its last re-arm: `manager-liveness.py --arm`
    writes `<branch>--<slug>.cadence` into THIS directory — the same `STATE_DIR` and
    the same base transform, plus the branch prefix the writer adds. ⚠️ **The prefix
    is not decoration**: a topic and a goal may share a name, so a flat read silently
    misses every namespaced record and falls back to `STUCK_SECONDS`. So the gate can
    read the cadence the manager actually armed, rather than assuming the 15-minute
    default the prose used to be true of.

    ⚠️ **The fallback is `STUCK_SECONDS`, and the caller is TOLD which basis was
    used — never handed a silent default.** A threshold that quietly reverts to the
    old constant is the same silent-default defect this branch exists to remove, one
    level down: at a 60-minute tick it fires on every row not written in 30 minutes
    while the caller believes the cadence was honoured.

    ⚠️ **`STUCK_SECONDS` itself is NOT changed by this.** It stays the `stuck`
    bucket's constant, because `stuck` IS a `digest_of` input — deriving it from the
    cadence here would move the digest and break the lockstep with
    `scripts/manager-predispatch.py` (the runbook's render-only trap). The park
    branch is render-only, so a park-only threshold keeps the digest byte-identical.
    """
    p = os.path.join(STATE_DIR, f"{branch}--{slug(topic)}.cadence")
    try:
        with open(p, encoding="utf-8") as fh:
            interval = int(fh.read().strip())
        if interval > 0:
            return 2 * interval, f"2 x cadence {interval}s (armed) from {p}", True
    except (OSError, ValueError):
        pass
    return (
        STUCK_SECONDS,
        f"no cadence record at {p}; fixed {STUCK_SECONDS}s",
        False,
    )


def parked_on_unregistered_gate(
    t: dict, now_ts: float, park_seconds: float = STUCK_SECONDS
) -> bool:
    """A live worker parked on a gate the attention feed never registered.

    The class this exists for: a tracked task, non-terminal, in `execution`, with
    open boxes, whose file has not moved for >= 2 cadence intervals, and for which
    the feed carries NO row — so every feed-reading mechanism reads zero and the
    manager's only honest report is "no gate is open", which is indistinguishable
    from a healthy worker. Measured 2026-09-26 (topic `Unattended Execution`): a
    worker sat parked ~4 hours while `who-needs-me.py`'s `Needs you` read 0 and the
    task file's mtime stayed frozen at 10:35:11. One `SendMessage` check-in
    recovered it in about two minutes.

    The four-way discriminator — each branch names the input it reads:

      1. **working (file)** -> the TASK-FILE MTIME DELTA against the PARK
                             THRESHOLD the caller passes (`park_seconds`, 2 x the
                             armed cadence). `now - mtime < park_seconds` means
                             the worker is writing; no fire. With no cadence
                             record the threshold reverts to `STUCK_SECONDS`
                             (30 min) and `main()` announces that basis on
                             stderr — never silently.
      2. **working (session)** -> the DRIVE LEG'S IN-FLIGHT CHECK, reused verbatim
                             (`agents/manager-drive.md:100-108`; see
                             `session_in_flight()`): the session's open tool marker,
                             then its transcript mtime against `LIVE_WINDOW`. A
                             worker running long tool calls has not moved its file,
                             so leg 1 misses it and THIS leg is the one that catches
                             it; no fire.
      3. **genuinely gated** -> the ATTENTION FEED. `is_open_gate()` true means a
                             gate IS registered and its feed row is the report; this
                             branch must not double-report it.
      4. **parked on an unregistered gate** -> the LIVE-OWNER input (the registry
                             verdict). A row with no live session is the orphan /
                             `stuck` path, not a park — there is nobody to check in
                             with, and the remedy below would be a no-op.

    Why this wins over `stuck`, which the same row also satisfies: `stuck`'s
    predicate (`task file unchanged AND no Progress entry`, plus the idle branch)
    holds NO feed input at all, so it cannot tell a wedged worker from one parked
    on a gate the feed cannot carry — and its remedy presumes a wedged worker. This
    branch reads the feed and therefore claims the row; `stuck` keeps every row this
    one does not claim.

    ⚠️ RENDER-ONLY, deliberately. The flag this sets is not a `digest_of` input, so
    the digest is byte-identical to the one this gate computed before the branch
    existed. That is what keeps `scripts/manager-predispatch.py` — the plugin's port
    of this gate, whose docstring requires the two not drift — in lockstep with no
    matching edit there. Adding the flag to the digest would silently make the two
    gates disagree about what "unchanged" means.

    ⚠️ The row is never dropped: only the cell and the action line change. *"A row
    that disappears from a sweep is indistinguishable from a row that got fixed."*
    """
    if t.get("status") in ("completed", "aborted"):
        return False
    if t.get("phase") != "execution":
        return False
    if open_box_count(t.get("met", "—")) < 1:
        return False
    mtime = t.get("mtime")
    if mtime is None:
        return False
    if (now_ts - float(mtime)) < park_seconds:
        return False  # (1) working — the file moved inside two intervals
    if t.get("feed_gate"):
        return False  # (2) a registered gate — the feed row IS the report
    if t.get("liveness") != LIVENESS_LIVE:
        return False  # (3) no live owner — orphan / stuck, not a park
    if t.get("in_flight"):
        return False  # (4) working — the session is mid-tool-call, not parked
    return True


def session_in_flight(sid: str, registry: dict, now_ts: float) -> bool:
    """The drive leg's shipped in-flight check, reused verbatim.

    Three limbs, in the drive leg's own order (`agents/manager-drive.md:100-108`):
    the session registry's `status` first, then the session's open tool marker,
    then the transcript mtime against `LIVE_WINDOW` as the last fallback.

    ⚠️ Reused, not re-derived. That bullet's line 108 reads *"Reuse that shipped
    reading; do not add a fourth definition of 'idle'."* — a check reading only one
    of its limbs would be exactly the new definition it forbids.

    ⚠️ RENDER-ONLY. The caller stores the answer on the row for `_classify`, and
    nothing here is a `digest_of` input, so the digest is byte-identical to the one
    this gate computed before the branch existed — which is what keeps
    `scripts/manager-predispatch.py` in lockstep with no matching edit there.

    ⚠️ `False` is "not proven in flight", never "dead". Ownership is `liveness`'s
    question, taken from the registry by `enrich_liveness()`; a row that is not in
    flight and has no live owner is the orphan path, not this one.
    """
    if not sid:
        return False
    # Limb 1 — the registry's own `status`. Decisive on two of its four values and
    # silent on the other two. Measured 2026-10-05 against a live registry: the two
    # limbs below were wrong in BOTH directions while this one was right in both —
    # six `shell` sessions whose transcript read `quiet` (a long tool call writes
    # nothing), and one `waiting` session the transcript called in flight (a parked
    # worker's re-render moves its transcript inside the window).
    # ⚠️ `busy` is deliberately NOT read as working: `agents/manager-sweep-reader.md`
    # input 11 rejects it as the weaker candidate, on the ground that trusting it
    # while distrusting `idle` is inconsistent — same field, both silent on the loop.
    # ⚠️ `shell` is terminal here, and that is safe ONLY because of precedence:
    # `_classify` evaluates the `waiting-external` branch BEFORE `in_flight`, so a
    # stopped worker holding a `run_in_background` watcher (a child shell that
    # outlives the turn) is claimed by that cell and never reaches this function.
    # The drive leg's copy of this check has no such branch and carries an explicit
    # `⏰ Ends:` carve-out instead — see `agents/manager-drive.md` limb 1.
    rec = registry.get(sid) or {}
    status = rec.get("status")
    if status == "shell":
        return True
    if status == "waiting":
        return False
    # Limb 2 — the session's own tool marker.
    marker = os.path.join(TOOL_MARKER_DIR, f"{sid}.tool.json")
    try:
        with open(marker, encoding="utf-8") as fh:
            state = json.loads(fh.read())
    except (OSError, json.JSONDecodeError):
        state = None
    if isinstance(state, dict) and state.get("state") == "open":
        # ⚠️ The age test is the drive leg's MIDDLE branch, and this copy omitted it
        # until 2026-10-05 — any `open` marker returned True, however old. A marker
        # left by a session that died mid-tool-call therefore read as "in flight"
        # forever. `open` for >= the cap falls through to the stuck path, exactly as
        # `agents/manager-drive.md` prescribes.
        try:
            age = now_ts - float(state.get("ts") or 0)
        except (TypeError, ValueError):
            age = STUCK_MARKER_SECONDS
        if age < STUCK_MARKER_SECONDS:
            return True
    # Limb 3 — marker absent, cleared, or too old. Both transcript shapes are probed
    # because the plugin resolves a transcript as `<projectsDir>/*/<sid>.jsonl`
    # (claude-supervisor CLAUDE.md § Reading a worker), while a fixture pins the flat
    # form.
    candidates = [os.path.join(TRANSCRIPT_DIR, f"{sid}.jsonl")]
    candidates += glob.glob(os.path.join(TRANSCRIPT_DIR, "*", f"{sid}.jsonl"))
    ages = []
    for path in candidates:
        try:
            ages.append(now_ts - os.path.getmtime(path))
        except OSError:
            continue
    if not ages:
        return False
    return min(ages) < LIVE_WINDOW


def apply_unregistered_parks(
    tracked: list[dict],
    feed: dict,
    registry: dict,
    now_ts: float,
    park_seconds: float = STUCK_SECONDS,
) -> None:
    """Set `feed_gate` and `unregistered_park` on every tracked row.

    `feed_gate` is read straight off the feed rather than off `liveness`, because
    `liveness_of()` folds the registry's `waiting` status into `LIVENESS_PARKED` —
    and the whole class this branch exists for is the row where the registry does
    NOT say `waiting` while the feed carries nothing. Reading liveness here would
    re-hide it.
    """
    for t in tracked:
        sid = t.get("session") or ""
        t["feed_gate"] = bool(is_open_gate(feed.get(sid))) if sid else False
        # ⚠️ BEFORE the park verdict: `parked_on_unregistered_gate` declines an
        # in-flight row, so the flag must already be set when it runs.
        t["in_flight"] = session_in_flight(sid, registry, now_ts)
        t["unregistered_park"] = parked_on_unregistered_gate(t, now_ts, park_seconds)


def apply_external_waits(tracked: list[dict], feed: dict) -> None:
    """Set `external_wait` on every tracked row.

    Reads `t["session"]` — the same single owner `apply_unregistered_parks` reads —
    so the two feed-derived flags cannot disagree about whose record they are
    looking at. Deliberately NOT the whole `session_ids` set that `enrich_liveness`
    walks: the class this cell exists for is a row whose CURRENT owner has stopped
    and is waiting, and a stale id from `metrics_sessions` carrying an old `⏰ Ends:`
    line would paint a live worker's row as waiting on a mechanism.
    """
    for t in tracked:
        sid = t.get("session") or ""
        t["external_wait"] = is_external_wait(feed.get(sid)) if sid else False


def apply_declared_waits(tracked: list[dict]) -> None:
    """Set `declared_wait` on every tracked row — branch (1)'s input, ONE channel.

    The channel is the row's own `declared_wait:` frontmatter, already on the row from
    `read_task`. A worker that has written down what it is waiting on has answered the
    park cell's question — *are you parked on a gate the feed cannot carry?* — in
    writing, so asking it again is the false positive this carve-out removes.

    ⚠️ **A SECOND channel was implemented and WITHDRAWN 2026-10-05, and the reason is
    kept here so it is not re-derived.** It read the manager-side open-items ledger
    (`~/.claude/state/open-items/<sid>.json`) for an OPEN entry naming the row. It
    failed review on two counts, the second fatal:

      - **The reader cannot key it.** The ledger is per session (`path_for(sid)`); the
        reader is given no session id, the sibling `agents/fleet-sweep-reader.md:44`
        says the id is the caller's and *"this agent may not share it"*, and the
        constraints ban the means of recovering one. A directory scan instead reads
        **every other manager's** asks, suppressing a row on someone else's business.
      - **The store does not record the fact this cell reads.** `scripts/open-items.py`
        defines exactly three kinds — `asked-of-me` (an operator instruction, which
        *"resolves when its task reads `status: completed`"*), `asked-of-you` (a
        question put to the operator) and `pushed` (a task filed on the operator's
        behalf). The first and third are open for the **whole life of the task** and
        mark work that is *under way*, so a wedged, non-parked row carrying one would
        render `⌛ waiting-declared` and emit **no action line** — suppressing the very
        true positive the park cell exists to catch. And none of the three means *"this
        row waits on a named external event"*: the ledger records what a manager
        **owes or has asked**, never what a row **waits on**. No kind filter repairs a
        store that was never recording the fact.

    The ledger-blocked instance this channel was written for is therefore **not**
    covered by this carve-out; it is split into its own task rather than left implied.

    ⚠️ Normalises channel 1's raw string to a bool. `read_task` leaves a string here
    and a truthiness test would work either way, but a flag that is sometimes a string
    and sometimes a bool is the shape that invites a later reader to write `== "true"`.
    ⚠️ A present-but-EMPTY or null-spelled value is **not** a declaration — it resolves
    to no suppression, exactly as an absent field does, the same direction the park
    cell takes on an `unread` input.

    ⚠️ RENDER-ONLY, deliberately, exactly like `unregistered_park` and `external_wait`.
    The flag is not a `digest_of` input, so the digest stays byte-identical to the one
    this gate computed before the branch existed — which is what keeps the plugin's
    port `scripts/manager-predispatch.py` in lockstep with no matching edit there.
    """
    for t in tracked:
        raw = (t.get("declared_wait") or "").strip()
        t["declared_wait"] = bool(raw) and raw not in _YAML_NULLS


def _classify(task: dict) -> tuple[str, str]:
    """The ordinary bucket, before the `deferred` overlay is applied."""
    status = task["status"]
    if status in BUCKETS:
        return BUCKETS[status]
    if task["phase"] == "human_review":
        return "⌛ waiting-on-human", "waiting"
    if task.get("liveness") == LIVENESS_PARKED:
        # The runbook's waiting-on-human is "phase human_review, or the worker asked
        # the human" — a raised gate IS the worker asking.
        return "⌛ waiting-on-human", "waiting"
    if task["phase"] == "todo" and not task.get("operator_flagged"):
        # An operator-flagged `todo` row is APPROVED (ruling 2026-11-15) and falls
        # through to the ordinary cells below.
        # `phase: todo` is the OPERATOR'S APPROVAL BOUNDARY, not a work state: agents
        # file here and stop, and the operator's `todo -> planning` flip is the
        # approval. A `todo` row is therefore waiting on a human by construction and
        # gets its own cell — deliberately NOT folded into `waiting-on-human`, whose
        # ~15-min TTS rule would fire on every unapproved row in the vault, and
        # deliberately NOT left as `ready`, which is what invited the spawn.
        #
        # ⚠️ Placement is load-bearing in both directions. It sits AFTER the parked
        # check, because a worker actually blocked on a gate is a sharper, more
        # actionable human wait than "not approved yet" — the same reason the
        # `deferred` overlay exempts `waiting`. And it sits BEFORE the stuck check,
        # because an unapproved row that has sat unstarted is the INTENDED state, not
        # a problem: measured 2026-09-28, 17 of the 18 rows in `ready-to-start` sat at
        # `phase: todo`, so leaving it below `stuck` would have re-filed the whole
        # unapproved backlog as `⚠️ problem` on every tick.
        #
        # A terminal or definitively-parked row never reaches here: `status in
        # BUCKETS` above already returned `✅ done` / `⏸️ blocked/hold` / `—` for
        # `completed`, `aborted`, `hold` and `backlog`, so a `todo` phase on any of
        # them keeps its disposition, exactly as the `deferred` overlay's four
        # exemptions do.
        return "⌛ waiting-approval", "approval"
    if task.get("external_wait"):
        # A live worker whose turn has ENDED and whose closer declares that a MACHINE
        # will end the wait — a bot review, CI, a deploy, a scheduled job.
        #
        # ⚠️ Placed BEFORE `in_flight`, which is the branch that currently claims this
        # row and the reason the cell is needed at all: a `run_in_background` Bash
        # watcher is an OPEN tool marker that never closes, so `session_in_flight()`
        # reads the STOPPED worker as working and this row rendered `🔄 progressing`,
        # asserting work is happening where none is. Placed AFTER the parked and
        # `waiting-approval` checks because those name a sharper blocker — a raised
        # gate is a person, not a mechanism — and `is_external_wait` already declines
        # every record that is not a `Stop` closer panel, so it cannot steal a gate.
        #
        # The cell reuses the `⌛` glyph, discriminated by its label word — the shape
        # `waiting-on-human`, `waiting-approval` and `parked-on-unregistered-gate`
        # already share, so the seven-icon legend is unchanged.
        #
        # ⚠️ It emits NO action line: the machine resumes the worker, so a nudge would
        # be exactly the false positive the `stuck` exemption exists to prevent. The
        # row keeps its place in the table and in the tally.
        return "⌛ waiting-external", "external"
    if task.get("in_flight"):
        # A live worker running long tool calls, which has NOT moved its task file
        # — so it satisfies every leg of the park rule below and would false-fire
        # as parked. Measured 2026-10-02: the file was frozen 66 min, the feed was
        # silent, the owner was live, and the worker was mid-probe with a 28-second
        # transcript; the manager's own mtime reading was correct when taken and
        # stale by the time the reader re-read it.
        #
        # ⚠️ Placed BEFORE `unregistered_park` AND before `stuck`, and both halves
        # are load-bearing. Before the park cell because that is the false positive
        # this branch removes. Before `stuck` because dropping the row from the park
        # cell alone is NOT the fix: the row still satisfies `stuck`'s predicate and
        # would land in `⚠️ problem` — one false positive traded for a louder one.
        # An in-flight row is WORKING; it renders `progressing`, and the row is
        # never dropped.
        return "\U0001f504 progressing", "progressing"
    if task.get("declared_wait"):
        # A live worker that has DECLARED its wait on a named external event — in its
        # own `declared_wait:` frontmatter, or as an open entry naming the row in a
        # manager-side open-items ledger. It is quiet, not in flight, and the feed is
        # silent because the gate it waits on was never registered — so it satisfies
        # every leg of the park rule and would draw a check-in whose only correct
        # answer is "not parked". Measured on one row three times in one evening
        # (2026-10-02 twice, 2026-10-04 once): every check-in came back *"not parked,
        # booted and working"*, and the row's own `# Progress` had said so in writing.
        #
        # ⚠️ Placed AFTER `in_flight` and BEFORE `unregistered_park`, and both halves
        # are load-bearing. After `in_flight` because a worker genuinely mid-tool-call
        # is WORKING and `progressing` is the honest cell for it; a declared wait
        # describes a row that is not in flight. Before the park cell because that is
        # the false positive this branch removes — and before `stuck`, which the park
        # cell already sits above, because a frozen declared-wait row satisfies
        # `stuck` too: dropping it from the park cell alone would trade one false
        # positive for the louder `⚠️ problem`, the same trap the `in_flight` branch
        # records.
        #
        # The cell reuses the `⌛` glyph, discriminated by its label word — the shape
        # `waiting-on-human`, `waiting-approval`, `waiting-external` and
        # `parked-on-unregistered-gate` already share, so the seven-icon legend is
        # unchanged.
        #
        # ⚠️ It emits NO action line, for the `external` branch's reason: the row waits
        # on something outside itself, so a check-in would ask a question whose answer
        # is already written down.
        return "⌛ waiting-declared", "declared"
    if task.get("unregistered_park"):
        # A live worker parked on a gate the feed never registered. Placed BEFORE
        # the `stuck` check and AFTER the parked / `waiting-approval` checks:
        #  - after `parked`, because a registered gate is the sharper cell and this
        #    branch already declines a row whose `feed_gate` is set;
        #  - after `waiting-approval`, because an unapproved row is the operator's
        #    inbox and is in its INTENDED state, not parked on anything;
        #  - before `stuck`, because `stuck`'s predicate holds no feed input and
        #    would otherwise claim this row and prescribe a wedged-worker remedy.
        # The cell reuses the `⌛` glyph, discriminated by its label word — the shape
        # `waiting-on-human` and `waiting-approval` already share.
        return "⌛ parked-on-unregistered-gate", "unregistered"
    if (
        task.get("role") in NON_SPAWNABLE_ROLES
        and not task.get("blocked_upstream")
        and task.get("liveness") != LIVENESS_LIVE
    ):
        # The role carve-out is PHASE-INDEPENDENT. It is not a property of the
        # `ready-to-start` offer, and scoping it there — where it sat until
        # 2026-10-15 — left a sessionless `role: human` row at `phase: execution`
        # rendering `🔄 progressing`, or `⚠️ problem` via the `stuck` branch below.
        # Both cells are false: `progressing` asserts work is happening with nobody
        # to do it, and `problem` prescribes a nudge with no target. The runbook's
        # § Step 4 bullet carries no phase qualifier, and `agents/manager-drive.md`
        # and `commands/steps/manager-spawn.md` state the same carve-out
        # unconditionally — this branch is what makes the two CLASSIFIERS agree
        # with them. Measured 2026-10-15 on [[Prove the Spawn Ledger End to End on
        # the Live Fleet]]: `🔄 progressing` at 08:26, `⚠️ problem` at 09:13.
        #
        # ⚠️ Placed BEFORE `stuck` and BEFORE the `ready-to-start` arm, and both
        # halves are load-bearing. Before `stuck` because a frozen sessionless
        # role'd row satisfies that predicate and would otherwise be filed as the
        # louder `⚠️ problem`. Before the offer arm because that arm is scoped to
        # `next` / `planning`, which is the defect itself.
        #
        # ⚠️ AFTER `blocked_upstream` — that is why the guard is a condition here
        # rather than the branch being unconditional. The runbook and
        # `manager-sweep-reader.md` both place blocked-upstream FIRST ("a role'd
        # task that is genuinely blocked is named better as blocked than as merely
        # yours"), and `sweep-gate-fixture.py` pins it (`"next, blocked + role"` ->
        # `⏸️`). The offer arm's own blocked-upstream check below still returns for
        # that row, which is what makes the old role test inside it unreachable.
        #
        # ⚠️ Guarded on a NON-LIVE owner, because the harm is the OWNERLESS row. A
        # role'd row with a live session IS being worked and `progressing` is the
        # honest cell for it. `enrich_liveness` sets `liveness` before `_classify`
        # runs, and a row recording no id reads `LIVENESS_NONE`, so a sessionless
        # row passes the guard.
        #
        # ⚠️ The `yours` key is what `action_lines()` keys the `👤 YOURS: <task> —
        # not a spawn target` line off, and it is unchanged: what this branch
        # suppresses is the spawn OFFER and the nudge, never the disposition line.
        return "\U0001f464 YOURS", "yours"
    if task.get("stuck") and not _is_waiting(task):
        # The runbook's `stuck`: file unchanged AND no new Progress entry AND either
        # (a) busy > ~30 min or (b) idle > ~30 min in `execution` with an open box.
        # ⚠️ That proxy cannot tell STALLED from WAITING — both
        # leave the mtime untouched — so a row held from OUTSIDE the worker is
        # exempted here (see `_is_waiting`) and falls through to `🔄 progressing`
        # below. The row is never dropped: only the `⚠️` cell and the nudge keyed
        # off it are suppressed.
        return "⚠️ problem", "problem"
    # An APPROVED row is offered whatever `status` it carries. `vault-cli task
    # approve` is a PHASE move (`todo -> planning`) and, once the paired vault-cli
    # change lands, writes no `status` key at all — until then it forces `status:
    # next`, which is the other half of why that arm stays. Either way an approved
    # row keeps the status it had: `next` for a row filed queued, `in_progress`
    # for one already promoted. `phase: planning` is what marks the
    # approved-and-not-yet-started state, so BOTH statuses reach the offer here.
    #
    # ⚠️ The `status == "next"` arm is kept DELIBERATELY and is not redundant:
    # 135 rows in this vault carry `next` with no `phase` key at all, so a
    # `phase == "planning"`-only predicate would silently drop every one of them
    # out of the offer. Widening is the only safe direction here.
    #
    # ⚠️ `phase == "planning"` is deliberately the narrow arm. An ownerless
    # `in_progress` row at `phase: execution` is one whose worker DIED mid-work —
    # an orphan for the caller's resume path, which the runbook states as
    # "`in_progress` with a DEAD owner" — and re-offering it as fresh work would
    # bypass that gate's crash-loop cap. Only `planning` marks approved-and-
    # not-yet-started.
    if status == "next" or (status == "in_progress" and task["phase"] == "planning"):
        # ready-to-start requires that no session owns it; a live owner means the
        # session is on it, not that it is waiting to be started.
        if task.get("liveness") == LIVENESS_LIVE:
            return "\U0001f504 progressing", "progressing"
        if task.get("blocked_upstream"):
            # Runbook § Step 4's blocked-upstream: an approved task with at least
            # one unmet `blocked_by` and no live owner. Placed BEFORE the role
            # check so both renderers agree — `manager-sweep-reader.md` carries no
            # role carve-out either, and a role'd task that is genuinely blocked is
            # named better as blocked than as merely yours.
            return "⏸️ blocked/upstream", "upstream"
        # ⚠️ The role test that used to sit HERE moved ABOVE the `stuck` branch on
        # 2026-10-15 (see its comment there). This arm is reached only by
        # `status: next`, or `in_progress` at `phase: planning` — which is exactly
        # why a `role: human` row at `phase: execution` never saw the test. A copy
        # left here would be dead code: every row arriving at this point either is
        # not role'd, or carries an unmet `blocked_by` and already returned above.
        return "\U0001f680 ready-to-start", "ready"
    if status == "in_progress":
        return "\U0001f504 progressing", "progressing"
    return "—", "other"


def session_cell(task: dict) -> str:
    """`[<sid8>]` alive / `(<sid8>)` not alive / `—` no id recorded.

    Three states, every one inside the 10-cell budget the 2026-09-20 narrowing set:

      `[<sid8>]`  — an id is recorded AND the session is alive. 10 terminal cells.
      `(<sid8>)`  — an id is recorded AND the session is not alive. 10 terminal
                    cells, byte-different from the form above, so the two cannot
                    be confused for one another at a glance.
      `—`         — NO id is recorded in either place (`claude_session_id`, or any
                    `session_id` under `metrics_sessions`). **Nothing else renders
                    `—`.**

    The id displayed is the current owner when one is recorded, else the newest
    `metrics_sessions` id; the delimiter reports the ROW's verdict over the whole set,
    so a live id is never masked by a dead one.

    ⚠️ `—` is a PROVENANCE claim, never a liveness one. Until 2026-09-30 this
    function returned `—` whenever its own liveness read was not-live, which
    re-encoded liveness into a provenance cell and contradicted the rule the
    runbook (§ Sweep output, Session bullet) and `agents/manager-sweep-reader.md`
    both carry. The delimiter carries the state now, so no word is reintroduced
    and Session stays 10 content cells.

    `parked` is ALIVE — the session holds a socket and is waiting on a gate — so
    it takes the live-shaped cell; only `LIVENESS_NONE` means not alive.

    The verdict is the CALLER's; this function only formats the one it is handed.
    A verdict that is missing or unreadable keeps the live-shaped cell rather than
    claiming a state nothing established, and the row is never dropped for this.
    """
    # `—` is defined over the WHOLE id set, never the current owner alone: a row whose
    # `claude_session_id` is empty but whose `metrics_sessions` holds ids DOES record an
    # id, and rendering `—` there would claim nothing was ever recorded — the same
    # wrong-provenance defect as `(null)`, in the opposite direction.
    ids = task.get("session_ids")
    if ids is None:
        ids = [task["session"]] if task.get("session") else []
    if not ids:
        return "—"
    sid = task.get("session") or ids[-1]
    if task.get("liveness", LIVENESS_NONE) == LIVENESS_NONE:
        return f"({sid[:8]})"
    return f"[{sid[:8]}]"


def marker(topic: str, tracked: list[dict], changed: bool) -> str:
    counts: dict[str, int] = {}
    for t in tracked:
        key = bucket(t)[1]
        counts[key] = counts.get(key, 0) + 1
    parts = [f"{counts[k]} {GLYPHS[k]}" for k in BUCKET_ORDER if counts.get(k)]
    verdict = "change" if changed else "no change"
    return (
        f"{datetime.now().strftime('%H:%M')} ✓ {topic} — "
        f"{len(tracked)} tasks · {' · '.join(parts)} · {verdict}"
    )


def status_label(status: str) -> str:
    """The bucket icon for a bare `status` — for the topic and goal rows.

    `bucket()` reads `phase`, `liveness` and `stuck` unguarded, so it cannot be
    handed a two-key dict. Topic and goal rows carry no session and no liveness,
    so this builds the shape `bucket()` requires with those fields neutral, and
    never re-implements the status→icon mapping.
    """
    return bucket(
        {
            "status": status,
            "phase": "",
            "session": "",
            "liveness": LIVENESS_NONE,
            "stuck": False,
        }
    )[0]


def render_table(
    topic: str, tracked: list[dict], members: list[str], vault: str, branch: str
) -> str:
    """The grouped frame — one row per level, carried by indent alone.

    ⚠️ **The indent encodes the level, and the ROOT is flush left on BOTH
    branches** — one rule, not two. On the topic branch the root is the topic, a
    goal sits one level down and a task two; on the goal branch there is no topic
    above the goal, so the goal IS the root and its tasks sit one level down.
    Each level down adds exactly three spaces, which is why the goal branch has
    **no six-space level**. Canonical rule, and the measurement behind it:
    `65 Runbooks/Manager Session.md` § Goal-branch indent.

    ⚠️ The goal branch therefore emits **no synthetic topic row**. Until
    2026-10-12 this function appended one unconditionally, built from
    `23 Topics/<subject>.md`; on a goal branch that page does not exist, so the
    frame drew the goal name TWICE — once flush left reading `SC — · Gate —`
    from the absent page, once at +3 carrying its real count. Reproduced against
    the goal `Fleet Communication`, and the indent was inverted against the
    runbook's rule in the same frame.

    A task naming several goals is listed ONCE, under its first member goal —
    the tracked-set rule admits it under every goal it names, which is right for
    membership and wrong for grouping.

    ⚠️ This is the grouping half of the frame only. The two-section split
    (`Essential` / `Optional`) is deliberately NOT rendered here: the
    declared-optional set is read semantically and the manager owns that read,
    so the gate renders one box and says nothing about optionality.
    """
    rows: list[list[str]] = []

    # The two depths ARE the branch difference: `goal_depth` is "" on the goal
    # branch because the goal is the root there, and a task sits one level below
    # the goal on both branches. Keep this the only place the branch is read —
    # a second `branch ==` test elsewhere in the frame is how the two branches
    # drift apart.
    goal_depth, task_depth = ("   ", "      ") if branch == "topic" else ("", "   ")

    if branch == "topic":
        topic_path = os.path.join(vault, "23 Topics", f"{topic}.md")
        try:
            with open(topic_path, encoding="utf-8") as fh:
                topic_text = fh.read()
        except OSError:
            topic_text = ""
        sc = checkbox_count(topic_text, "Success Criteria")
        gate = checkbox_count(topic_text, "Completion Gate")
        rows.append([topic, "—", status_label("in_progress"), "—", f"SC {sc} · Gate {gate}"])

    groups: dict[str, list[dict]] = {g: [] for g in members}
    ungrouped: list[dict] = []
    for t in tracked:
        placed = False
        for g in t["goals"]:
            if g in groups:
                groups[g].append(t)
                placed = True
                break
        if not placed:
            ungrouped.append(t)

    def task_row(t: dict, depth: str) -> list[str]:
        label, _ = bucket(t)
        return [depth + t["name"], session_cell(t), label, t["phase"] or "—", t.get("met", "—")]

    for g in members:
        owned = groups[g]
        if not owned:
            continue
        goal_path = os.path.join(vault, "24 Goals", f"{g}.md")
        try:
            with open(goal_path, encoding="utf-8") as fh:
                goal_text = fh.read()
        except OSError:
            goal_text = ""
        goal_status = fm_scalar(split_frontmatter(goal_text) or "", "status") or "in_progress"
        rows.append(
            [goal_depth + g, "—", status_label(goal_status), "—",
             checkbox_count(goal_text, "Success Criteria")]
        )
        for t in owned:
            rows.append(task_row(t, task_depth))

    if ungrouped:
        rows.append([goal_depth + "(declared directly — no member goal)", "—", "—", "—", "—"])
        for t in ungrouped:
            rows.append(task_row(t, task_depth))

    payload = json.dumps({"header": HEADER, "rows": rows, "widths": WIDTHS})
    try:
        out = subprocess.run(
            [sys.executable, BOX_TABLE],
            input=payload,
            capture_output=True,
            text=True,
            check=True,
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        raise RuntimeError(f"box-table render failed: {exc}") from exc
    return out.stdout.rstrip("\n")


def closer_of(session: str) -> str:
    """The session's newest-closer read, in `close-line.py`'s vocabulary.

    Measured 2026-10-12: this gate emitted a stamped close line for `da1a1266` at
    14:13:08Z, ten minutes after that session's closer had run at 14:03:18Z — a
    reader instructed to take a close action that had already happened, which is
    the same defect class as the unstamped line this wiring exists to remove.

    ⚠️ A failed read maps to `unreadable`, NEVER to `closed`. `reap-closer.py`'s own
    contract is the rule — *"Only CLOSED may withhold a reap. OPEN and UNREADABLE
    both mean reap as before"* — so a broken read must not suppress a notice. That
    is fail-open in the one direction that is safe here: the line still renders, and
    it still carries the re-check a reader settles it with.
    """
    try:
        out = subprocess.run(
            [sys.executable, REAP_CLOSER, session],
            capture_output=True,
            text=True,
        )
    except OSError:
        return "unreadable"
    return {0: "closed", 1: "open", 2: "unreadable"}.get(out.returncode, "unreadable")


def close_line(task: str, session: str, liveness: str, closer: str) -> str:
    """The stamped close line, or `""` when the producer declines to emit one.

    ⚠️ Deliberately NOT `check=True`. The sibling `BOX_TABLE` call (`:2400-2404`)
    uses it, and copying that pattern here would abort the whole sweep frame on the
    candidate shape — `close-line.py` exits 1 for `absent`/`unknown` **and** for
    `--closer closed`, both of which its own docstring calls normal outcomes rather
    than failures.

    ⚠️ Exit 1 returns `""` rather than the candidate it printed. This gate renders
    `✅ done` and never `🔧 close-me` — the candidate is a post-verdict overlay the
    caller applies, not this renderer's output (see `action_lines`' docstring, point
    (2)). Adopting `--closer` must not quietly add a line type this gate has never
    emitted.

    ⚠️ A failure here RAISES rather than falling back to a hand-assembled line. The
    fallback would be the defect this wiring removes: a close instruction emitted
    without the stamp and the re-check that let a reader settle it. That is this
    file's stated preference (`:224`) — a silent fail-open is the worst failure
    mode — and the raise matches the BOX_TABLE precedent.
    """
    try:
        out = subprocess.run(
            [
                sys.executable,
                CLOSE_LINE,
                "--task",
                task,
                "--session",
                session,
                "--liveness",
                liveness,
                "--closer",
                closer,
            ],
            capture_output=True,
            text=True,
        )
    except OSError as exc:
        raise RuntimeError(f"close-line render failed: {exc}") from exc
    if out.returncode == 0:
        return out.stdout.rstrip("\n")
    if out.returncode == 1:
        return ""
    raise RuntimeError(
        f"close-line render failed: exit {out.returncode}: {out.stderr.strip()}"
    )


def action_lines(tracked: list[dict]) -> list[str]:
    """The runbook's non-empty action lines, printed under the box.

    Only the ones a model-free read can honestly produce are emitted. Two are
    deliberately NOT emitted, because the gate cannot produce them without a
    verdict it has no right to make:

      ⚠️ ORPHANED — the runbook requires a *confirmed* orphan (every id in the set
      probed by `pgrep -f` AND `ps -eo pid,args`, plus transcript recency), and
      prints no row on a candidate alone. A false ORPHANED row is what gets a live
      worker double-resumed, so the gate stays out of it.

      ⚠️ ACTION NEEDED — that is a human decision surfaced by the manager's own
      judgement (runbook § Gate triage); a shell process has none to contribute.

    ⚠️ The line itself is RENDERED, never assembled here — `close_line()` (`:2038`)
    shells out to the plugin's one producer, so this site cannot drift from the two
    manager print steps. A hand-assembled line is the defect that producer removes.

    DONE — CLOSE is emitted, and deliberately so: this process holds the liveness
    instrument the sweep reader does not (`read_registry` / `liveness_of`, the
    registry keyed by pid), so the verdict is *made* here rather than guessed from
    roster presence. Two gaps remain, named rather than papered over:

      (1) A HEADLESS worker has no registry record — `read_registry`'s own note is
      "a missing record proves nothing: a headless worker has none". So a finished
      headless worker whose session is still live emitted NO close line: the silent
      direction of the close-me defect, a live session never closed.

      ⚠️ CLOSED 2026-09-25 by the heartbeat store. `liveness_of` now reads it, so a
      live headless worker reads `live` — or `parked` when it has a gate open — where
      it previously read `none`. That fixes BOTH directions: the close line below now
      fires for a done task whose headless worker is still live, and a `next` task
      whose headless worker is mid-turn stops bucketing as 🚀 ready. The second is the
      dangerous one — a false READY invites a duplicate spawn onto live work.

      ⚠️ The READER is deployed; the WRITER is not, and the difference is not
      cosmetic. This script reads `~/.local/state/claude-supervisor/live`, stamped by
      the supervisor server that owns each worker. Until that server change ships AND
      the running servers are restarted, the store is empty and this gate behaves
      exactly as it did before — silently, and in the same direction. Confirm the
      store is live with `scripts/live-workers.py --list` (claude-supervisor) against
      a worker that is genuinely mid-turn before trusting a `—` here.

      (2) This gate renders `✅ done` and never `🔧 close-me`, because `bucket()`
      has no close-me branch. That is NOT a divergence from the legend: `🔧 close-me`
      is a post-verdict overlay the caller applies, the same shape `aborted` has —
      see runbook § Sweep output's icon legend.
    """
    ready, yours, close, hold, backlog, upstream, unregistered = (
        [],
        [],
        [],
        [],
        [],
        [],
        [],
    )
    for t in tracked:
        # A deferred task is deliberately parked until its `defer_date`, so it yields
        # NO action line — the same silence `⌛ waiting-on-human` keeps. Without this,
        # a `next` task with a future `defer_date` renders `⏸️ deferred` in its Status
        # cell while the line beneath says `🚀 READY TO START: … start a session`: a
        # `next` task keeps the `ready` key, so the overlay cannot suppress the line
        # by itself. That contradiction is worse than the one this task removes —
        # it invites a spawn onto work deliberately deferred. 34 `next` tasks in this
        # vault carry a `defer_date`, so the case is populated, not hypothetical.
        # ⚠️ A held session emits ZERO action lines — no READY TO START, no
        # DONE — CLOSE, no HOLD, no BLOCKED UPSTREAM, no YOURS, no BACKLOG. The
        # row still renders (bucket() labels it `⏸️ HELD`); only the message stops.
        # This is checked BEFORE `deferred` because a hold is the stronger
        # statement: a `defer_date` says "not yet", a hold says "not by you".
        # ⚠️ `session`, not `claude_session_id` — the same trap as in `bucket()` above.
        # Reading the raw frontmatter field name here is inert in exactly the same way,
        # and inert suppression is indistinguishable from correct suppression.
        if is_held(t.get("session") or ""):
            continue
        if is_deferred(t):
            continue
        _, key = bucket(t)
        if key == "upstream":
            # The runbook's line: `⏸️ BLOCKED UPSTREAM: <task> — blocked_by <blocker>
            # unshipped — no action until it lands`. Naming the blocker is what makes
            # the row actionable rather than merely parked — a reader learns WHICH
            # prerequisite to watch, not just that something is missing.
            blocker = (t.get("blocked_by_unmet") or ["?"])[0]
            upstream.append(
                f"⏸️ BLOCKED UPSTREAM: {t['name']} — blocked_by {blocker} unshipped "
                f"— no action until it lands"
            )
        elif key == "unregistered":
            # The remedy is a session-to-session check-in, NOT TTS and NOT a
            # publish: nothing is on the phone because the feed never carried the
            # gate. The line names the act and quotes the shape that recovered the
            # measured run. It is deliberately not an `approve:` line — this
            # session cannot execute a check-in on the operator's behalf, and the
            # manager session is the one that sends it.
            unregistered.append(
                f"⌛ PARKED-ON-UNREGISTERED-GATE: {t['name']} — no feed row and the "
                f"file has not moved for ≥2 intervals — SendMessage a check-in: "
                f'"booted? first step? or parked on a gate?"'
            )
        elif key == "ready":
            ready.append(
                f'🚀 READY TO START: {t["name"]} — start a session: /supervisor:open "{t["name"]}"'
            )
        elif key == "yours":
            # Excluded from the OFFER, not from the SWEEP — the row stays in the box.
            # The word is `manager-loop.md`'s, so a reader of either path reads the
            # same verdict.
            yours.append(f"👤 YOURS: {t['name']} — not a spawn target")
        elif key == "done" and t.get("liveness") in (
            LIVENESS_LIVE,
            LIVENESS_PARKED,
        ):
            # ⚠️ Rendered by its one producer, never assembled here. The line this
            # branch used to build carried no verification moment and no re-check,
            # so a reader following it unconfirmed could act on a void instruction
            # — the defect `close-line.py` exists to remove. The full id is passed
            # (the producer shortens it for display and embeds the full one in the
            # re-check); `t["liveness"]` is already the caller's own verdict, which
            # is exactly what the producer's `--liveness` contract requires.
            # ⚠️ `--closer` is the SECOND half of that verdict, and it is a separate
            # read: a session whose close already ran is still live while its process
            # holds the socket, so a live verdict alone does not mean there is a close
            # left to take. This branch emitted for exactly that row until 2026-10-12.
            line = close_line(
                t["name"], t["session"], t["liveness"], closer_of(t["session"])
            )
            if line:
                close.append(line)
        elif key == "hold":
            hold.append(f"⏸️ HOLD: {t['name']} — parked")
        elif key == "backlog":
            backlog.append(f"◦ BACKLOG: {t['name']} — someday, no action")
        elif key == "approval":
            # ⚠️ NO action line, and this branch exists to make that SILENCE explicit
            # rather than incidental. An unapproved row is the operator's inbox, not
            # a defect and not an offer: nothing opens it, nothing resumes it,
            # nothing nudges it. It is not TTS'd either — unlike `waiting-on-human`,
            # where a ~15-min wait means a worker is genuinely blocked, an unapproved
            # row may sit for days by design. `/supervisor:open --flagged` is the one
            # carve-out, and it is the operator's command, never this loop's.
            #
            # Written as an explicit branch, not left to fall through: the fall-
            # through is byte-identical today, and a later reader adding an offer
            # line here would otherwise be editing a branch that did not exist.
            continue
        elif key == "external":
            # ⚠️ NO action line, and like `approval` this branch exists to make that
            # SILENCE explicit rather than incidental. A machine is producing the
            # change — that is what the worker's own `⏰ Ends:` declaration asserts —
            # so there is nothing for the manager to do and nothing to nudge. A
            # check-in here would be the same false positive the `stuck` exemption
            # exists to prevent, one turn of a worker whose only correct next action
            # is to keep waiting.
            #
            # It is deliberately NOT the `unregistered` line either: that one
            # prescribes a `SendMessage` because a gate the feed never carried has no
            # other route to the operator. Here the worker is not blocked on a
            # person at all, and the mechanism that resumes it is already armed.
            continue
        elif key == "declared":
            # ⚠️ NO action line, and like `approval` and `external` this branch exists
            # to make that SILENCE explicit rather than incidental. The row's owner has
            # written down what it is waiting on — its own `declared_wait:` frontmatter,
            # or an open entry on a manager-side open-items ledger — so there is nothing
            # for the manager to do and nothing to ask. A check-in here would be exactly
            # the false positive this cell exists to remove: measured three times on one
            # row in one evening, every one answered *"not parked, booted and working"*.
            #
            # It is deliberately NOT the `unregistered` line: that one prescribes a
            # `SendMessage` because a gate the feed never carried has no other route to
            # the operator. Here the wait is already recorded and the row is healthy.
            continue
    return ready + yours + close + upstream + unregistered + hold + backlog


def frame_of(
    topic: str, tracked: list[dict], changed: bool, members: list[str], vault: str, branch: str
) -> str:
    """The tick frame: marker, the box two spaces under it, then the action lines.

    The runbook's § Sweep output owns all three — the marker shape, the columns and
    widths, and the non-empty action lines below the box — so this function only
    assembles them.

    `branch` is passed through to `render_table`, which is the only consumer: the
    marker and the action lines are branch-independent.
    """
    lines = [marker(topic, tracked, changed)]
    lines.append("  " + render_table(topic, tracked, members, vault, branch).replace("\n", "\n  "))
    lines.extend("  " + line for line in action_lines(tracked))
    return "\n".join(lines)


# --------------------------------------------------------------------------- #
# main
# --------------------------------------------------------------------------- #


def resolve_subject(vault: str, subject: str) -> tuple[str, list[str]]:
    """-> (branch, members). Raises ValueError unless exactly one page resolves.

    The gate's subject list unions the declared-manager state (which records
    `branch: goal`) with the topic pages, so a GOAL subject reaches this gate by
    design. Resolving it only under `23 Topics/` made it fail open on every tick.

    This mirrors `manager-predispatch.py:resolve_subject` exactly, so the gate and
    the commands that defer to it gate the same tree. The goal branch keys on the
    folder alone; the topic branch additionally requires `page_type: topic`. The
    asymmetry is measured, not stylistic — see `docs/subject-resolution.md` § The
    page test. Both-match and no-match are refusals, never a silent preference.
    """
    goal_path = os.path.join(vault, "24 Goals", f"{subject}.md")
    topic_path = os.path.join(vault, "23 Topics", f"{subject}.md")
    is_goal = os.path.exists(goal_path)
    is_topic = os.path.exists(topic_path) and page_type_of(topic_path) == "topic"
    if is_goal and is_topic:
        raise ValueError(f"ambiguous subject: both {goal_path} and {topic_path} resolve")
    if is_goal:
        return "goal", [subject]
    if is_topic:
        return "topic", declared_members(topic_path)
    raise ValueError(f"no subject page for {subject!r} in 24 Goals/ or 23 Topics/")


def page_type_of(path: str) -> str:
    """The page's `page_type:` frontmatter value, or "" when absent/unreadable."""
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except OSError:
        return ""
    fm = split_frontmatter(text)
    return fm_scalar(fm, "page_type") if fm else ""


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description="manager sweep change gate")
    ap.add_argument("--topic", required=True)
    ap.add_argument("--vault", default=DEFAULT_VAULT)
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="verdict only")
    mode.add_argument("--render", action="store_true", help="frame to stdout only")
    mode.add_argument("--tick", action="store_true", help="frame -> tick file; stdout on change")
    # SC1's invariant, as its own mode. It prints the card payload as JSON and NOTHING
    # else, so the caller (`sweep-gate-notify-tick.sh`) can post it verbatim without
    # parsing the frame — and an empty stdout means "the invariant did not fire",
    # which is the same silence a no-change tick keeps.
    mode.add_argument(
        "--invariant",
        action="store_true",
        help="print the open-card card payload (JSON) when the invariant fires",
    )
    args = ap.parse_args(argv)

    try:
        branch, members = resolve_subject(args.vault, args.topic)
        tracked = resolve_tracked(args.vault, members)
    except (ValueError, OSError) as exc:
        # ⚠️ stderr ONLY, in EVERY mode — never stdout. stdout is a `Monitor`'s
        # event stream, so the `CHANGE` line that stood here made an unresolvable
        # subject wake the model on every tick, carrying no table: the exact wake
        # this gate exists to remove, and strictly worse than the per-tick
        # delegation it replaces. This supersedes the comment that argued
        # `--tick`'s fail-open was correct — "a change you cannot parse IS a
        # change worth waking on" holds for a TRANSIENT fault, and fails for a
        # subject that can never resolve. The distinction is transient vs
        # permanent; the module docstring's "Fail-open" section carries the case.
        print(f"fail-open: subject unreadable/unparseable: {exc}", file=sys.stderr)
        #
        # ⚠️ `--invariant`'s exit code is a SEPARATE contract and keeps its own
        # carve-out. An unreadable subject answers the invariant's own question
        # directly — it cannot be shown to be operator-blocked — so the honest
        # answer is "nothing", said with the no-fire code. Returning a non-zero
        # exit here makes every unreadable subject read to the CALLER as a broken
        # install: `covered-subject-invariant.md` treats `3` as "another layer
        # already holds it" and ANY other exit as "the vault-side script is
        # unavailable", reported once per round. Measured 2026-10-10: a RETIRED
        # topic (`23 Topics/Manager Layer.md`, `status: completed`, deliberately
        # carrying no `## Goals` section) and a phantom subject with no page at
        # all (`ADOPT-PROBE FIXTURE`, named by three stale manager records)
        # produced 1487 + 851 + 239 fail-open ledger rows — every one of them
        # reporting a working install as broken.
        if args.invariant:
            return EXIT_NOCHANGE
        return EXIT_FAILED

    registry = read_registry()
    feed = read_feed()
    stored, fail_reason = load_stored(args.topic)
    prev_busy = load_state(args.topic).get("busy_since") or {}

    now_ts = time.time()
    enrich_liveness(tracked, registry, feed)
    # ⚠️ Read from the manager's own arm record, never assumed. The gate takes no
    # interval argument, and the prose's "2 x interval" was true only at the 15-min
    # default — so at any longer tick the cell fired on rows the rule calls working.
    # `park_basis` names which basis was used, so an absent cadence record is
    # visible rather than silently reverting to the old constant.
    park_seconds, park_basis, _ = park_threshold_seconds(args.topic, branch)
    # ⚠️ The basis is ALWAYS announced, armed or not — a correct run that prints
    # nothing is uncitable, which is how this defect stayed invisible. stderr is the
    # right channel: stdout's `CHANGE` lines drive the Monitor and would wake the
    # manager on every tick.
    print(f"park: {park_basis}", file=sys.stderr)
    apply_unregistered_parks(tracked, feed, registry, now_ts, park_seconds)
    apply_external_waits(tracked, feed)
    # ⚠️ Order is free for this one: unlike `unregistered_park`, the declared-wait
    # flag is read by `_classify` and by `_is_waiting`, never inside
    # `parked_on_unregistered_gate` — so it may be set anywhere before the render.
    apply_declared_waits(tracked)
    busy = apply_stuck(tracked, registry, prev_busy, now_ts)
    # Before digest_of: the digest is built from what the sweep renders, so a bucket
    # resolved after it would move the table without moving the change signal.
    apply_blocked_upstream(tracked, blocker_statuses(args.vault))
    digest = digest_of(tracked)
    changed = fail_reason is not None or stored != digest

    # SC1: the model-free half. Placed here — after every `apply_*` that can move a
    # row's bucket and before the render — because `subject_blocked_on_operator`
    # reads `bucket()`, and a bucket resolved earlier would answer about a state the
    # tick never renders. ⚠️ `open_cards()` is a SECOND read of the feed, not a reuse
    # of `read_feed()` above: that one is collapsed to one record per session and
    # cannot count a subject's cards. Read here rather than at the top so the two
    # reads are as close together as the pipeline allows.
    if args.invariant:
        card = invariant_card(args.topic, tracked, open_cards())
        if card is not None:
            print(json.dumps(card))
        return EXIT_NOCHANGE

    if args.tick:
        # The card is produced HERE rather than by a second `--invariant` run in the
        # wrapper. Both modes resolve the same tree, so the wrapper's second pass pays
        # a full extra resolution per subject — measured 2026-11-13 at 9.3 s of a
        # 16.9 s per-subject total, more than half the loop's pass. Folded here that
        # half is saved; `--invariant` remains its own mode for any other caller.
        try:
            write_card(args.topic, invariant_card(args.topic, tracked, open_cards()))
        except OSError as exc:
            # Same rule the tick file keeps: a failed side-channel write is named on
            # stderr and never read as a verdict. The tick itself still completes.
            print(f"fail-open: could not write card file ({exc})", file=sys.stderr)

    if args.check:
        if changed:
            print(f"CHANGE {fail_reason or 'digest differs'}")
            return EXIT_CHANGE
        print("NOCHANGE")
        return EXIT_NOCHANGE

    frame = frame_of(args.topic, tracked, changed, members, args.vault, branch)

    if args.render:
        print(frame)
        return EXIT_CHANGE if changed else EXIT_NOCHANGE

    # --tick: the frame is WRITTEN every tick; stdout carries it only on a change.
    # stdout is the Monitor's event stream, so a silent no-change tick is the whole
    # point — and a failed write must never pass quietly, so it prints and wakes.
    try:
        write_tick(args.topic, frame)
    except OSError as exc:
        # ⚠️ Same shape as the subject handler: stderr only, and NOT a change
        # signal. A `CHANGE` line here had the same defect — stdout is the
        # Monitor's event stream, so a persistently unwritable tick file woke the
        # model on every tick while the table it claimed to report was never
        # written anywhere the operator could read it.
        print(f"fail-open: could not write tick file ({exc})", file=sys.stderr)
        return EXIT_FAILED

    if changed:
        print(frame)
        print(f"VERDICT change ({fail_reason or 'digest differs'})")
    try:
        save_stored(args.topic, digest, tracked, busy)
    except OSError as exc:
        # Failing to record the digest must never read as "no change" next tick —
        # but "not no-change" is not the same as "a change". `EXIT_FAILED` says the
        # tick could not complete, which is what happened; `EXIT_CHANGE` would have
        # sent the host down the CHANGE-WAKE classify path and could publish a
        # "needs you" for a tick that rendered nothing. Same class as the two
        # handlers above, widened here in the same change.
        print(f"fail-open: could not record state ({exc})", file=sys.stderr)
        return EXIT_FAILED
    return EXIT_CHANGE if changed else EXIT_NOCHANGE


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except KeyboardInterrupt:
        sys.exit(130)
