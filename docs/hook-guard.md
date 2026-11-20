# The Bash hook guard

| | |
|---|---|
| Package | `pkg/hook` |
| Entry point | `cmd/hook` → `~/.claude/hooks/hook` |
| Registered | `~/.claude/settings.json` → `PreToolUse` on `*`, as `hook \|\| exit 2` |
| Fails | **closed** — a binary that cannot answer refuses every tool call |
| Tests | `pkg/hook/*_test.go` (Ginkgo), `make precommit` |
| Installed by | `make install-hook` (`scripts/install-hook.sh`) |

This page is the extension guide: what decides a command, how to teach the guard a
new shape, and how to prove the change without regressing anything.

## Why it lives here

Every Claude session on this machine runs this binary on every tool call. It is the
only place a command is *approved* — under `permissions.defaultMode: auto` the
`permissions.allow` list grants nothing on its own, so a command with a matching
rule still parks unless a guard here approves it. That is why a rule alone never
fixes a park, and why this package is where the behaviour lives.

## How a decision is made

`CompoundBashGuard.Evaluate` runs in this order, and the first hit wins:

1. **`CredentialRead(command)`** — a command naming a credential store or a
   secret-printing CLI is refused before any arm runs, whatever rule names the
   program. It matches the raw text **and** the text with quoted spans removed, so a
   quote split (`cat ~/.a""ws/credentials`) or an escape (`cat ~/.a\ws/credentials`)
   does not hide a path, and it refuses a glob inside a dot-directory *name*
   (`~/.a?s/credentials`) while allowing a glob in a later segment (`~/.claude/*.json`).
2. **`planCompound`** picks the arm:
   - **text arm** — `SplitCompound` splits the line into segments, each judged by an
     allow rule; a leading literal assignment prefix is substituted first.
   - **syntax-tree arm** — `CompoundTreeAllows` parses with `mvdan.cc/sh/v3/syntax`
     and models every node, capped at 24 calls. Used for anything the text split
     cannot account for: an unquoted redirect, a heredoc, a substitution, a chain
     over 8 segments, or a single command.
3. **`plan.allows`** applies, per segment, in this order: a shape guard
   (`shapeAllows`: `git`, `sed`, `gh`, `base64`), then an allow rule whose program
   matches, subject to the shape gates the tree arm applies (glob and tilde checks
   on any word carrying an unquoted part, the shell-state builtin refusal, the
   env-prefix redirect check).

The registry in `pkg/hook/registry.go` then applies **deny precedence**: if any
guard denies, the command is denied no matter what this one allows.

## Teach it a new shape

Pick the narrowest of these that covers the case.

### 1. A read-only program a rule already names

Nothing to do in Go — the rule approves it, as long as the call carries no shape
the tree arm refuses. This is the common case and it is why most parity work was
about the *tree arm*, not new guards.

### 2. A program whose arguments must be gated

Add a shape guard and wire it into `shapeAllows`:

```go
// MkdirAllows reports whether the call only creates directories under the roots.
func MkdirAllows(values []string, cwd string, roots []string) bool {
    // refuse everything you cannot resolve; approve the narrow, additive form only
}
```

Rules a shape guard must hold, all of them load-bearing:

- **Resolve every path** — `ResolvePath` / `WriteTargetUnderRoots` / `UnderRoots`,
  never a raw prefix test. `..` is refused outright, and a path that does not exist
  yet is judged by its longest existing prefix.
- **Refuse the options you did not model** — `gitWriteArgs` is the pattern: an
  explicit `--` ends the options, an unknown `-`-prefixed word refuses the call, and
  an option with a dangerous long-form must be refused by **prefix** (`--am` covers
  `--am`, `--ame` and `--amend`, which git abbreviates).
- **Never approve a write verb** — `push`, `reset`, `worktree remove`,
  `gh pr create|close|review|ready`, `gh api -X PUT` stay with the prompt.
- **Refuse a credential path**, even one this guard would otherwise allow.

### 3. A read-only verb under an existing rule

Extend the verb list (`ghRuleReadVerbs`) rather than adding a rule — a rule cannot
distinguish `gh pr view` from `gh pr close`, and the verb list can.

### 4. Something that must now be refused

Add it to `CredentialRead` or to a deny guard in `registry.go`. A deny is
unconditional: it wins over every allow in the chain, which is what makes it the
right tool for a safety rule.

## Prove it

```bash
make precommit                      # tidy, gofmt, goimports, golangci-lint, go test
DRY_RUN=1 ./scripts/install-hook.sh # build + smoke gate, no swap
```

The smoke gate is the same set of probes the real install runs: 3 allow, 3 pass,
2 deny, 1 malformed payload that must exit 2. It is deliberately independent of the
operator's allow list, so it tests the binary rather than the settings file.

Beyond the smoke gate, four instruments live in `~/.claude/state/hook-parity/` and
each answers a different question:

| Instrument | Question |
|---|---|
| `replay.py --bin <hook>` | Does the frozen baseline still replay the same? Prints a per-bucket parity table and aborts if its four control commands go blind. |
| `probes.py --check` | Is anything unsafe newly allowed? Exits non-zero if it is. |
| `would-have-parked.py` | On *live* traffic, which commands does this binary allow that a control binary refuses, and did any regress? |
| `ratio.py --start … --end …` | Escalated parks ÷ Bash executions over a window. |

⚠️ **`replay.py`'s absolute counts are not reproducible across runs.** The guard
loads the live `~/.claude/settings.json`, so its allow count moves when anyone edits
the allow list — measured 2026-11-20: 592/664 before a sibling session grew the
Bash rules from 188 to 494, 609/679 after, for the *same* binaries. Compare
`py∖go` (what the Python hook allowed and this one does not), never the absolute
number.

⚠️ **A parity measurement is not a park-rate measurement.** The Python hook itself
cleared only 42 % of the parks in the baseline window, so reaching full parity
still leaves roughly half of them parked. Judge a change by the controlled
comparison (`would-have-parked.py`, which reports regressions as well as gains), not
by a ratio target.

## Where the next increments come from

The guard is a starting point, not a finish line. The known gap list, and the
procedure that grows the allow list each month, are tracked in the vault:
`[[Headless Workers Park on Compound Routine Commands]]` and the `perm-review`
recurring task. Add a shape here, prove it with the four instruments, then record
the measurement — the next increment starts from that number.
