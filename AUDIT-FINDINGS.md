# Prompt audit findings — spec 003

Verdicts from `prompt-auditor` over the five generated prompts. Prompts are NOT rewritten on
findings; the reviewer owns that decision.

**Summary: 1 of 5 passes (prompt 4). 9 blocking findings across prompts 1, 2, 3, 5.**

## prompts/1-spec-003-closer-reader.md — FAIL (1 blocking)

B1 — lines 129–132: requirement 7 ("exactly these two specs") forbids the tests requirement 3
needs, so the new file lands under 80% coverage. Untested: missing file, unreadable file,
`Stat`/`Seek` error, the `offset > 0` partial-first-line discard (the only path for a file
> 256 KB), an undecodable line, a last assistant line with no text block; plus requirement 6's
security rejections (`-owner/repo#1`, `N == 0`, port > 65535, octet > 255).

Fix: change "exactly these two specs" to "these three specs", and add two table specs —
`returns no closer when the file cannot yield one` (missing / `0o000` / truncated-first-line /
tool-only-last-line / non-JSON) and `rejects a declaration it cannot address safely`
(`-owner/repo#1`, `owner/repo#0`, `sun:99999`, `999.1.1.1` → `parked-until`).

## prompts/2-spec-003-parked-probes.md — FAIL (3 blocking)

1. Line 49 — `defaultParkedPort` declared but never used; `unused` is enabled in `.golangci.yml`
   and requirement 6 dials `strconv.Itoa(declaration.Port)` directly (prompt 1's classifier
   already guarantees `Port == 22`), so `make precommit` fails. Fix: delete the const, or use it
   when `declaration.Port == 0`.
2. Line 136 — `CardProbe` untested: four branches, zero specs, against the new-file coverage rule.
   Fix: add a third spec driving `CardProbe` with a fake `OpenCardReader`.
3. Lines 111–113 vs line 154 — `HostProbe.DialTimeout` contradicts "do NOT add a per-class
   tunable timeout" and has no caller. Fix: remove the field, or delete the conflicting sentence.

## prompts/3-spec-003-parked-baseline.md — FAIL (3 blocking)

1. Line 131 + 145 — `Logf` is never initialized: requirement 7 gives the gate a `Logf` field but
   `NewParkedGate` has no `Logf` argument and returns the `ParkedHold` interface, so prompt 4's
   5-arg call cannot set it. Requirement 8 step 8 then calls it on a `Save` failure → nil-func
   panic on exactly the path that must not fail the tick. Fix: set `Logf: log.Printf` in the
   constructor, keeping prompt 4's call site intact.
2. Lines 149–153 — `StateDirs` has no test and requirement 9 forbids adding one. It is the sole
   resolver `main.go` uses while `NewSessionStore` resolves its own; if they disagree the gate
   reads a directory sessions are never written to and every parked row reopens forever — the bug
   this spec fixes. Fix: add a `resolves the state directories like the session store` spec
   (`CLAUDE_WRAPPER_STATE_DIR` set → `<temp>/sessions` + `<temp>/parked`).
3. Line 151 — the expiry spec is self-contradictory: "call `Hold` at age 59 minutes and then at
   61" cannot be followed, because the first call has no baseline to be 59 minutes old. Fix:
   `Hold` once at `fixedNow(T0)` to record the baseline, then again at `T0+59m` and `T0+61m`.

## prompts/4-spec-003-flagged-tick-guard.md — PASS (0 blocking)

Verified: sections and frontmatter; `spec:` slug resolves; insertion point exact (after
`decision(...)`, before `Caps.CheckWorker`, so a hold spends no budget); one `FlaggedTick{}`
construction site; one call site per changed function; no name collisions; `<verification>`
container-safe (`make precommit`, `go test -ginkgo.focus`, a grep); `/home/node/.claude/...` doc
paths resolve via `claudeDir: ~/.claude-yolo`; fail-closed probes covered; frozen PR argv asserted
for two repos; no YAGNI knob; CHANGELOG anchor `## v0.30.0` correct.

## prompts/5-spec-003-parked-waits-docs.md — FAIL (2 blocking)

1. Line 27 — tells the agent to match a "See also" line that `docs/eligibility-predicate.md` does
   not contain. Fix: say it has none today and to append one.
2. Line 86 — says to add the CHANGELOG entry under a new `## Unreleased`, while line 71 forbids
   touching `CHANGELOG.md` and the repo head is `## v0.30.0`; following it duplicates prompt 4's
   entry. Fix: forbid editing unless the entry is absent, and never edit a released section.
