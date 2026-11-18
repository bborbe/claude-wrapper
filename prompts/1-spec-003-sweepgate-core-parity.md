---
spec: ["003-sweep-gate-port"]
status: draft
created: "2026-11-18T00:35:03Z"
---

# sweepgate core: config, paths, task parsing, digest, parity fixture

<summary>
- A new Go package inside the daemon can resolve one sweep subject into the set of tracked tasks
- Task frontmatter is parsed the same way the vault-side script parses it, including all four `goals:` shapes
- The change signal (the digest) is computed by Go and is bit-identical to the one the Python script computes for the same tasks
- A checked-in fixture vault reproduces a real subject's inputs, so parity can be tested without touching the operator's vault
- A generator script captures reference output from the Python script and stamps every captured file with the script's own revision, so a "golden" produced by the Go port cannot pass
- Every state file keeps its existing path, name and JSON shape, so the switchover produces no spurious change and loses no stored digest
- Subject names are turned into file names by one hardened function that cannot escape the state directory
- No vault path is hardcoded; the vault is an argument
</summary>

<objective>
Create `pkg/sweepgate` and give it the half of the gate that has nothing to do with rendering: configuration and path resolution, task parsing, tracked-set resolution, the digest, the state-file readers/writers, and the checked-in parity fixture with its generator and captured reference output. This is the foundation every later prompt builds on; nothing here renders a frame.
</objective>

<context>
Read `README.md` for project conventions, and `pkg/flagged_tick.go` for the shape a package-level tick takes in this repo (`FlaggedTick` is a struct with exported collaborator fields plus `Run(ctx) error`; the clock is `libtime.CurrentDateTimeGetter` and is a field, never `time.Now()`).

Files to read before changing anything:
- `pkg/flagged_tick.go` — struct-of-collaborators style, `Now libtime.CurrentDateTimeGetter`, `Interval`, `Logf func(format string, args ...any)`, `Run(ctx) error`, package-level constants with doc comments explaining the measurement behind them.
- `pkg/flagged_rows_source.go` — the `CommandRunner` interface (`Run(ctx, name string, args ...string) ([]byte, error)`) and `ExecCommandRunner`, the repo's pattern for shelling out to a script without spawning it in tests.
- `pkg/subject_state.go` — `baseSlug` / `nonSlug` (`regexp.MustCompile(`[^a-z0-9]+`)`), the `worker-manager` and `sweep-gate` state-directory defaults, and the `os.UserHomeDir()` + `filepath.Join` default-resolution idiom.
- `pkg/attention_read.go` — `readJSONArray`, the repo's rule that only a 200 is a read and a decode failure is "could not read it", never an empty result.
- `pkg/pkg_suite_test.go` — the suite shape; this new package's suite follows `pkg/handler/handler_suite_test.go` (dot-imported `ginkgo`/`gomega`, `RunSpecs(t, "Sweepgate Suite")`).
- `pkg/flagged_tick_test.go` — test style: `ginkgo.Describe` / `ginkgo.It`, `ginkgo.GinkgoWriter.Printf` for evidence lines, `ginkgo.GinkgoT().TempDir()`.

The port's reference implementation is `testdata/sweepgate/reference/sweep-gate.py` — a frozen, verbatim copy of the vault-side script at the pinned revision. Read it; never edit it, never import it from Go, and never wire it into the daemon. The functions you are porting are `split_frontmatter`, `fm_scalar`, `fm_session_id`, `fm_metrics_session_ids`, `session_id_set`, `fm_wikilinks`, `fm_list`, `progress_hash`, `checkbox_count`, `declared_members`, `read_task`, `resolve_tracked`, `digest_of`, `resolve_subject`, `page_type_of`, `slug`, `state_path`, `tick_path`, `card_path`, `settled_path`, `load_state`, `load_stored`, `save_stored`, `write_tick`, `write_card`, `load_settled`, `save_settled`, and the constants block (`DEFAULT_VAULT`, `STATE_DIR`, `SETTLED_DIR`, `FEED_DIR`, `HOLDS_PATH`, `PREDISPATCH_DIR`, `WORKER_MANAGER_DIR`, `EXIT_NOCHANGE`, `EXIT_CHANGE`, `EXIT_USAGE`, `EXIT_FAILED`, `TASKS_INDEX_VERSION`).

<!-- OPEN QUESTION (reviewer): the container mounts only this repo. The vault script
     is therefore reachable ONLY through `testdata/sweepgate/reference/sweep-gate.py`.
     That copy must exist before this prompt runs — it cannot be produced from inside
     the container. If it is absent, stop and report it (requirement 1). Its long-term
     fate after the vault copy is deleted is undecided: keeping it makes `generate.sh`
     re-runnable, deleting it makes the captured goldens the only evidence. This prompt
     assumes KEEP. -->

Coding guides (in container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-package-layout-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
1. **Verify the reference exists, and stop if it does not.**
   `testdata/sweepgate/reference/sweep-gate.py` must be present before you write code. If it is missing, do not create it, do not synthesise goldens, and do not continue: report the exact missing path as a blocker and stop. Everything below is a port of that file; guessing from this prompt is not an acceptable substitute.

2. **Create the package `pkg/sweepgate`** (`package sweepgate`, copyright header identical to `pkg/flagged_tick.go`, `Sweepgate Suite` bootstrap in `pkg/sweepgate/sweepgate_suite_test.go`). Tests are in-package (`package sweepgate`) with QUALIFIED `ginkgo.` / `gomega.` imports, following `pkg/pkg_suite_test.go` — the guards below are unexported and must be testable directly.

3. **`pkg/sweepgate/config.go` — `Config` and defaults.**
   ```go
   type Config struct {
       Vault             string
       Subject           string
       StateDir          string
       SettledDir        string
       FeedDir           string
       HoldsPath         string
       PredispatchDir    string
       WorkerManagerDir  string
       RegistryDir       string
       StoreURL          string
       Now               libtime.CurrentDateTimeGetter
       Interval          time.Duration
       ReadTimeout       time.Duration
       Logf              func(format string, args ...any)
   }
   ```
   - `func (c Config) Validate(ctx context.Context) error` — `Vault` and `Subject` are required. A missing vault is an error naming the field; it is never defaulted. (AC7: the vault is a constructor argument and no vault path is hardcoded anywhere in the package.)
   - `func (c Config) WithDefaults(ctx context.Context) (Config, error)` — fills the empty fields. Precedence for each directory/URL is: explicit `Config` field, then the `SWEEP_GATE_*` environment variable, then the built-in default. The variables and defaults are exactly: `SWEEP_GATE_STATE_DIR` → `~/.claude/state/sweep-gate`; `SWEEP_GATE_SETTLED_DIR` → `~/.claude/state/sweep-gate-settled`; `SWEEP_GATE_FEED_DIR` → `~/.claude/state/attention`; `SWEEP_GATE_HOLDS` → `~/.claude/state/session-holds.json`; `SWEEP_GATE_PREDISPATCH_DIR` → `~/.claude/state/manager-predispatch`; `SWEEP_GATE_WORKER_MANAGER_DIR` → `~/.claude/state/worker-manager`; `SWEEP_GATE_STORE_URL` (then `ATTENTION_STORE_URL`) → `http://localhost:18080`. `RegistryDir` defaults to `~/.claude/sessions`. `Now` defaults to `libtime.NewCurrentDateTime()`, `Interval` to `DefaultInterval` (15 minutes, doc-commented with the same reasoning as `DefaultFlaggedTickInterval`), `ReadTimeout` to `DefaultReadTimeout` (25 minutes, doc-commented: it bounds a hung read, never a slow one). `Logf` defaults to `log.Printf`.
   - Resolve `~` with `os.UserHomeDir()` + `filepath.Join`, the way `pkg/subject_state.go` does. Do not use `os.ExpandEnv` on a path.

4. **`pkg/sweepgate/paths.go` — `Slug` and the five state paths.**
   - `func Slug(ctx context.Context, subject string) (string, error)` reproduces `re.sub(r"[^a-z0-9]+", "-", topic.lower()).strip("-")` exactly, then REFUSES a result that is empty, is `.` or `..`, or contains a path separator. Return `errors.Errorf(ctx, ...)` naming the offending subject. This is the only thing between a vault-authored subject name and a path (see the spec's Security section).
   - `func StatePath(cfg Config, subject string) (string, error)` → `<StateDir>/<slug>.json`; `TickPath` → `<StateDir>/<slug>.tick.txt`; `CardPath` → `<StateDir>/<slug>.card.json`; `SettledPath` → `<SettledDir>/<slug>.json`. A sixth helper `CadencePath(cfg Config, branch, subject string) (string, error)` → `<StateDir>/<branch>--<slug>.cadence` — the branch prefix is load-bearing (a topic and a goal may share a name); its consumer arrives in the next prompt, so leave it unexported only if nothing else needs it yet.
   - Every path helper takes `ctx` and returns `error`; none may call `Slug` and ignore its error.

5. **`pkg/sweepgate/task.go` — frontmatter and task parsing.** Port these, preserving the exact output shapes:
   - `SplitFrontmatter(text string) (string, bool)` — `^---\n(.*?)\n---` with dot-matches-newline; no match → `false`.
   - `FMScalar(fm, key string) string`, `FMSessionID(fm, key string) string` (a null-ish scalar is no id, never the string `"null"`), `FMMetricsSessionIDs`, `SessionIDSet(fm string) []string`, `FMWikilinks(fm, key string) []string` (all four shapes the vault writes: block list, inline flow list, quoted-wikilink scalar, bare-name scalar), `FMList(fm, key string) []string` (both `[[Name]]` and bare `Name`).
   - `ProgressHash(text string) string` — sha256 of the `# Progress` section body (empty section or no section → sha256 of `""`), hex, first 16 characters.
   - `CheckboxCount(text, section string) string` — `n/m`, `—` when the section has no boxes or the section is absent; excludes struck rows per the reference's three measured rules (content begins with a struck wikilink, the box is `[ ]`, trailing prose is allowed); splits lines on `"\n"` only, never `strings.Split`-on-`SplitLines`.
   - `DeclaredMembers(topicPath string) ([]string, error)` — only `- [[...]]` list items inside `## Goals`, section ends at the next `##` or `#` (a `###` does NOT end it); no `## Goals` section is an error.
   - `type Task struct` carrying the frontmatter-derived fields the reference's `read_task` returns: `Name`, `Status`, `Phase`, `OperatorFlagged bool`, `Role`, `Session string`, `SessionIDs []string`, `Goals []string`, `BlockedBy []string`, `DeferDate string` (quotes stripped), `DeclaredWait string` (quotes stripped), `ProgressHash string`, `Met string`, `Mtime time.Time`. Later prompts add render-only fields to this struct; do not add them here and do not rename these.
   - `func ParseTask(name, text string, mtime time.Time) (Task, bool)` — `false` when there is no frontmatter.
   - `func ReadTask(ctx context.Context, path string) (Task, bool, error)` — reads the file, `fstat`s the descriptor it just read (never a second `stat` on the path) for `Mtime`; an unreadable file returns `false` with a nil error, matching the reference's `return None`.

6. **`pkg/sweepgate/snapshot.go` — tracked set and subject resolution.**
   ```go
   type Snapshot struct {
       Branch  string
       Members []string
       Tasks   []Task
   }
   type Snapshotter interface {
       Snapshot(ctx context.Context, subject string) (Snapshot, error)
   }
   func NewVaultSnapshotter(cfg Config) Snapshotter
   ```
   - `ResolveSubject(ctx, vault, subject string) (branch string, members []string, err error)` — `24 Goals/<subject>.md` exists → `"goal"` with members `[subject]`; `23 Topics/<subject>.md` exists AND its `page_type:` is `topic` → `"topic"` with `DeclaredMembers`; both → an error naming both paths; neither → an error naming both directories. The asymmetry (folder alone vs folder plus page test) is the reference's and must not be "fixed".
   - `ResolveTracked(ctx, vault string, members []string) ([]Task, error)` — a member is a goal (admits tasks whose `goals:` name it) or a task name declared directly; results sorted by name; an unreadable `25 Tasks/` yields an empty set with a nil error. You may port the per-vault selection index (`tasks-index-<sha12>.json`, version-checked, validated by `(mtime, size)`) or read every task file — but if you skip the index, say so in the package doc and keep the index version constant out of the code. Do not add a cache that can change an answer.
   - `NewVaultSnapshotter(cfg)` composes the two and returns the branch, the members and the tasks.

7. **`pkg/sweepgate/digest.go` — `func Digest(tasks []Task) string`.** Byte-for-byte the reference's `digest_of`: for each task sorted by name, sha256 over
   `<name>|<status>|<phase>|<session>|<progress_hash>|<liveness>|<0 or 1>\n`
   then the literal line `--members--\n`, then each name sorted, each followed by `\n`. `liveness` defaults to `none` and `stuck` to false when the caller has not enriched them (the fixture calls the digest before enrichment). Hex output, full length. Add a doc comment stating that this string is a cross-tool contract: the vault-side script and the manager-predispatch port both compute it, and a field added here silently changes what "unchanged" means.

8. **`pkg/sweepgate/state.go` — state files, formats frozen.**
   - `type BusyStamp struct { Since float64 \`json:"since"\`; Progress string \`json:"progress"\` }` — the cross-tick busy clock the state file carries.
   - `type State struct` with `Topic string \`json:"topic"\``, `Digest string \`json:"digest"\``, `Members int \`json:"members"\``, `BusySince map[string]BusyStamp \`json:"busy_since"\``, `RecordedAt string \`json:"recorded_at"\``.
   - `LoadState(ctx, path string) (State, error)` and `LoadStored(ctx, path string) (digest string, failReason string, err error)` — a missing file is `failReason = "first run — no state file"`; an unreadable file is `state unreadable (<reason>)`; a parse error is `state parse error (<reason>)`; a parsed object with no `digest` key is `state parse error (no digest key)`. Every one of those returns a non-empty `failReason` and no error, which forces a full sweep.
   - `SaveStored(ctx, path string, state State) error` — atomic (`<path>.tmp` then `os.Rename`), `json.MarshalIndent` with two-space indent, `RecordedAt` in local time with seconds precision (`time.RFC3339`).
   - `LoadSettled(ctx, path string) (Settled, error)` / `SaveSettled(ctx, path string, settled Settled) error` with `type Settled struct { Subject string; RowKey string; RecordedAt string }` and the JSON tags `subject`, `row_key`, `recorded_at`. A missing or half-written record is "no memory" (`Settled{}`, nil error), never an error — the safe direction is a re-post, never a silence.
   - `WriteTickFile(ctx, path, frame string) error` — atomic, mode `0600` at creation (`os.OpenFile` with `0o600`, never a `chmod` after the write), content is `StripLinks(frame)` with trailing newlines normalised to exactly one. `StripLinks(text string) string` drops OSC 8 sequences (`\x1b]8;...ST`); it is unexported-in-spirit but needed by the render prompt, so export it.
   - `WriteCardFile(ctx, path string, payload []byte) error` — `payload == nil` REMOVES the file (a stale payload would re-post an answered card) and a missing file is not an error; otherwise atomic write of `json.MarshalIndent` bytes.
   - The record's `recorded_at` uses the injected clock (`cfg.Now.Now().Time()`), never `time.Now()`.

9. **`testdata/sweepgate/` — the fixture vault and its inputs.**
   Build a checked-in fixture vault under `testdata/sweepgate/vault/` with the same shape the reference's own fixture uses:
   - `23 Topics/Parity Topic.md` with `page_type: topic`, a `## Goals` list naming `Parity Goal`, and a `# Success Criteria` section with ticked and unticked boxes (so the topic row's Met cell is non-trivial).
   - `24 Goals/Parity Goal.md` with `status: in_progress` and a `# Success Criteria` section with at least one struck row (`- [ ] ~~[[...]]~~ — reason`) so the struck-row rule is exercised.
   - `25 Tasks/` with at least: one `completed` task with a session id; one `aborted` task; one `hold`; one `backlog`; one `next`/`todo` (unapproved); one `in_progress`/`planning` (approved, not started); one `in_progress`/`execution` with `role: human` and no session; one `in_progress`/`human_review`; one `in_progress`/`execution` with `declared_wait:`; one `in_progress`/`execution` with an unmet `blocked_by`; one `in_progress`/`execution` with `defer_date` in the future; one task naming TWO goals (to pin "listed once, under its first member goal"); and one task whose `goals:` names a goal that is not a member (the control that must stay out of the set). Give two of them `metrics_sessions:` ids so the Session cell's id-set rule is exercised.
   - `testdata/sweepgate/inputs/` with: an empty `registry/` (no live sessions — this is what keeps the frame link-free and byte-stable), a `feed/` with one `*.events.jsonl` carrying an open `permission` gate for one session and a `Stop` closer panel for another, a `session-holds.json` holding one session whose `held_at` is deliberately MALFORMED (so the rendered age is `?` and the golden cannot drift), and a `manager-predispatch/` verdict cache with one `needs-you` verdict.
   - No live sessions anywhere in the fixture: the renderer shells out to resolve jump links for `[sid]` cells, and a live fixture row would make the frame depend on the operator's machine.
   - **No `phase: execution` task in the fixture may carry a checkbox.** Branch (b) of the `stuck` rule compares the task file's MTIME against 30 minutes, so a checked-box execution row renders `⚠️ problem` or `🔄 progressing` depending on when the repository was checked out — the golden would then disagree with a render run days later. Keep the execution rows box-free so that branch can never fire; the `⚠️ problem` cell is covered by the hand-derived case, which builds its `Snapshot` directly with `Stuck` set.
   - The deferred row's `defer_date` is a far-future fixed date (e.g. `2099-01-01`), never a relative one, for the same reason.
   - The goldens are FIRST-RUN renders: `generate.sh` points the state directory at an empty throwaway copy, so `changed` is true, the marker reads `change`, and a render run against an empty state directory reproduces it.

10. **`testdata/sweepgate/generate.sh` — the only producer of goldens.**
    - Runs `python3 testdata/sweepgate/reference/sweep-gate.py` (repo-relative; never a host path) against `testdata/sweepgate/vault/`, with `SWEEP_GATE_STATE_DIR`, `SWEEP_GATE_SETTLED_DIR`, `SWEEP_GATE_FEED_DIR`, `SWEEP_GATE_HOLDS`, `SWEEP_GATE_PREDISPATCH_DIR`, `SWEEP_GATE_WORKER_MANAGER_DIR` and `SWEEP_GATE_STORE_URL` pointed at `testdata/sweepgate/inputs/` and a throwaway copy of it, and `BOX_TABLE_NO_LINKS=1` exported.
    - Writes, for the case name `parity-topic`:
      1. `testdata/sweepgate/parity-topic.render.golden.txt` — from `--render`, prefixed by exactly one first line `# generated by sweep-gate.py@<sha8>` where `<sha8>` is the first 8 hex characters of `sha256sum testdata/sweepgate/reference/sweep-gate.py`.
      2. `testdata/sweepgate/parity-topic.render.stderr.golden.txt` — the `--render` run's stderr (the `park:` line and any failure line).
      3. `testdata/sweepgate/state/parity-topic.json` — the state file a `--tick` run writes, copied out of the throwaway state dir.
      4. `testdata/sweepgate/parity-topic.tick.golden.txt` — the `--tick` run's stdout, same marker first line.
    - The script must be idempotent and must fail loudly (`set -eu`, non-zero exit) if any of the four outputs is missing or empty. Make it executable (`chmod +x`).
    - **Handle the pair already on this branch without fabricating provenance.** `testdata/sweepgate/development-pipeline-throughput.render.golden.txt` and `development-pipeline-throughput.render.stderr.txt` are a LIVE-vault capture, not a fixture case: the frame has 1293 tasks and cannot be reproduced from `testdata/sweepgate/vault/`. Do NOT regenerate them, and do NOT add a marker line by hand. Move both files, bytes unchanged, to `testdata/sweepgate/live/development-pipeline-throughput.render.txt` and `testdata/sweepgate/live/development-pipeline-throughput.render.stderr.txt`. They are the post-deploy diff reference (a later acceptance criterion diffs a live tick file against that captured frame), and they are NOT part of the parity set — the parity test globs `<case>.render.golden.txt` under `testdata/sweepgate/`, so it must not see them. Record their role in `docs/sweep-gate.md` when the documentation prompt runs.
    - `generate.sh` takes a case name and a `--vault` argument, so it can be pointed at any vault; the checked-in default is `testdata/sweepgate/vault`. It never touches `testdata/sweepgate/live/`.
    - Never generate a golden by running Go code. The marker is the proof.

11. **Tests (in `pkg/sweepgate`).** Each `Describe`/`It` text below must appear verbatim, because the spec's verification greps for it.
    - `Describe("parses every frontmatter shape", ...)` — the four `goals:` shapes, `goals: []` staying empty, a null `claude_session_id` staying empty, `metrics_sessions` ids.
    - `Describe("resolves the tracked set from a fixture vault", ...)` — against `testdata/sweepgate/vault`, the set is exactly the expected names (the untracked-goal control absent), sorted; the multi-goal task appears once.
    - `Describe("reads a state file written by the python", ...)` — read `testdata/sweepgate/state/parity-topic.json`, take its `digest` as `python_digest`, recompute `Digest` over the same fixture vault with `liveness=none` and `stuck=false`, and assert equality. Print, via `ginkgo.GinkgoWriter.Printf`, exactly:
      `file=%s python_digest=%s go_digest=%s equal=%t`
      This is AC8's evidence line. If the two digests disagree, fix the Go digest — never edit the state file.
    - `Describe("rejects a traversal subject", ...)` — `../../etc/x`, `..`, `.`, `""`, and a subject that slugs to empty; each returns an error and no path is produced. Assert that a subject containing `../` never yields a path whose `filepath.Clean` escapes `StateDir`.
    - `Describe("computes the digest from the rendered state", ...)` — each of status, phase, session id, task-added, task-removed and a new `# Progress` entry moves the digest, and a mutation to a task outside the tracked set does not. This is the reference fixture's FIVE-WAY group; keep the control.

12. **`CHANGELOG.md`.** Ensure exactly one `## Unreleased` heading exists (create it directly above the first `## v` heading, blank line either side). Add one bullet:
    `- feat: pkg/sweepgate ports the manager sweep gate's task parsing, tracked-set resolution and digest to Go, with a checked-in parity fixture and Python-generated goldens`
    Never edit a released section.

13. **Self-check before finishing.** Re-run `<verification>` and confirm it passes. Walk requirements 1-12 against the change. Confirm `grep -rn 'Obsidian/Personal' pkg/sweepgate/` prints nothing (AC7) and that no file in the package calls `time.Now()` outside a doc comment (the clock is injected).
</requirements>

<constraints>
- The vault path is a constructor argument; no vault path is hardcoded anywhere in `pkg/sweepgate` (AC7). The Python's `DEFAULT_VAULT` must NOT be ported.
- The state file formats are frozen for the switchover (AC8): `<slug>.json`, `<slug>.tick.txt`, `<slug>.card.json`, `<branch>--<slug>.cadence`, and the settled record in its own directory. Renaming a field costs a spurious change on the first tick and a lost digest.
- The `SWEEP_GATE_{STATE_DIR,SETTLED_DIR,FEED_DIR,HOLDS,PREDISPATCH_DIR,WORKER_MANAGER_DIR}` overrides are preserved: a second host relies on `STATE_DIR` and the reference fixture monkeypatches the rest.
- `slug()` is the only thing between a vault-authored subject and a path. It is applied before every path join and rejects an empty result or one containing `..`.
- Parsed JSON from disk is untrusted: a malformed record reads as "no record", never as a fatal error.
- `github.com/bborbe/errors` only (`errors.Wrap`, `errors.Wrapf`, `errors.Errorf`, `errors.Is`); `ctx` threaded through every call that can fail. Do not introduce `fmt.Errorf`.
- Ginkgo v2 + Gomega, in-package tests, qualified (non-dot) imports, one suite (`Sweepgate Suite`). No counterfeiter mocks — use real files in `ginkgo.GinkgoT().TempDir()` or the checked-in fixture.
- `testdata/sweepgate/reference/sweep-gate.py` is a frozen reference: never edit it, never import it from Go, never wire it into the daemon.
- Do NOT shell out to `box-table.py` or any Python from production code in this prompt.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Additional checks (all container-executable):
- `ls pkg/sweepgate/*.go | wc -l` — prints a number greater than 0.
- `! grep -q 'Obsidian/Personal' pkg/sweepgate/*.go` — no hardcoded vault path (AC7). Use `! grep -q`, not `grep -c`: `grep -c` exits 1 when the count is zero.
- `ls testdata/sweepgate/parity-topic.render.golden.txt testdata/sweepgate/parity-topic.render.stderr.golden.txt testdata/sweepgate/state/parity-topic.json` — all three exist.
- `head -1 testdata/sweepgate/parity-topic.render.golden.txt | grep -qE '^# generated by sweep-gate\.py@[0-9a-f]{8,}$'` — the provenance marker is present.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='reads a state file written by the python' -ginkgo.v` — exits 0 and prints `file=… python_digest=… go_digest=… equal=true`.
- `go test -v -count=1 ./pkg/sweepgate -args -ginkgo.focus='rejects a traversal subject' -ginkgo.v` — exits 0.
- `grep -c '^## Unreleased' CHANGELOG.md` — prints `1`.
</verification>
