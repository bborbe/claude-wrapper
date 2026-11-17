---
status: draft
---

## Summary

- `sweep-gate.py` is a 3 337-line vault-side Python script that renders the manager sweep frame and produces the covered-subject invariant card.
- It has no owner: it is not in this repo, not in the supervisor plugin, and its only committed caller lives in the plugin.
- It is spawned by `while true` shell loops — one committed (`sweep-gate-ledger-loop.sh`, its own `INTERVAL`), the rest ad-hoc per topic — and there is no launchd job.
- This change ports it into this daemon as Go, behind the same four modes, and retires the Python file and the loop.
- Placement decision (operator, 2026-11-17): `claude-wrapper`, on the parent goal's "fleet ticks" remit.

## Problem

Three call sites exist today, and none of them is a durable home:

1. **`--invariant`** — the only committed spawner. `claude-supervisor/commands/steps/covered-subject-invariant.md:14` runs `python3 '<vault>/.claude/scripts/sweep-gate.py' --topic '<subject>' --vault '<vault>' --invariant` once per covered subject, and consumes the JSON card payload plus the exit code. That file states the split explicitly: *"The script is VAULT-side, not plugin-shipped … this repo does not carry it."*
2. **`--tick`** — a `while true` loop with `sleep 900`. One committed instance exists: the vault's `.claude/scripts/sweep-gate-ledger-loop.sh` (`GATE="$VAULT/.claude/scripts/sweep-gate.py"` line 40, `python3 "$GATE" … --tick` line 54, `sleep "$INTERVAL"` line 58), and it states at line 5 that the gate is *"UNTOUCHED by this script"*. Per-topic loops also exist in live sessions' argv with no file behind them. `launchctl list | grep -i sweep` returns nothing — there is no launchd job.
3. **`sweep-gate-notify-tick.sh`** — named by the script's own comment as the `--invariant` caller. The file does not exist at either path.

So the code is unowned and its tick is unowned, and the cost is paid per tick: a fresh Python interpreter start per topic per 15 minutes, plus a 3 337-line parse.

The two candidate homes both exclude it by their own stated boundary. `attention-controller`'s `CLAUDE.md` reads *"What it is not. No ranking …, no arms or renderers"*, and this script **is** a renderer. This repo's own `CLAUDE.md` describes phase 1 as *"owns a `claude` child process and exposes it over REST: spawn, list, inspect, answer, stop"*, which does not name a change gate either.

The parent goal supplies the missing clause: *"Fleet liveness work that needs no judgement — closing idle workers, reopening flagged rows, keepalive ticks — runs as Go code inside the long-lived `claude-wrapper` daemon"*, and classifies the script as *"spawn/control and fleet ticks"*. The operator chose this repo on 2026-11-17.

## Goal

The daemon owns the gate. The four modes keep their current contracts, the frame is byte-identical for the same inputs, and the Python file and its loop are gone.

Modes, as they stand today (`sweep-gate.py:3181-3330`):

| Mode | stdout | Exit |
|---|---|---|
| `--check` | `CHANGE <reason>` or `NOCHANGE` | 10 / 0 |
| `--render` | the frame | 10 / 0 |
| `--tick` | the frame **only on change**, then `VERDICT change (<reason>)`; frame written to the tick file every tick | 10 / 0 |
| `--invariant` | the JSON card payload, or nothing | 0 always |

`--tick` also writes `park: <basis>` to **stderr** every tick, and every failure path writes to stderr only — stdout is a `Monitor`'s event stream, and a `CHANGE` line on an unreadable subject wakes the model on every tick.

## Non-goals

- The 21 open sweep-gate behaviour tasks. This change moves code; it does not change which subjects are ticked, how they rank, or what the cards say.
- The fixture suite (`sweep-gate-fixture.py`) **while the port is in flight** — it is the parity reference, not a modification target. ⚠️ It loads `sweep-gate.py` directly by path (`importlib`, `GATE = …/sweep-gate.py`), so it **cannot survive AC10's deletion**: its retirement is therefore part of AC10, and the captured goldens are what carry the evidence forward.
- `sweep-gate-{subjects,classify,adhoc}.py` (under `~/.claude/scripts/`). Verified independent: `grep` for their names inside `sweep-gate.py` returns nothing. Their neighbours in the vault scripts dir — `sweep-gate-boundary-probe.py`, `sweep-gate-boundary-probe-fixture.py`, `sweep-gate-fixture.py`, `sweep-empty-target-vault.py` — are likewise out of scope.
- The duplicate install-path skew (the script also appears in a plugin cache copy).
- `who-needs-me.py` — its own task, in flight.

## Acceptance Criteria

- [ ] **AC1** — `make precommit` exits 0. Evidence: the exit code, quoted.

- [ ] **AC2** — **The Go frame is byte-identical to the Python frame for the same inputs.** A checked-in fixture vault with its recorded store, registry and hold inputs is rendered by the port and compared to the goldens in `testdata/sweepgate/<case>.render.golden.txt`, with a paired `<case>.render.stderr.golden.txt` — half the contract is stderr (the `park:` line and every failure path), so a stdout-only golden checks the weaker half.

  **Provenance is checked, not asserted.** The goldens are produced by `testdata/sweepgate/generate.sh`, which invokes `python3 <vault>/.claude/scripts/sweep-gate.py` against the fixture; the script is committed, and each golden's first line records `# generated by sweep-gate.py@<sha>`. The test fails if that marker is absent, so a golden regenerated from the Go port itself cannot pass.

  **At least one case is NOT golden-backed.** A second fixture with different subject names, row shapes and bucket mix has its expected frame stated inline in the test, derived by hand from the classification rules. Golden replay — a port that returns the checked-in bytes by case name — passes every golden case and fails this one.

  Evidence: `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='renders the parity fixture' -ginkgo.v` exits 0 and prints `case=<name> stdout_bytes=<n> stderr_bytes=<n> identical=true marker=<sha>` per case, plus `case=<name> golden=false identical=true` for the hand-derived case.

- [ ] **AC3** — **Every refusal is carried as a test, and the list is mechanically derived.** `## Refusals` in `docs/sweep-gate.md` enumerates each guard, each mapped to a named test. The list is derived by a sweep recorded verbatim in that document — the command and its raw hit count — and the table's row count must equal that count minus the exclusions it justifies. Each guard cites its `sweep-gate.py` line number **at the pinned revision** recorded in the doc header, since AC10 deletes the file.

  Evidence: `printf 'rows '; grep -c '^| ' docs/sweep-gate.md` and `printf 'derived '; grep -oE 'raw hits: [0-9]+' docs/sweep-gate.md` print matching numbers, and `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='carries every refusal' -ginkgo.v` exits 0 printing one `refusal=<name> test=<name>` line per table row.

- [ ] **AC4** — **A tick that finds nothing still proves it ran.** `--tick` writes the tick file and the `park:` stderr line on every tick, including a no-change tick, and the no-change tick prints nothing on stdout. A test drives two ticks with unchanged input and asserts: two tick-file writes, two `park:` lines, zero stdout bytes. Evidence: the focused test's printed counts.

- [ ] **AC5** — **A hanging read is abandoned and the next tick still fires.** A test drives the tick loop with a source whose read blocks until the context is cancelled, cancels after 100 ms, and asserts `Run` returns an error satisfying `errors.Is(err, context.Canceled)` and that the loop's next iteration still runs. Evidence: the printed `err=` value and two tick log lines from one `Run` call.

- [ ] **AC6** — **The invariant's exit contract is one code per command.** `--invariant` returns 0 on every path, including the unreadable-subject path, and prints the payload only when the card fires. A test covers: card fires → payload + exit 0; subject unreadable → empty stdout + exit 0. Evidence: two printed `case=<name> stdout_bytes=<n> exit=<n>` lines.

  ⚠️ The caller's `3` arm belongs to its own claim command, not to this script — `sweep-gate.py:316-324` records that `3` is reserved by the consumer. The port must not invent a 3.

- [ ] **AC7** — **No hardcoded vault path.** `ls pkg/sweepgate/*.go | wc -l` is non-zero **and** `grep -c 'Obsidian/Personal' pkg/sweepgate/*.go` is 0 on every file, and the vault path is a constructor argument. The Python's `DEFAULT_VAULT` points at the pre-rename directory, which is a 1-entry stub today.

  ⚠️ The file-count clause is load-bearing: `grep -c` over a glob that matches nothing exits 2 and prints nothing, which reads as a clean zero to a reader who does not check the exit code.

- [ ] **AC8** — **The state files keep their real paths and formats.** Read from the source (`sweep-gate.py:196-215`, `1723-1760`), the layout is:

  | Purpose | Path | Notes |
  |---|---|---|
  | digest state | `$SWEEP_GATE_STATE_DIR/<slug>.json` | default `~/.claude/state/sweep-gate/` |
  | tick file | `$SWEEP_GATE_STATE_DIR/<slug>.tick.txt` | the operator-facing table |
  | card payload | `$SWEEP_GATE_STATE_DIR/<slug>.card.json` | written by `--tick` too, not only `--invariant` |
  | cadence arm | `$SWEEP_GATE_STATE_DIR/<branch>--<slug>.cadence` | branch prefix |
  | settled ask | `$SWEEP_GATE_SETTLED_DIR/<slug>.json` | **separate directory**, `~/.claude/state/sweep-gate-settled/` |

  `slug()` is `re.sub(r"[^a-z0-9]+", "-", topic.lower()).strip("-")` — subjects carry spaces, so the transform is part of the contract. The `SWEEP_GATE_{STATE_DIR,SETTLED_DIR,FEED_DIR,HOLDS,PREDISPATCH_DIR,WORKER_MANAGER_DIR}` overrides are preserved: the fixture suite monkeypatches the constants, and a second host relies on `STATE_DIR`.

  Evidence: `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='reads a state file written by the python' -ginkgo.v` exits 0 and prints `file=<path> python_digest=<x> go_digest=<x> equal=true`.

- [ ] **AC9** — **`main.go` actually starts the tick, and the tick does something.** `grep -nE '^[[:space:]]*go[[:space:]]+startSweepGate' main.go` returns at least one match, **and** a test constructs the wiring from a config pointing at a fixture vault and observes a tick file written within the tick interval. Evidence: the grep output quoted, plus the focused test's printed `tick_file=<path> mtime=<ts>`.

  ⚠️ The grep alone is satisfied by `go startSweepGate(…)` whose body returns immediately — a wired no-op reads exactly like a working gate until AC11's 40-minute observation.

- [ ] **AC10** — **The Python file and BOTH its spawners are gone.** Deletion scope, all three parts:

  1. `sweep-gate.py` deleted from the vault's `.claude/scripts/`.
  2. `sweep-gate-ledger-loop.sh` deleted from the same directory — it is a committed `while true` loop that invokes `--tick` every interval (`:40`, `:54`, `:58`). Leaving it would make every interval fail against a missing script.
  3. `sweep-gate-fixture.py` is retired in the same commit — it imports the deleted module by path and cannot run afterwards. The parity goldens captured by `generate.sh` before the deletion are what preserve the evidence.
  4. The per-topic loops in live sessions' argv are stopped, and the plugin's `commands/steps/covered-subject-invariant.md:14` is repointed from `python3 '<vault>/.claude/scripts/sweep-gate.py' … --invariant` to the daemon — **a `claude-supervisor` change, so it ships as its own PR in that repo**, not in this one.

  Evidence: `git -C <vault> log -1 --stat` naming every deletion; `pgrep -f 'sweep-gate\.py' | wc -l` returning 0; the plugin PR number recorded in `# Results` of the owning task.

  ⚠️ The grep is scoped to the two deleted **paths**, not a directory sweep: `sweep-gate-boundary-probe.py` and `__pycache__/sweep-gate*.pyc` legitimately still mention the name, and a directory-wide `grep` would never return clean.

- [ ] **Post-Deploy (Rung-2):** **AC11** — **The gate runs in the daemon and the Python process is gone.** After `make install` and `launchctl kickstart -k gui/$UID/de.bborbe.claude-wrapper`:

  - `deploy_check:` `go version -m ~/.local/bin/claude-wrapper | sed -n 's/.*vcs\.revision=\(.*\)/\1/p'`
  - `deploy_target:` `$(git rev-parse HEAD)`

  Within **40 minutes** (two 15-minute ticks plus a 10-minute margin): `pgrep -f 'sweep-gate\.py' | wc -l` returns **0**, **and** `~/.claude/state/sweep-gate/<slug>.tick.txt` (the AC8 path — `<slug>` is the lowercased, dash-collapsed subject, never the raw topic name) carries an mtime inside that window **and non-empty content whose header line matches the frame the daemon renders** — the mtime alone is satisfied by a timer writing an empty file.

  ⚠️ `pgrep` alone cannot carry this: a stopped loop also returns 0. The tick file is the positive observation, and its content is what makes it evidence of a *render* rather than of a timer.

## Verification

**Container rung** (runs in the dark-factory container and in CI):

- `make precommit` — exits 0 (AC1).
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='renders the parity fixture' -ginkgo.v` — AC2.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='carries every refusal' -ginkgo.v` — AC3.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='writes the tick on a no-change tick' -ginkgo.v` — AC4.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='cancels a hanging read' -ginkgo.v` — AC5.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='prints the payload only when the card fires' -ginkgo.v` — AC6.
- `grep -c 'Obsidian/Personal' pkg/sweepgate/*.go` — AC7, every line 0.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='reads a state file written by the python' -ginkgo.v` — AC8.
- `grep -nE '^[[:space:]]*go[[:space:]]+startSweepGate' main.go` — AC9, at least one match.

**Operator rung** (needs the host, the launchd job and the real vault):

- `make install` — builds, codesigns, installs to `~/.local/bin/claude-wrapper`.
- `launchctl kickstart -k gui/$UID/de.bborbe.claude-wrapper` — restarts the daemon.
- AC11's 40-minute observation.
- AC10's vault-side deletion lands as its own commit in the vault repo.

## Desired Behavior

1. The daemon renders the same frame the Python renders, for the same inputs.
2. The four modes keep their stdout, stderr and exit contracts exactly.
3. A no-change tick writes the tick file and prints nothing on stdout.
4. An unreadable subject fails open to stderr with exit 2 — never a `CHANGE` line.
5. The invariant prints a payload only when the card fires, and always exits 0.
6. The Python file and the per-topic shell loop are gone, and no new spawner replaces them.
7. No `time.Now()`: the clock is a `libtime` `CurrentDateTimeGetter`, injected.

## Constraints

- **`sweep-gate-fixture.py` is the parity reference**, read-only. Its five assertion groups — `CLOSE-ME`, `FIVE-WAY`, `PROGRESS`, `LIVENESS`, `HELD` — are the minimum the port's tests must cover.
- **The state file formats are frozen** for the switchover (AC8). Renaming a field costs a spurious change on the first tick and a lost digest.
- **The attention store and session registry are read, never written.** The port adds no producer.
- **`github.com/bborbe/errors` only**, `ctx` threaded, Ginkgo/Gomega tests against a real collaborator.
- **One interface per concern** — digest, classify, render, card — rather than a translation of the Python's 87 functions into 87 Go functions.
- **The boundary is recorded in `docs/sweep-gate.md`, which is tracked.** ⚠️ This repo's `CLAUDE.md` is **gitignored** (`.gitignore:9`, `git ls-files CLAUDE.md` empty), so it cannot enter a PR and the placement cannot rest on editing it. `docs/sweep-gate.md` carries the boundary sentence the gate is placed against — this repo owns "spawn/control **and periodic fleet ticks**" — and that file is what a reviewer can read. Landing the code in a repo whose only written boundary excludes it recreates the defect this change exists to end.

## Failure Modes

| Trigger | Expected | Recovery | Detection | Concurrency |
|---|---|---|---|---|
| Store unreachable | Tick fails open to stderr, exit 2, tick file still written | Next tick retries | `fail-open:` in stderr | — |
| Subject unreadable | stderr line, exit 2, no `CHANGE` | Fix the subject | stderr | — |
| State file corrupt | Treated as no stored digest → one change | None needed | one spurious `VERDICT change` | — |
| Two daemons tick | Duplicate cards | Single launchd job is the assumption | duplicate dedup keys | ⚠️ Not prevented |
| Port drifts from Python | Frame diff | AC2 catches it | parity test | — |
| Tick overlaps its interval | Next tick delayed, not stacked | Sequential loop | tick spacing > 15 min | Single goroutine |

## Security / Abuse Cases

- **`slug()` is the only thing between a subject name and a path.** A subject is vault-authored, model-written content. `slug()` must be applied before every path join and must reject a result that is empty or contains `..`, so `--topic '../../etc/x'` cannot escape `STATE_DIR`. A test covers a traversal-shaped subject and asserts the resolved path stays under the state directory.
- **Vault-authored strings are untrusted.** The plugin caller documents that a subject can carry `"`, a backtick or `$(`, which is why its own invocation is single-quoted. The port must never interpolate a subject into a shell command, and never pass it as a format string — `fmt.Sprintf(subject)` turns a `%s` in a task title into a corrupted render or a panic.
- **Parsed JSON is untrusted**: the attention feed, the holds file, the predispatch verdict cache and the worker-manager records are all read from disk. A malformed record must read as "no record", never as a fatal error — the Python's `load_settled` records that a half-written file reading as "no memory" is the safe direction.
- **The `SWEEP_GATE_*` env overrides are trusted local config**, set by the operator or the fixture. They are not a request surface.
- **No new listener.** The four modes are a CLI contract; the one caller invokes a command. If an HTTP route is added it is a deliberate scope increase needing its own justification — an unauthenticated local route that renders vault contents is a disclosure surface this change does not need.
- **No credentials are read, written or logged.** The gate reads vault markdown and local state files.

## Suggested Decomposition

`DB1`–`DB7` are the `## Desired Behavior` items in order. Row 7 is an **operator hand-off**, not a generated prompt: it commits in the vault repo and in `claude-supervisor`, neither of which the container can reach.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `pkg/sweepgate`: frontmatter, task parsing, digest, tracked-set resolution + the parity fixture, `generate.sh` and its goldens | DB1, DB7 | AC2, AC8 | — |
| 2 | `pkg/sweepgate`: bucket classification, hold overlay, parks and waits, liveness | DB1, DB3 | AC2, AC3 | 1 |
| 3 | `pkg/sweepgate`: table render, action lines, close lines | DB1, DB2, DB3 | AC2, AC4 | 1, 2 |
| 4 | `pkg/sweepgate`: invariant card, store feed read, card write, `slug()` hardening | DB5 | AC6 | 1, 2 |
| 5 | `main.go` wiring: one flag, one goroutine, the four modes as CLI entry points | DB2, DB4, DB6 | AC7, AC9 | 1–4 |
| 6 | `docs/sweep-gate.md`: the refusal table, the mode contracts, the state-file layout and the boundary sentence | DB1, DB2, DB6 | AC3, AC8 | 1–5 |
| 7 | **Operator hand-off:** vault-side deletion of `sweep-gate.py` and `sweep-gate-ledger-loop.sh`, and the `claude-supervisor` PR repointing the `--invariant` caller | DB6 | AC10 | 5, 6 |

## Do-Nothing Option

Nothing changes: a 3 337-line script keeps running from a path no repo owns, started by loops no file contains, at one interpreter start per topic per 15 minutes. The `--invariant` caller keeps depending on a file its own repo says it does not carry.

The cost of the change is one package and one goroutine. The cost of not making it is a gate whose only committed caller documents that the code it calls has no home — and a defect class (21 open tasks) that nobody can fix in a language nobody chose.
