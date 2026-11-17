# Parked waits

`claude-wrapper`'s flagged reopen tick reopens a flagged row whose session reads
`ABSENT`. A session that parked on something outside its control also reads
`ABSENT`, so the tick reads that session's last closer from its own event stream
and holds the row while the declared observable is unchanged.

This page is the contract for that second decision. It exists because the spec
that introduced the gate is not a durable home for it: specs are archived once
the work lands, and the four class tokens are part of a vocabulary shared with
another repo. Without a page saying so, a later reader would treat the tokens
here as a competing taxonomy and either duplicate or replace them.

The tick's other decision — which rows are eligible in the first place — has its
own contract, [`eligibility-predicate.md`](eligibility-predicate.md).

## What a park is

A park is a closer that names an end-observable. The tick reads the last
complete assistant record of the row's session, takes the last line carrying the
machine-wait marker, and classifies that declaration into exactly one class. A
closer with no such line leaves the row on the existing clock resume; it is not
a park and nothing here applies to it.

The tick records the observable's first reading as a baseline and holds the row
while that reading is unchanged. A later tick opens the row when the observable
is satisfied. A resumed session that parks again on different words starts a new
baseline, because the baseline is tied to the session id and to a hash of the
closer text.

## The four classes

Classification follows a fixed precedence — card, then pull request, then host,
then clock — so a declaration matching more than one pattern still yields
exactly one class and at most one probe runs per row per tick.

| Class | Observes | Satisfied when | Expires |
|---|---|---|---|
| `parked-on-card` | whether the row's session still owns an open attention card | the session no longer owns one | no |
| `parked-on-pr` | a pull request's head, review decision and state | the state is `MERGED` or `CLOSED`, or any of the three fields changed since the recorded fingerprint | no |
| `parked-on-host` | whether a host accepts a TCP connection | the host is reachable | no |
| `parked-until` | a declared clock, or the age of the park when no clock is named | the clock has passed, or the park is older than `MAX_AGE_S` | only the unprobeable form |

The pull-request probe reads exactly three fields — `headRefOid`,
`reviewDecision` and `state` — through a fixed `gh pr view` argv. The card probe
asks the existing open-card set whether the row's session id is still in it. The
host probe makes one bounded TCP dial and writes no bytes. The clock probe
compares the declared deadline against the injected clock.

## Only `⏰ Ends:` parks

Only a closer carrying `⏰ Ends:` is classified. That marker has exactly one home
in this repo: `machineWaitEnds` in `pkg/pane_guard.go`. This page names it and
does not define it a second time; a change to the marker belongs in the pane
guard, not here.

## `⏰ Next:` is not a park

`⏰ Next:` is the general forward pointer. A closer carrying only `⏰ Next:` is
not held: it describes what the worker would do next, not what it is waiting on.
A closer may carry both markers, in which case only the `⏰ Ends:` line is
classified.

## `MAX_AGE_S`

`MAX_AGE_S = 3600` seconds. It mirrors
`claude-supervisor/scripts/declared-wait.py`'s rule for a declaration naming no
future clock: one extra hour, never an indefinite pin. A park on something this
build cannot probe therefore opens within one hour plus one tick.

`MAX_AGE_S` is a package constant in `pkg/parked_baseline.go`, not a config key,
and there is no per-class variant of it. No other bound is configurable either:
the probe timeout and the TCP dial timeout are constants too.

## What is not probeable

A declaration must name an observable this build can address safely. A bare
`#412` names no owner and repo, and a bare `sun` names no port, so neither is
probeable here; both classify as `parked-until` and expire under `MAX_AGE_S`. A
classifier that treated every declaration as probeable would send a bare number
to `gh` and a bare word to the network.

The same rule rejects an owner beginning with `-`, a pull-request number of
zero, and a port or IPv4 octet out of range. Such a declaration falls through to
`parked-until` with no deadline rather than reaching a command or a dial. The
owner/repo/number token is split into separate argv elements, so no shell is
involved and no token can become a flag.

## A probe that cannot be read holds the row

When a probe fails — the attention store cannot be read, `gh` exits non-zero or
times out, the clock is unconfigured — the row is held. The skip reason is the
class, with the cause appended after it as `: <cause>`. The gate fails closed:
an unreadable observable is never taken as an answered one.

A hold is a skip, not a refusal. It does not spend a reservation, does not end
the tick's opening pass, and does not count against the per-tick row budget. The
next tick simply probes again.

## Where the vocabulary comes from

The four class names are `claude-supervisor`'s `parked-wait` vocabulary refined
for this one decision, not replaced. `scripts/closer-verdict.py`,
`scripts/who-needs-me.py` and `scripts/declared-wait.py` remain the owners of
that vocabulary, and they are read-only references from this repo. This page
describes what `claude-wrapper` does with the vocabulary and nothing more; it
does not restate their rules, and a change to them is a change in
`claude-supervisor`.
