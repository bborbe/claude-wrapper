---
status: verifying
approved: "2026-11-17T16:28:42Z"
generating: "2026-11-17T17:09:53Z"
prompted: "2026-11-17T17:09:53Z"
verifying: "2026-11-17T23:50:01Z"
branch: dark-factory/parked-observable-reopen-gate
---

## Summary

- The flagged reopen tick resumes every ABSENT row every 15 minutes, including rows whose worker deliberately parked on an external wait.
- Measured 2026-11-17 from `~/Library/Logs/claude-wrapper/stderr.log`: 10–25 rows reopened per tick all night (`02:58 opened=10`, `03:17 opened=25`, `04:01 opened=18`, `07:29 opened=14`).
- Each reopen polls once, writes a "no change" closer and exits, so the repeatedly reopened rows' event files grew to 5.7–9.3 MB; three sessions died in the 9–13 MB range.
- The tick will read each row's last closer from the session's own event stream, classify what that session was waiting on, and reopen only when that observable is satisfied.
- A declaration this build cannot probe expires after one hour; every other behaviour of the tick is unchanged.

## Problem

A flagged row is reopened on one condition: its session reads ABSENT. That condition is true of a row whose worker finished and died, and equally true of a row whose worker parked on something outside its control — an unanswered attention card, a PR waiting on review or CI, a `defer_date`, a host that is down. Both look identical to the tick, so both are resumed on the next tick, 15 minutes later. The resumed worker finds nothing changed, writes another "no change" closer and exits, and the cycle repeats for as long as the wait lasts.

The cost is not the resumes themselves. It is the event stream each resume appends to, in the same per-session file, for as long as the wait lasts: the rows reopened repeatedly all night reached 5.7–9.3 MB, and three sessions died in the 9–13 MB range. A row parked overnight therefore accumulates megabytes of identical "nothing changed" turns, and the ticks that reopened them displaced rows that genuinely needed reopening under the same per-tick row budget.

The information needed to tell the two apart is already on disk. The wrapper's own per-session event stream ends with the assistant's last message, and that message carries the closer the operator's convention requires — including the `⏰ Ends:` line that names what the session is waiting on. Nothing reads it for this decision.

## Goal

A flagged row whose last session parked on an external observable is not reopened while that observable is unchanged, and is reopened within one tick of it being satisfied. A row whose session ended any other way is reopened exactly as it is today.

A parked row's closer declares what it waits on. The tick classifies that declaration into one of four classes, probes the observable, and compares the probe against the last observation it recorded. Only a satisfied observable opens the row.

The classification is bounded in both directions: a declaration naming a clock is resolved by that clock, and a declaration naming something this build cannot probe expires after one hour rather than pinning the row forever. A probe that cannot be read holds the row; it never opens it.

## Non-goals

- Changing `claude-supervisor`'s `scripts/closer-verdict.py`, `scripts/who-needs-me.py` or `scripts/declared-wait.py`. Those own the `parked-wait` vocabulary; this build refines it locally for one decision and does not replace it. They are read-only references.
- Changing `flagged-rows.py`, its eligibility predicate, or the fields it emits.
- Changing the 15-minute interval, the 50-row per-tick budget, `recentlySpawnedWindow`, `predicateTimeout`, or any spawn cap.
- Posting, answering or reading any card beyond the existing open-card read.
- Classifying a closer that carries no `⏰ Ends:` line. `⏰ Next:` is the general forward pointer, not a park.
- A model call anywhere in the path.
- Pruning baseline files. A stale baseline cannot match a later park, because it is keyed to the session id and the closer's own hash.
- Expiring `parked-on-card`, `parked-on-pr` or `parked-on-host`. Those three wait on something a human or a machine will change; only the unprobeable class expires.
- Do NOT add a config key or flag that disables the park gate, and do NOT add a per-class tunable timeout. Both are invariants; an escape hatch on the Goal is itself a regression. If a future consumer demands variation, that is a separate spec.

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0 in `claude-wrapper`. Evidence: the exit code, quoted.

- [ ] **AC2** — **The closer is read from a real event file, and `⏰ Next:` alone is not a park.** A focused test writes a real `events.jsonl` under a temp state directory (a real file, not a stubbed reader) containing, in order: an assistant text line, a tool-only assistant line, a `result` line, an assistant line whose text carries `⏰ Ends: card 3f9a1c7e answered` and `⏰ Next: nothing`, and a trailing partial line with no newline. It asserts the extracted closer is that last complete assistant text and that a closer carrying only `⏰ Next: nothing` classifies as not parked. Evidence: the printed `closer=%q` and `parked=<bool>` lines, and the focused test's exit 0. The trailing partial line must be discarded — emitting half a JSON object would be a decode error, not a closer.

- [ ] **AC3** — **Every measured declaration family maps to exactly one class.** One table-driven test prints `declaration=<text> class=<class>` per case; the expected classes are the table below. Evidence: one line per case, and exit 0.

  | Declaration (from a live session) | Class |
  |---|---|
  | `card 3f9a1c7e answered` | `parked-on-card` |
  | `the attention card 9b2f0d41 is answered` | `parked-on-card` |
  | `bborbe/claude-wrapper#412 merges` | `parked-on-pr` |
  | `the bot posts a review on bborbe/nuke#88 or CI goes red` | `parked-on-pr` |
  | `sun:22 answers` | `parked-on-host` |
  | `192.168.178.38:22 accepts a connection` | `parked-on-host` |
  | `192.168.178.38 answers` (dotted, no port → 22) | `parked-on-host` |
  | `2026-11-18T09:00:00+01:00` | `parked-until` |
  | `2026-11-18` | `parked-until` |
  | `task ABC-123 flips to a terminal status on disk` | `parked-until` |
  | `the tag v1.2.3 appears` | `parked-until` |
  | `#412 merges` (no owner/repo) | `parked-until` |
  | `sun answers` (bare hostname, no port) | `parked-until` |
  | `⏰ Ends:` with nothing after it | `parked-until` |
  | `⏰ Next: nothing` and no `⏰ Ends:` | not parked |

  ⚠️ The last four rows are the adversarial ones: a classifier that treats every `⏰ Ends:` as probeable would send a bare `#412` to `gh` and a bare `sun` to the network. A declaration naming an observable this build cannot address is `parked-until`, and that class is what expires.

- [ ] **AC4** — **A row parked on an unanswered card is held, and reopened within one tick of the card being answered.** A focused test drives the same tick object twice with a real `events.jsonl` and an open-card reader that reports the row's session id as owning a card on the first tick and not on the second. Evidence: the two printed lines `tick=1 opened=0 skipped_by_reason=parked-on-card:1` and `tick=2 opened=1`, and exit 0.

- [ ] **AC5** — **A row parked on a PR is held while head, review and state are unchanged, and opened within one tick of any of them changing.** A focused test drives three ticks: the first records the baseline (held), the second sees the same `headRefOid`/`reviewDecision`/`state` (held), the third sees a new `headRefOid` (opened). Evidence: the three printed tick lines, plus the recorded argv, which must be exactly `pr view <N> --repo <owner>/<repo> --json headRefOid,reviewDecision,state`. A `state` of `MERGED` or `CLOSED` counts as satisfied on its own. **The same test then repeats the three ticks for a second row whose closer names a different PR — `bborbe/other-repo#9` — and the recorded argv for that case must be exactly `pr view 9 --repo bborbe/other-repo --json headRefOid,reviewDecision,state`.** An implementation that hardcodes the repo or number from the first case fails the second, which is the point of running it: the argv must be derived from the declaration, never from the test.

- [ ] **AC6** — **A row parked on a host or a clock is resolved by that observable.** A focused test covers: unreachable host → held with `parked-on-host`; reachable host → opened; a clock one hour in the future → held with `parked-until`; a clock in the past → opened. Evidence: four printed `case=<name> opened=<bool> skipped=<reason>` lines, and exit 0.

- [ ] **AC7** — **An unprobeable declaration expires, and never pins a row.** A focused test holds a row whose closer says `task ABC-123 flips to a terminal status on disk`, advances the injected clock past `MAX_AGE_S`, and opens it. Evidence: the printed `age=59m opened=0` and `age=61m opened=1` lines. **Mutation check:** with `MAX_AGE_S` changed from `3600 * time.Second` to `0`, the same focused test must exit non-zero with `[FAILED]` and still print both lines. The verifier applies the mutation, runs, and restores with `git checkout -- pkg/parked_baseline.go`. A build failure does not satisfy the mutated run.

- [ ] **AC8** — **A probe that cannot be read holds the row; it never opens it.** Two cases in one focused test: an open-card reader returning an error, and a command runner returning an error for `gh`. Each asserts zero reopen calls and a skip whose reason is the class. Evidence: the printed `reopens=0 skipped=...` lines, both naming `parked-on-card` and `parked-on-pr` respectively.

- [ ] **AC9** — **A hold is a skip, not a refusal, and does not spend a rate token.** A focused test drives a tick over one parked row followed by one eligible row. Evidence: the printed `opened=1 refused=0 skipped=1 considered=2`, the reopened row's key, and the caps fake's recorded `CheckWorker` call count of exactly 1 — a hold that halted the tick or consumed a reservation would show `opened=0` or 2 calls.

- [ ] **AC10** — **A baseline file cannot escape its directory.** A focused test writes the baseline for a row whose key is `private-personal/task/../../escape`, and asserts the parked directory lists exactly one entry whose name matches `^[0-9a-f]{64}\.json$`, and that no file exists outside it. Evidence: the printed `entries=1 name=<hex>.json` and `outside=0` lines.

- [ ] **AC11** — **The running daemon wires the gate.** `grep -nE '^[[:space:]]*Parked:' main.go` returns at least one match. Evidence: the grep output, quoted. An unwired gate is otherwise indistinguishable from a working one until AC13's overnight observation, and an overnight wait is not a usable gate for a one-line omission.

- [ ] **AC12** — **The four classes are documented durably.** `docs/parked-waits.md` exists and names: each of the four class tokens, the `⏰ Ends:`-only rule, the `⏰ Next:` exclusion, `MAX_AGE_S = 3600`, and the statement that the vocabulary belongs to `claude-supervisor`'s `parked-wait` class and is refined here, not replaced. Evidence: seven per-token `grep -c` lines, each non-zero — see `## Verification`.

- [ ] **Post-Deploy (Rung-2):** **AC13** — **Over one night, parked rows stop being reopened tick after tick.** Read `~/Library/Logs/claude-wrapper/stderr.log` over a window of at least 8 hours covering at least 30 `flagged-tick opened=` lines. Evidence, all quoted: (a) `grep -c 'flagged-tick opened='` ≥ 30; (b) the number of those lines carrying `skipped_by_reason=` with one of the four `parked-` classes ≥ 10; (c) the recurrence check below prints no `RECUR` line and exits 0. A recurrence is a failure unless the verifier quotes the two detail lines and the observable change that justified reopening the row between them.

  - `deploy_check:` `go version -m ~/.local/bin/claude-wrapper | sed -n 's/.*vcs\.revision=\(.*\)/\1/p'`
  - `deploy_target:` `$(git rev-parse HEAD)`

- [ ] **AC14** — **The change is merged and released.** Evidence: `gh -R bborbe/claude-wrapper pr view <N> --json state,mergeCommit` reports `state: MERGED`; `git tag --contains <merge sha>` prints at least one tag; `git show <tag>:CHANGELOG.md | grep -c 'parked'` is non-zero. `autoRelease: false` in this repo means the release is triggered by the operator after merge (`/github-release-repo-trigger`), not by the merge itself.

**Scenario coverage:** none. The existing `pkg` suite plus the focused tests above reach every branch, and AC13 is the end-to-end observation on the real host. No new scenario file.

## Verification

**Container rung** (runs in the dark-factory container and in CI):

- `make precommit` — exits 0.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='reads the last closer' -ginkgo.v` — AC2.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='classifies a parked declaration' -ginkgo.v` — AC3.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='holds a row parked on an unanswered card' -ginkgo.v` — AC4.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='holds a row parked on a pull request' -ginkgo.v` — AC5.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='resolves a host and a clock park' -ginkgo.v` — AC6.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='expires an unprobeable park' -ginkgo.v` — AC7, both runs (clean and mutated).
- `go test -v -count=1 ./pkg -args -ginkgo.focus='fails closed on an unreadable probe' -ginkgo.v` — AC8.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='skips a parked row without spending a reservation' -ginkgo.v` — AC9.
- `go test -v -count=1 ./pkg -args -ginkgo.focus='keeps a baseline inside its directory' -ginkgo.v` — AC10.
- `grep -nE '^[[:space:]]*Parked:' main.go` — AC11, at least one match.
- `for t in parked-on-card parked-on-pr parked-on-host parked-until '⏰ Ends:' '⏰ Next:' MAX_AGE_S; do printf '%s ' "$t"; grep -cF "$t" docs/parked-waits.md; done` — AC12, seven lines each with a non-zero count.

**Operator rung** (needs the host, the launchd job and the real fleet):

- `make install` — builds, codesigns, installs to `~/.local/bin/claude-wrapper`.
- `launchctl kickstart -k gui/$UID/de.bborbe.claude-wrapper` — restarts the daemon.
- AC13's overnight observation. The recurrence check:

  ```bash
  awk '
    /rows=/ {
      n=split($0, f, "rows="); cur=f[2]; sub(/ .*/, "", cur);
      m=split(cur, c, ",");
      for (i=1;i<=m;i++) if (c[i] != "" && (c[i] in prev)) { print "RECUR " c[i]; bad=1 }
      delete prev; for (i=1;i<=m;i++) if (c[i] != "") prev[c[i]]=1
    }
    END { exit bad }
  ' ~/Library/Logs/claude-wrapper/stderr.log
  ```
- Merge, then the release cut, then AC14's three checks.

## Desired Behavior

1. For each ABSENT row with a session id, the tick reads the last complete assistant message from that session's own event stream, over a bounded tail of the file. A closer with no `⏰ Ends:` line leaves the row on the existing clock resume.
2. A closer carrying `⏰ Ends:` is classified into exactly one class, in this precedence: `parked-on-card`, `parked-on-pr`, `parked-until`, `parked-on-host`. At most one probe runs per row per tick. ⚠️ The clock outranks the host deliberately: a declaration naming both a clock and a host token must bound at the clock, because a `parked-on-host` row is never released by `MAX_AGE_S`. `docs/parked-waits.md` is the owner of the precedence.
3. Each class has one observable: whether the row's session still owns an open card; the PR's head, review decision and state; the host's TCP reachability; the named clock, or for an unprobeable declaration the age since first sighting.
4. The tick records the first sighting's fingerprint for the row and holds it; a later tick opens the row when the observable is satisfied. A missing baseline costs exactly one tick.
5. A baseline is tied to the session id and the closer's own hash, so a resumed session that parks again on a new declaration starts a new baseline rather than inheriting the old one.
6. A probe that cannot be read holds the row, with the class as the skip reason and the cause as a second segment of the same entry. A hold is a skip: it does not spend a reservation, does not end the tick's opening pass, and does not count toward the row budget.
7. Never-opened rows, and rows whose last session wrote no closer, keep the existing clock resume. Every existing guard keeps its position and its reason string.

## Constraints

- **The closer's source is the session's own event stream**, `<sessions-dir>/<session-id>/events.jsonl`, read as a bounded tail of the last 256 KB. It is the wrapper's own file, the wrapper's session id equals the Claude session id (a fresh spawn passes `--session-id <id>`; a resume uses the same id), so a row's `session_id` addresses the directory directly and a resume appends to the same file.
- **The last complete line whose `type` is `assistant`** is the closer candidate; its `message.content` blocks of type `text` are joined. A tool-only assistant line, a `result` line, or a trailing partial line yields no closer. A missing or unreadable file, or a session id that is not UUID-shaped, yields no closer — never a hold.
- **The marker literal is the existing `machineWaitEnds` constant** in `pkg/pane_guard.go`. Do not add a second copy of `⏰ Ends:`, and do not extend the pane guard's verdicts.
- **`MAX_AGE_S = 3600` seconds is a package-level named constant declared in `pkg/parked_baseline.go`** (that path is frozen because AC7's mutation restores it), not a config key. It is the same rule `claude-supervisor/scripts/declared-wait.py:42` states for a declaration naming no future clock: one extra hour, never an indefinite pin.
- **The PR probe's argv is frozen**: `pr view <N> --repo <owner>/<repo> --json headRefOid,reviewDecision,state`, run through the existing `CommandRunner` interface. The three fields are the observable; nothing else is read.
- **The card probe uses the existing `OpenCardReader.OpenSessionIDs`**, keyed by producer id, which for a session's own card is its session id.
- **Probe bounds are constants**: 10 seconds per probe, 2 seconds per TCP dial, default port 22 when the declaration names no port.
- **One baseline file per row**, named by the sha256 hex of the row key, under the daemon's state directory in a `parked/` sibling of `sessions/`. The file carries the class, the fingerprint, the first-seen time, the session id and the closer hash.
- **The skip reasons are the four class tokens**, through the existing `Skipped` / `skipped_by_reason=` path. A probe failure appends `: <cause>` after the class, which the existing `reasonClass` cut leaves in the same class count.
- **The park gate is a `Parked` field on the tick, set in `startFlaggedTick`'s tick literal** (that field name is frozen because AC11 greps for it). A nil gate means the gate is inactive, which is what a test that predates it gets.
- **No new config key, flag or per-class timeout.** `MAX_AGE_S`, the probe timeout and the dial timeout are the only bounds, and all three are constants.
- **`docs/parked-waits.md` is this contract's durable home.** The four class names are `claude-supervisor`'s `parked-wait` vocabulary refined for this decision; the page states that, so a later reader does not treat it as a competing taxonomy.
- **Repo conventions**: `github.com/bborbe/errors` wrapping (no `fmt.Errorf`, no bare `return err`), `ctx` threaded through every probe and store call, `libtime.CurrentDateTimeGetter` for the clock (so AC6 and AC7 drive time rather than sleep), GoDoc on every exported item.
- **Tests run against real collaborators where the real thing is cheap**: a real temp `events.jsonl`, a real temp baseline directory, a real `net.Listener` for the host probe. Only the `gh` runner and the open-card reader are fakes, and neither is the thing under test.
- **The CHANGELOG entry goes under a new `## Unreleased` heading.** The release cut renames it after merge; do not edit a released section.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility | Concurrency |
|---|---|---|---|---|---|
| Session dir pruned, or its event file unreadable | No closer → existing clock resume | None needed; the row reopens as before | Row appears in `rows=` with no `parked-` skip | Reversible | — |
| Last assistant line is tool-only (mid-work death) | No closer → clock resume | — | Same | Reversible | — |
| Attention store unreadable | Row held, `parked-on-card: <cause>` | Next tick re-probes | `skipped_by_reason=` count | Reversible | — |
| `gh` non-zero, rate-limited or timed out | Row held, `parked-on-pr: <cause>` | Next tick re-probes | Same | Reversible | — |
| Host dial times out | `reachable:false` → row held (this is the declared state, not a failure) | Held until the host answers | `parked-on-host` count | Reversible | — |
| Declaration names something unprobeable | `parked-until`, expires after `MAX_AGE_S` | Opens within one hour + one tick | `parked-until` count | Reversible | — |
| Baseline file corrupt or unreadable | Treated as missing → record and hold one tick | Self-heals on the next tick | One extra tick of hold | Reversible | — |
| Two daemons tick | Both hold the same row on the same observable; last baseline writer wins | None needed — both hold, so no duplicate session | Duplicate `flagged-tick` lines | Reversible | ⚠️ Baselines are not locked; the single launchd job is the assumption, as it already is for the recently-spawned guard |
| Probes are slow across many rows | The sequential loop overruns; the next tick starts late | Rows wait one interval | Tick spacing > 15 min | Reversible | No overlap — the loop is sequential, as with `predicateTimeout` |
| Clock skew between host and a declared deadline | A clock park resolves early or late by the skew | None needed at minute granularity | — | Reversible | — |
| Host permanently down, or a card never answered | Row held indefinitely — the declaration names that observable | Operator clears the flag or the host returns | `parked-on-host` / `parked-on-card` count stays flat | Reversible | — |
| Row reopened, then dies before writing a new closer | The old closer is still the last one → held on the old observable | Held until the observable changes | — | Reversible | — |

## Security / Abuse Cases

The closer is **model output** and is treated as untrusted input: it drives a subprocess argv, a network dial, and a filesystem path.

- **The `gh` argv.** The PR reference is parsed by an anchored pattern (`owner/repo#N`, each segment starting with an alphanumeric character) into three separate argv elements, never a shell. An owner or repo beginning with `-` is rejected, so a closer cannot smuggle a flag into the command. `N` must be digits and greater than zero.
- **The dial.** A declaration can name any `host:port`, so a closer can make the daemon open a TCP connection. The dial writes no bytes, is bounded at 2 seconds, accepts only a dotted name or `host:port` with a port in 1–65535, and runs at most once per row per tick. A bare single word is not treated as a host, which is also what keeps prose out of the network path.
- **The file path.** The session id is joined into the state directory only when it is UUID-shaped; anything else means no closer. The baseline file name is the sha256 hex of the row key, so a `<vault>/<kind>/<name>` key containing `..` cannot escape the parked directory (AC10).
- **Resource bounds.** One 256 KB tail read and at most one probe per ABSENT row per tick; the tail is a window, never a whole-file read, so a 9 MB event file costs the same as a small one.
- **What is logged.** The class and, on a probe failure, the cause — never the closer text verbatim. The closer can carry operator-visible prose and a card identifier, neither of which belongs in a log line that outlives the turn.
- **What can hang.** A `gh` call or a dial that never answers is cut by its own bound; a probe that times out is a probe failure, which holds the row rather than opening it. A hung probe therefore costs freshness, never a duplicate session.

## Suggested Decomposition

`DB1`–`DB7` are `## Desired Behavior` items 1–7. AC13 is verification-only (the overnight observation) and is covered by every prompt together rather than by any one; its absence from the table is deliberate.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Closer reader + classifier: bounded tail, last assistant text, `⏰ Ends:` detection, four classes with precedence — tests written red first | DB1, DB2 | AC2, AC3 | — |
| 2 | The four probes: card, PR (frozen argv), host, clock — each returning a fingerprint, each failing closed | DB3, DB6 | AC4, AC5, AC6, AC8 | 1 |
| 3 | Baseline store + the satisfied-vs-unchanged decision, `MAX_AGE_S` expiry, hashed file names | DB4, DB5 | AC7, AC10 | 1 |
| 4 | `pkg/flagged_tick.go` + `main.go`: guard placement after the existing guards and before the caps, skip reasons, wiring | DB6, DB7 | AC1, AC9, AC11 | 1, 2, 3 |
| 5 | `docs/parked-waits.md`: the four-class contract and its relationship to `claude-supervisor`'s vocabulary | DB2 | AC12 | 1 |

Prompt 1 is the bulk and the only one whose tests are adversarial: AC3's last four rows and AC2's `⏰ Next:` case are what a lazy classifier fails. Prompt 4 is small and could ride with 3; it is separate so the guard's placement relative to the caps and the halt flag is reviewed on its own rather than buried in a large diff.

## Do-Nothing Option

Nothing changes. The tick keeps reopening every ABSENT row every 15 minutes, including rows whose workers are parked on a card, a PR, a host or a date. Each reopen appends another "nothing changed" turn to the same per-session event file, so a row parked overnight keeps growing toward the 9–13 MB range where three sessions already died — and a session that dies there loses the work of the turn it was in, not just the poll.

The rows that genuinely need reopening are also affected, because the reopened parked rows consume the same 50-row per-tick budget and the same start-rate pacing. The measurement is direct: 10–25 rows per tick, all night, on a fleet where the parked population is a minority of the flagged set.

The alternative — asking each worker to check its own wait before exiting — cannot work: the worker has already exited, which is the condition that makes the row eligible in the first place.
