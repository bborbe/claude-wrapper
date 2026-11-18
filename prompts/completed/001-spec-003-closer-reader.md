---
status: completed
spec: [003-parked-observable-reopen-gate]
summary: Added LastCloser (bounded 256 KB tail reader) and ClassifyParked (card/pr/host/until classifier with security rejections) plus five focused ginkgo specs and a changelog entry; make precommit exited 0.
execution_id: claude-wrapper-parked-observable-exec-001-spec-003-closer-reader
dark-factory-version: v0.199.1
created: "2026-11-17T12:00:00Z"
queued: "2026-11-17T20:40:50Z"
started: "2026-11-17T21:23:27Z"
completed: "2026-11-17T23:21:45Z"
---

# Read the last closer and classify what it waits on

<summary>
- The wrapper can now tell what a finished session was waiting on, from the last thing that session said.
- That last message is read from the session's own event stream on disk, over a bounded tail, never from a stubbed reader.
- A half-written trailing line is ignored, so a partial write never produces a bogus closer.
- A window that starts in the middle of a line discards that fragment, so a 9 MB event file is read the same way a small one is.
- Only a message that declares an end-observable counts as a park; a plain forward pointer does not.
- Every wait declaration lands in exactly one of four classes, in a fixed precedence.
- A declaration naming something this build cannot address is classified as an expiring wait, not as a network target or a command.
- A declaration whose owner, repo, number or port would smuggle a flag or an out-of-range value into a command or a dial is refused the same way.
- Nothing in this step reads a card, runs a command, opens a connection, or writes a file.
- Existing behaviour is untouched: no other code path changes.

</summary>

<objective>
Produce the two pure pieces the reopen tick needs to tell "the worker died" from "the worker parked on something outside its control": a bounded reader that returns a session's last closer from its own event stream, and a classifier that turns that closer's `⏰ Ends:` declaration into exactly one wait class. Without them the tick reopens every ABSENT row every 15 minutes, including rows waiting on a card, a PR, a host or a date, and each reopen grows the same event file toward the 9–13 MB range where sessions have died.
</objective>

<context>
Read these before writing anything:

- `pkg/pane_guard.go` — `machineWaitEnds` is the `⏰ Ends:` literal. Reuse that constant; do not add a second copy of the string, and do not extend the pane guard's verdicts.
- `pkg/session_registry.go` — `transcriptRecord`, `messageBody`, `contentBlock`, `transcriptTailBytes`, and `LastAssistantText`. Reuse the three types. **Do not reuse `LastAssistantText`**: it walks backwards past tool-only assistant records, and the rule here is the opposite (the LAST assistant record only; a tool-only one yields no closer).
- `pkg/sessionstore.go` — `sessionEventsName` is `"events.jsonl"`; a session's event file is `<sessions-dir>/<session-id>/events.jsonl`.
- `pkg/flagged_tick_test.go` — the ginkgo style plus `fixedNow`, `rowWith`, `newTick` helpers later prompts reuse.
- `pkg/pkg_suite_test.go` — `ginkgo` and `gomega` are imported **qualified**, never dot-imported, because this package declares its own `Pending` type.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. Create `pkg/parked_closer.go`, package `pkg`, starting with the same BSD copyright header every other file in `pkg/` carries.

2. Declare `const parkedCloserTailBytes = 256 * 1024` with a GoDoc comment stating it bounds one closer read, so a 9 MB event file costs the same as a small one. Do not reuse `transcriptTailBytes`: the two windows bound different reads and must be able to change independently.

3. `func LastCloser(path string) string` — the session's last closer text, or `""`.

   - Open the file, `Stat` it, and seek to `max(0, size - parkedCloserTailBytes)`.
   - When that offset is greater than zero, discard everything up to and including the first `\n`: the window starts mid-line and a truncated JSON object must never be parsed. **This is the only path taken by an event file larger than 256 KB**, so it must be exercised by a test (requirement 7).
   - Accept only newline-terminated lines. Read with `bufio.Reader.ReadString('\n')` and drop the final fragment that returns `io.EOF` without a `'\n'`.
   - Take the **last** line whose `type` is `"assistant"`, decode it with the existing `transcriptRecord` and `messageBody`, and join the `contentBlock`s whose `Type == "text"` with `"\n"`.
   - If that last assistant line carries no text block, return `""`. Do **not** walk back to an earlier assistant line.
   - A missing file, an unreadable file, a failed open / `Stat` / `Seek`, a read that fails mid-file, or an undecodable line all return `""`. This function never returns an error and never signals a hold: no closer means the row keeps the existing clock resume.
   - A tool-only assistant line, a `result` line and a trailing partial line each yield `""`.

4. Declare the class vocabulary in the same file:

   ```go
   // ParkedClass is the closed set of observables a closer can park on.
   type ParkedClass string

   const (
       ParkedOnCard ParkedClass = "parked-on-card"
       ParkedOnPR   ParkedClass = "parked-on-pr"
       ParkedOnHost ParkedClass = "parked-on-host"
       ParkedUntil  ParkedClass = "parked-until"
   )
   ```

   The four values are the tick's skip reasons verbatim, so they are part of the contract. Do not add an `Available*` collection: the four classes are enumerated by the gate's switch and by `docs/parked-waits.md`, and a collection nothing ranges over is dead weight.

5. Declare the classifier's output:

   ```go
   // PullRequestRef is the pull request a closer names, split into separate argv
   // elements so no shell is ever involved.
   type PullRequestRef struct {
       Owner  string
       Repo   string
       Number int
   }

   // ParkedDeclaration is one classified `⏰ Ends:` declaration.
   type ParkedDeclaration struct {
       // Class is the wait's class and is also the skip reason the tick records.
       Class ParkedClass
       // Text is the declaration with the marker and surrounding space removed.
       Text string
       // PR is set for parked-on-pr.
       PR PullRequestRef
       // Host and Port are set for parked-on-host; Port is 22 when the
       // declaration names none.
       Host string
       Port int
       // Deadline is the clock a parked-until declaration names. It is nil when
       // the declaration names something this build cannot probe.
       Deadline *time.Time
   }
   ```

6. `func ClassifyParked(closer string) (ParkedDeclaration, bool)`.

   - Find the **last** line of `closer` carrying `machineWaitEnds`; take the remainder of that line, `strings.TrimSpace`d, as `Text`. No marker anywhere means `ok == false` — that is what makes `⏰ Next: nothing` alone not a park.
   - Classify `Text` in this precedence, first match wins: card, then pr, then host, then until.
   - **card**: `Text` matches `(?i)\bcard\b`. Covers `card 3f9a1c7e answered` and `the attention card 9b2f0d41 is answered`.
   - **pr**: match each whitespace-delimited token of `Text` against `^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*#([1-9][0-9]*)$`; the PR reference may be any token, so a token beginning with `-` cannot match. Store owner, repo and number in `PR`.
   - **host**: a whitespace-delimited token that is either `name:port` (name starts alphanumeric, then alphanumerics, `.` or `-`; port 1–65535) or a dotted IPv4 address of four numeric octets each 0–255. Store `Host` and `Port`; `Port` is 22 when none is named. A bare single word is not a host, a token carrying more than one `:` is not a host, and a dotted token that is not four numeric octets (`v1.2.3`) is not a host.
   - **until**: a token that parses as `time.RFC3339`, or as `2006-01-02` in local time, sets `Deadline` (first such token wins). Otherwise `Deadline` stays nil. An empty `Text` (`⏰ Ends:` with nothing after it) is `parked-until` with a nil `Deadline`.
   - The bounds above are also the **security rejections**, and each must fall through to `parked-until` with a nil `Deadline` rather than reaching a command or a dial:
     - `-owner/repo#1` — the token begins with `-`, so the anchored PR pattern cannot match it and no flag can be smuggled into the argv.
     - `owner/repo#0` — `N` must be greater than zero.
     - `sun:99999` — a port outside 1–65535 is not a host.
     - `999.1.1.1` — an octet outside 0–255 is not a host.
   - Every declaration below must classify as the table says:

     | Declaration | Class |
     |---|---|
     | `card 3f9a1c7e answered` | `parked-on-card` |
     | `the attention card 9b2f0d41 is answered` | `parked-on-card` |
     | `bborbe/claude-wrapper#412 merges` | `parked-on-pr` |
     | `the bot posts a review on bborbe/nuke#88 or CI goes red` | `parked-on-pr` |
     | `sun:22 answers` | `parked-on-host` |
     | `192.168.178.38:22 accepts a connection` | `parked-on-host` |
     | `192.168.178.38 answers` (dotted, no port, port becomes 22) | `parked-on-host` |
     | `2026-11-18T09:00:00+01:00` | `parked-until` |
     | `2026-11-18` | `parked-until` |
     | `task ABC-123 flips to a terminal status on disk` | `parked-until` |
     | `the tag v1.2.3 appears` | `parked-until` |
     | `#412 merges` (no owner/repo) | `parked-until` |
     | `sun answers` (bare hostname, no port) | `parked-until` |
     | `⏰ Ends:` with nothing after it | `parked-until` |
     | `⏰ Next: nothing` and no `⏰ Ends:` | not parked |

   - Keep `ClassifyParked` inside `funlen`'s 80-line / 50-statement limit (`make lint` enforces it): one small helper per class, plus one helper that extracts the marker line.

7. Write `pkg/parked_closer_test.go` with exactly these five specs (the fourth is a `ginkgo.DescribeTable`; its `ginkgo.Entry` cases are the ones listed there), using qualified `ginkgo` / `gomega`. Between them they must reach every branch of `LastCloser` and every security rejection in requirement 6 — `<verification>` permits no spec beyond this list, so a branch left out here stays untested.

   - `ginkgo.It("reads the last closer from a real event file", ...)` — write a **real** `events.jsonl` under a temp directory (a real file, not a stubbed reader) containing, in this order: an assistant text line, a tool-only assistant line, a `result` line, an assistant line whose text carries `⏰ Ends: card 3f9a1c7e answered` and `⏰ Next: nothing`, and a trailing partial line with no newline. Assert the extracted closer is that last complete assistant text, and that a closer carrying only `⏰ Next: nothing` classifies as not parked. Print `closer=%q parked=%t`.
   - `ginkgo.It("reads the last closer past a partial first line in a large event file", ...)` — write a real `events.jsonl` larger than `parkedCloserTailBytes`: a first line padded past the tail window (a non-assistant record with a long filler field) that is newline-terminated, followed by a newline-terminated assistant line carrying `⏰ Ends: sun:22 answers`. The assistant line is the file's last line and must end with `\n`, preceded by a newline: requirement 3 drops a final fragment that ends at `io.EOF` without a `'\n'`, so a fixture whose last line is unterminated yields `closer=""` and fails this spec for the wrong reason. Assert the file's size exceeds `parkedCloserTailBytes` (so the spec fails loudly if the constant grows), that the returned closer is the assistant text, and that it carries none of the padding — the truncated fragment was discarded rather than decoded. Print `size=%d closer=%q`.
   - `ginkgo.It("classifies a parked declaration into exactly one class", ...)` — a table-driven spec over all 15 rows of requirement 6's table. Print one `declaration=<text> class=<class>` line per case, and `class=none` for the not-parked row.
   - `ginkgo.It("returns no closer when the file cannot yield one", ...)` — a `ginkgo.DescribeTable` with one `ginkgo.Entry` per case below, each asserting `""` and printing one `case=<name> closer=%q` line:
     - `missing` — a path under the temp directory that does not exist.
     - `unreadable` — a real file written then `os.Chmod(path, 0o000)`. Inside that entry's body, when `os.Geteuid() == 0` print `case=unreadable skipped=root` and call `ginkgo.Skip("root can read a 0o000 file")` before the assertion: the container runs as root, where a `0o000` file is still readable and the case would fail for the wrong reason. The skip must live in this `ginkgo.Entry` and never in a `for` loop over cases: `ginkgo.Skip` inside a loop-driven container aborts every later case, silently dropping the remaining `returns no closer` branches while the spec still passes.
     - `directory` — the temp directory itself passed as the path: the open succeeds and the read fails, which is the mid-read error branch.
     - `truncated first line only` — a file larger than `parkedCloserTailBytes` whose tail window holds only a partial JSON fragment with no terminating newline.
     - `tool-only last assistant line` — a complete `events.jsonl` whose **last** assistant line's content is a single `tool_use` block, preceded by an earlier newline-terminated assistant line that carries text. The assertion is `""`: an implementation that walks back to that earlier assistant text returns it and fails here, which is what pins requirement 3's "do not walk back".
     - `non-JSON` — a newline-terminated line of non-JSON text.
     - `assistant with no text block` — a complete `events.jsonl` whose last assistant line has an empty `content` array.
   - `ginkgo.It("rejects a declaration it cannot address safely", ...)` — a table spec over the four rejections in requirement 6: `-owner/repo#1`, `owner/repo#0`, `sun:99999`, `999.1.1.1`. Each must classify as `parked-until` with a nil `Deadline`, an empty `PR.Owner`, and an empty `Host`. Print `declaration=%q class=%s pr_owner=%q host=%q` per case.

8. GoDoc every exported item, and GoDoc the unexported helpers where the reasoning is not obvious from the name. `LastCloser` and `ClassifyParked` are pure over a path and a string and take no `ctx`; the next prompt's probes take the context.

9. Before finishing, re-run `<verification>` and confirm it passes, then walk each numbered requirement against the code you wrote.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- **No model call anywhere in this path.**
- **`⏰ Ends:` has exactly one home**: `machineWaitEnds` in `pkg/pane_guard.go`. Do not add a second literal and do not extend the pane guard's verdicts.
- Do NOT classify a closer that carries no `⏰ Ends:` line. `⏰ Next:` is the general forward pointer, not a park.
- Do NOT touch `claude-supervisor`'s `scripts/closer-verdict.py`, `scripts/who-needs-me.py` or `scripts/declared-wait.py` — they own the `parked-wait` vocabulary and are read-only references.
- Do NOT change `flagged-rows.py`, its eligibility predicate, or the fields it emits.
- Do NOT change the 15-minute tick interval, the 50-row per-tick budget, `recentlySpawnedWindow`, `predicateTimeout`, or any spawn cap.
- Do NOT add a config key or flag, and do NOT add a per-class tunable timeout.
- The closer is **untrusted model output** that later drives an argv, a network dial and a file path. It is never interpolated into a shell, never logged verbatim, and never used as a path segment here.
- Repo conventions: `github.com/bborbe/errors` wrapping (no `fmt.Errorf`, no bare `return err`), `ctx` threaded through every collaborator call, `libtime.CurrentDateTimeGetter` for the clock, GoDoc on every exported item.
- Tests are in-package ginkgo specs; import `ginkgo`/`gomega` qualified, never dot-imported.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Then run the focused specs and confirm exit 0 plus the printed evidence lines:

```
go test -v -count=1 ./pkg -args -ginkgo.focus='reads the last closer' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='classifies a parked declaration' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='returns no closer' -ginkgo.v
go test -v -count=1 ./pkg -args -ginkgo.focus='rejects a declaration' -ginkgo.v
```

The first focus matches two specs (the plain event file and the large one); all four runs must exit 0.
</verification>
