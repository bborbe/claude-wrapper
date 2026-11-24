// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/claude-wrapper/pkg"
)

// startTickJobs starts the launchd-replacement lifecycle jobs and the gated
// keepalive tick.
func startTickJobs(
	ctx context.Context,
	supervisor pkg.Supervisor,
	keepalive bool,
	registry pkg.SessionRegistry,
	heartbeats pkg.HeartbeatReader,
	vaults pkg.VaultLister,
	tabs pkg.TabClient,
	surface *pkg.OpenSurface,
	stateDir string,
	now libtime.CurrentDateTimeGetter,
) {
	startLifecycleJobs(ctx, supervisor, now, registry, heartbeats, vaults)
	startKeepaliveIfEnabled(ctx, keepalive, registry, vaults, tabs, surface, stateDir, now)
}

// startKeepaliveIfEnabled assembles and starts the keepalive tick when the
// -keepalive flag is set. Only the :18090 daemon sets it.
func startKeepaliveIfEnabled(
	ctx context.Context,
	enabled bool,
	registry pkg.SessionRegistry,
	vaults pkg.VaultLister,
	tabs pkg.TabClient,
	surface *pkg.OpenSurface,
	stateDir string,
	now libtime.CurrentDateTimeGetter,
) {
	if !enabled {
		return
	}
	job, err := newKeepaliveJob(ctx, registry, vaults, tabs, surface, stateDir, now)
	if err != nil {
		log.Printf("WARNING: keepalive job assembly failed: %v", err)
		return
	}
	startKeepaliveJob(ctx, job)
}

// startKeepaliveJob runs the keepalive tick as a background job, gated on the
// -keepalive flag by the caller. A nil job (empty state base) is a refusal, not a
// quiet tick.
func startKeepaliveJob(
	ctx context.Context,
	job *pkg.KeepaliveJob,
) {
	if job == nil {
		log.Printf("WARNING: keepalive job not started (empty state base)")
		return
	}
	go func() {
		if err := job.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("WARNING: keepalive job stopped: %v", err)
		}
	}()
}

// keepaliveFilteredVaults returns the configured vaults with `seibert-brogrammers`
// removed by NAME — the ONE list every per-vault leg shares.
func keepaliveFilteredVaults(ctx context.Context, vaults pkg.VaultLister) ([]pkg.Vault, error) {
	all, err := vaults.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]pkg.Vault, 0, len(all))
	for _, vault := range all {
		if vault.Name == "seibert-brogrammers" {
			continue
		}
		out = append(out, vault)
	}
	return out, nil
}

// keepaliveOpener builds the resume seam over the mounted open surface.
func keepaliveOpener(surface *pkg.OpenSurface) pkg.KeepaliveOpener {
	return func(ctx context.Context, row pkg.OpenRow) error {
		body, err := json.Marshal(pkg.OpenRequestBody{Rows: []pkg.OpenRow{row}})
		if err != nil {
			return errors.Wrap(ctx, err, "marshal open row failed")
		}
		_, serveErr := surface.Serve(ctx, body, "")
		return serveErr
	}
}

// keepaliveScript resolves a script under SupervisorScriptsDir.
//
// A package VAR, not a func, so a fixture can substitute a runner and assert the
// EXACT arguments a decide step hands a script — the only way to catch a wrong
// argument (a vault NAME where the script wants a PATH) that still exits 0 and
// prints a plausible verdict.
var keepaliveScript = func(name string) pkg.KeepaliveScriptRunner {
	return pkg.NewKeepaliveScriptRunner(pkg.SupervisorScriptsDir + "/" + name)
}

// keepaliveLister builds a lister over the filtered vaults for one kind.
func keepaliveLister(
	filtered []pkg.Vault,
	require, kind, cursorPath string,
	logf func(string),
) *pkg.KeepaliveLister {
	folderFor := pkg.NewKeepaliveFolderFor(keepaliveScript("resolve-folders.py"), require)
	return &pkg.KeepaliveLister{
		Source: pkg.NewKeepalivePageSource(filtered, folderFor, logf),
		Cursor: pkg.NewKeepaliveCursorFile(cursorPath),
		Kind:   kind,
		Cap:    20,
	}
}

// newKeepaliveJob assembles the eight procedures and the two pre-steps.
func newKeepaliveJob(
	ctx context.Context,
	registry pkg.SessionRegistry,
	vaults pkg.VaultLister,
	tabs pkg.TabClient,
	surface *pkg.OpenSurface,
	stateDir string,
	now libtime.CurrentDateTimeGetter,
) (*pkg.KeepaliveJob, error) {
	sessionsDir, _, err := pkg.StateDirs(stateDir)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "resolve state dirs failed")
	}
	base := filepath.Dir(sessionsDir)
	if strings.TrimSpace(base) == "" || base == "." {
		return nil, errors.New(ctx, "keepalive state base is empty — refusing a cwd-relative state path")
	}
	filtered, err := keepaliveFilteredVaults(ctx, vaults)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list vaults failed")
	}
	logf := func(line string) { log.Printf("%s", line) }

	store := pkg.NewPendingRecordDir(filepath.Join(base, "pending-records"))
	topicLister := keepaliveLister(filtered, "topics_dir", pkg.KeepaliveKindTopic,
		filepath.Join(base, "keepalive-cursor-topics.json"), logf)
	taskLister := keepaliveLister(filtered, "tasks_dir", pkg.KeepaliveKindTask,
		filepath.Join(base, "keepalive-cursor-tasks.json"), logf)
	paneResolver := newPaneResolver(registry)
	act := pkg.NewKeepaliveAct(
		keepaliveOpener(surface),
		pkg.NewKeepalivePaneSender(tabs, paneResolver, now),
		nil, // the no-kill tripwire is never called in production
		func(sessionID string) string { return "/supervisor:restart " + sessionID },
	)
	hold := pkg.NewKeepaliveHold(keepaliveScript("session-holds.py"))
	claim := pkg.NewKeepaliveClaim(keepaliveScript("topic-keepalive.py"))

	procedures := newKeepaliveProcedures(filtered, registry, base, topicLister, taskLister, act, hold, claim, store, tabs, paneResolver, now)

	return &pkg.KeepaliveJob{
		PreSteps:   []func(context.Context) error{newPendingDrain(registry, store, now)},
		Procedures: procedures,
		Logf:       logf,
	}, nil
}

// newKeepaliveProcedures wires the eight keep-alive procedures.
func newKeepaliveProcedures(
	filtered []pkg.Vault,
	registry pkg.SessionRegistry,
	base string,
	topicLister, taskLister *pkg.KeepaliveLister,
	act func(context.Context, string, pkg.LapsedManagerDecision) error,
	hold pkg.KeepaliveHold,
	claim func(context.Context, string) (bool, error),
	store pkg.PendingRecordStore,
	tabs pkg.TabClient,
	paneResolver func(string) (string, bool),
	now libtime.CurrentDateTimeGetter,
) []pkg.KeepaliveProcedure {
	return []pkg.KeepaliveProcedure{
		&pkg.LapsedManagerProcedure{
			Action:  "lapsed-topic-manager",
			Decide:  newManagerDecide(topicLister, filtered, "topic"),
			Claim:   claim,
			Hold:    hold,
			Act:     act,
			Pending: store,
			Now:     now,
		},
		&pkg.LapsedManagerProcedure{
			Action:  "topicless-goal-manager",
			Decide:  newTopiclessDecide(filtered),
			Claim:   claim,
			Hold:    hold,
			Act:     act,
			Pending: store,
			Now:     now,
		},
		&pkg.DeadWorkerProcedure{
			Decisions: newWorkerDecisions(taskLister, filtered),
			Claim:     claim,
			Hold:      hold,
			Act:       act,
			Pending:   store,
			Now:       now,
		},
		&pkg.DecisionParkedWorkerProcedure{
			Decide:  newDecisionParkDecide(filtered, base),
			Act:     act,
			Pending: store,
			Now:     now,
		},
		&pkg.DueParkWatchProcedure{
			DueTasks: pkg.NewParkEventDue(keepaliveScript("park-event.py")),
			// The same index the flagged tick reads: a `blocked_by` row must be
			// skipped on the same rule in both legs, or the keep-alive re-spawns a
			// row the tick has already refused as `blocked`.
			Blockers: pkg.NewVaultAPIBlockers(pkg.HTTPVaultAPIClient{BaseURL: *flaggedTickVaultAPIURL}),
			Resume:   newDueParkResume(filtered, act),
		},
		&pkg.ParkNudgeProcedure{
			Nudges: newParkNudges(filtered),
			Send:   pkg.NewKeepalivePaneSender(tabs, paneResolver, now),
		},
		&pkg.StoodDownCloseProcedure{
			Marker: newStoodDownMarker(registry, filtered),
		},
		&pkg.StalledTabsProcedure{
			Rows: newStalledTabRows(keepaliveScript("stalled-tab-restart.py"), filtered),
		},
	}
}

// newPaneResolver resolves a session id to its pane via the registry pid.
func newPaneResolver(registry pkg.SessionRegistry) func(sessionID string) (string, bool) {
	joiner := pkg.NewWeztermPaneJoiner("")
	return func(sessionID string) (string, bool) {
		ctx := context.Background()
		entries, err := registry.Live(ctx)
		if err != nil {
			return "", false
		}
		pid := 0
		for _, entry := range entries {
			if entry.SessionID == sessionID {
				pid = entry.PID
				break
			}
		}
		if pid == 0 {
			return "", false
		}
		panes, err := joiner.PanesForPIDs(ctx, []int{pid})
		if err != nil {
			return "", false
		}
		pane, ok := panes[pid]
		return pane, ok
	}
}

// newPendingDrain records pending resumes on a later tick, dispatching on Kind.
func newPendingDrain(
	registry pkg.SessionRegistry,
	store pkg.PendingRecordStore,
	now libtime.CurrentDateTimeGetter,
) func(context.Context) error {
	live := func(ctx context.Context, sessionID string) (bool, error) {
		entries, err := registry.Live(ctx)
		if err != nil {
			return false, errors.Wrapf(ctx, err, "read live registry failed")
		}
		for _, entry := range entries {
			if entry.SessionID == sessionID {
				return true, nil
			}
		}
		return false, nil
	}
	record := func(ctx context.Context, pending pkg.PendingRecord) error {
		switch pending.Kind {
		case "manager":
			return pkg.NewKeepaliveRecordResume(keepaliveScript("topic-keepalive.py"))(ctx, pending.SessionID)
		case "worker":
			return pkg.NewWorkerRecordResume(keepaliveScript("worker-keepalive.py"))(ctx, pending.Vault, pending.Task, pending.SessionID)
		case "decision-park":
			_, code, err := keepaliveScript("decision-park-resume.py")(ctx, "--record", pending.Task, "--sid", pending.SessionID)
			if err != nil {
				return errors.Wrapf(ctx, err, "decision-park record %s failed", pending.SessionID)
			}
			if code != 0 {
				return errors.Errorf(ctx, "decision-park record %s exited %d", pending.SessionID, code)
			}
			return nil
		default:
			return errors.Errorf(ctx, "unknown pending-record kind %q", pending.Kind)
		}
	}
	return func(ctx context.Context) error {
		_, _, err := pkg.DrainPendingRecords(ctx, store, live, record, pkg.DefaultPendingRecordMaxAge, now)
		return err
	}
}

// keepaliveVaultPath resolves a vault NAME — what the page refs carry, because
// the open surface validates a row against the configured vault NAMES — to its
// configured PATH.
//
// ⚠️ The keep-alive scripts (`topic-keepalive.py`, `worker-keepalive.py`) open the
// vault DIRECTORY: each does `os.path.abspath(os.path.expanduser(--vault))`, so a
// bare name resolves against the daemon's own cwd, the directory does not exist,
// and every decision comes back `cannot-tell` — measured 2026-11-24: 364
// `lapsed-topic-manager` and 363 `dead-worker` lines, every one `cannot-tell`, so
// the tick had never acted on a manager or a worker. Passing the name where a
// path is wanted fails silently, which is the whole defect class.
//
// A name that is not configured is returned unchanged rather than dropped: the
// script then reports its own `cannot-tell`, which is visible, where an empty
// argument would be a different failure the caller could not read.
func keepaliveVaultPath(vaults []pkg.Vault, name string) string {
	for _, vault := range vaults {
		if vault.Name == name {
			return vault.Path
		}
	}
	return name
}

// newManagerDecide returns the first topic page's keepalive verdict.
func newManagerDecide(lister *pkg.KeepaliveLister, vaults []pkg.Vault, branch string) func(context.Context) (pkg.LapsedManagerDecision, error) {
	return func(ctx context.Context) (pkg.LapsedManagerDecision, error) {
		refs, err := lister.Select(ctx)
		if err != nil {
			return pkg.LapsedManagerDecision{}, err
		}
		if len(refs) == 0 {
			return pkg.LapsedManagerDecision{Verdict: "leave"}, nil
		}
		ref := refs[0]
		out, _, runErr := keepaliveScript("topic-keepalive.py")(ctx, "--vault", keepaliveVaultPath(vaults, ref.Vault), "--subject", ref.Name, "--branch", branch)
		if runErr != nil {
			return pkg.LapsedManagerDecision{}, runErr
		}
		verdict, parseErr := pkg.ParseTopicKeepaliveVerdict(ctx, out)
		if parseErr != nil {
			return pkg.LapsedManagerDecision{}, parseErr
		}
		// ⚠️ ADVANCE, or this leg examines the same page for the life of the
		// daemon. `Select` rotates from a persisted cursor, but `Advance` was
		// never called from anywhere in production: the cursor file did not
		// exist on 2026-11-24, so every tick loaded an EMPTY map and
		// `RotatePages` started at index 0 again — measured 11:23, 11:38 and
		// 11:53 all evaluating the same topic, one manager decision per tick
		// for the whole fleet. Advancing on the evaluated page (whatever its
		// verdict) is what makes the rotation real; a failure here is reported
		// rather than swallowed, because a silently unadvanced cursor is the
		// defect this call exists to remove.
		if advanceErr := lister.Advance(ctx, map[string]string{ref.Vault: ref.Name}); advanceErr != nil {
			return pkg.LapsedManagerDecision{}, advanceErr
		}
		return pkg.LapsedManagerDecision{Verdict: verdict, SessionID: ref.SessionID, Vault: ref.Vault, Name: ref.Name, Branch: branch}, nil
	}
}

// newTopiclessDecide runs the topicless BLOCK and returns its first actionable row.
func newTopiclessDecide(filtered []pkg.Vault) func(context.Context) (pkg.LapsedManagerDecision, error) {
	return func(ctx context.Context) (pkg.LapsedManagerDecision, error) {
		for _, vault := range filtered {
			out, _, runErr := keepaliveScript("topicless-goal-keepalive.py")(ctx, "--vault", vault.Path)
			if runErr != nil {
				return pkg.LapsedManagerDecision{}, runErr
			}
			block, parseErr := pkg.ParseTopiclessGoalBlock(ctx, out)
			if parseErr != nil {
				return pkg.LapsedManagerDecision{}, parseErr
			}
			if decision, ok := block.FirstActionable(vault.Name); ok {
				return decision, nil
			}
		}
		return pkg.LapsedManagerDecision{Verdict: "leave"}, nil
	}
}

// newWorkerDecisions runs worker-keepalive.py per task page.
func newWorkerDecisions(lister *pkg.KeepaliveLister, vaults []pkg.Vault) func(context.Context) ([]pkg.DeadWorkerDecision, error) {
	return func(ctx context.Context) ([]pkg.DeadWorkerDecision, error) {
		refs, err := lister.Select(ctx)
		if err != nil {
			return nil, err
		}
		decisions := make([]pkg.DeadWorkerDecision, 0, len(refs))
		lastEvaluated := make(map[string]string, len(refs))
		for _, ref := range refs {
			out, _, runErr := keepaliveScript("worker-keepalive.py")(ctx, "--vault", keepaliveVaultPath(vaults, ref.Vault), "--task", ref.Name)
			if runErr != nil {
				return nil, runErr
			}
			verdict, parseErr := pkg.ParseTopicKeepaliveVerdict(ctx, out)
			if parseErr != nil {
				return nil, parseErr
			}
			decisions = append(decisions, pkg.DeadWorkerDecision{Vault: ref.Vault, Task: ref.Name, SessionID: ref.SessionID, Verdict: verdict})
			// The LAST page evaluated for this vault becomes its cursor, so the
			// next tick starts after it. Without this the selection restarts at
			// index 0 every tick and any page past the per-vault cap is never
			// examined at all.
			lastEvaluated[ref.Vault] = ref.Name
		}
		if advanceErr := lister.Advance(ctx, lastEvaluated); advanceErr != nil {
			return nil, advanceErr
		}
		return decisions, nil
	}
}

// newDecisionParkDecide runs decision-park-resume.py and returns its first resume row.
func newDecisionParkDecide(filtered []pkg.Vault, base string) func(context.Context) (pkg.LapsedManagerDecision, error) {
	return func(ctx context.Context) (pkg.LapsedManagerDecision, error) {
		for _, vault := range filtered {
			digest := filepath.Join(base, "keepalive-park-"+vault.Name+".digest")
			out, _, runErr := keepaliveScript("decision-park-resume.py")(ctx, "--vault", vault.Name, "--persist-beside", digest)
			if runErr != nil {
				return pkg.LapsedManagerDecision{}, runErr
			}
			rows, parseErr := pkg.ParseDecisionParkRows(ctx, out)
			if parseErr != nil {
				return pkg.LapsedManagerDecision{}, parseErr
			}
			for _, row := range rows {
				if row.Verdict == "resume" {
					row.Vault = vault.Name
					return row, nil
				}
			}
		}
		return pkg.LapsedManagerDecision{Verdict: "leave"}, nil
	}
}

// newDueParkResume opens a due task by name, resolving its vault by first hit
// (the tasks dir resolved per vault via resolve-folders).
func newDueParkResume(filtered []pkg.Vault, act func(context.Context, string, pkg.LapsedManagerDecision) error) func(context.Context, string) error {
	folderFor := pkg.NewKeepaliveFolderFor(keepaliveScript("resolve-folders.py"), "tasks_dir")
	return func(ctx context.Context, task string) error {
		for _, vault := range filtered {
			dir, err := folderFor(ctx, vault)
			if err != nil || dir == "" {
				continue
			}
			if _, statErr := os.Stat(filepath.Join(dir, task+".md")); statErr == nil {
				return act(ctx, "resume", pkg.LapsedManagerDecision{Verdict: "resume", Vault: vault.Name, Name: task})
			}
		}
		return errors.Errorf(ctx, "due-park task %q found in no vault", task)
	}
}

// newParkNudges runs session-activity.py then park-nudge.py (global, once).
func newParkNudges(filtered []pkg.Vault) func(context.Context) ([]pkg.ParkNudge, error) {
	return func(ctx context.Context) ([]pkg.ParkNudge, error) {
		if len(filtered) == 0 {
			return nil, nil
		}
		activity := filepath.Join(filtered[0].Path, ".claude", "scripts", "session-activity.py")
		out, _, runErr := pkg.NewKeepaliveScriptRunner(activity)(ctx, "--json")
		if runErr != nil {
			return nil, runErr
		}
		tmp, tmpErr := os.CreateTemp("", "keepalive-activity-*.json")
		if tmpErr != nil {
			return nil, tmpErr
		}
		defer os.Remove(tmp.Name())
		if _, writeErr := tmp.WriteString(out); writeErr != nil {
			return nil, writeErr
		}
		_ = tmp.Close()
		nudgeOut, _, nudgeErr := keepaliveScript("park-nudge.py")(ctx, "--activity", tmp.Name(), "--json")
		if nudgeErr != nil {
			return nil, nudgeErr
		}
		return pkg.ParseParkNudges(ctx, nudgeOut)
	}
}

// newStoodDownMarker reports the session a stood-down marker names, joined to its
// page's claude_session_id and confirmed live.
func newStoodDownMarker(registry pkg.SessionRegistry, filtered []pkg.Vault) func(context.Context) (string, bool, error) {
	candidates := func(ctx context.Context) ([]pkg.StoodDownCandidate, error) {
		out, _, runErr := keepaliveScript("manager-liveness.py")(ctx, "--stood-down-past", "60")
		if runErr != nil {
			return nil, runErr
		}
		return pkg.ParseStoodDownCandidates(ctx, out)
	}
	sessionFor := func(ctx context.Context, base string) (string, error) {
		for _, vault := range filtered {
			for _, kind := range []string{pkg.OpenKindGoal, pkg.OpenKindTopic} {
				page := pkg.ReadOpenPage(ctx, vault, kind, base)
				if page.Found && page.ReadErr == nil {
					return page.Fields["claude_session_id"], nil
				}
			}
		}
		return "", nil
	}
	live := func(ctx context.Context, sessionID string) (bool, error) {
		entries, err := registry.Live(ctx)
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if entry.SessionID == sessionID {
				return true, nil
			}
		}
		return false, nil
	}
	return pkg.NewStoodDownMarker(candidates, sessionFor, live)
}

// newStalledTabRows builds the stalled-tab row source: the fleet snapshot's rows
// with stall_count >= 2, goal/topic pages excluded by resolved page kind, each
// driven through `stalled-tab-restart.py --dry-run`.
//
// ⚠️ **`--dry-run` is mandatory, not caution** — without it the script acts on any
// row reading `idle` at call time, making the forbidden unattended restart
// reachable through the snapshot-to-call window. ⚠️ **`--allow-busy-lapse` is NEVER
// passed**: the operator ruled (2026-11-12) that it stays their own judgement, so
// the script's answered path is the only call site that carries it.
func newStalledTabRows(
	run pkg.KeepaliveScriptRunner,
	vaults []pkg.Vault,
) func(context.Context) ([]pkg.StalledTab, error) {
	return func(ctx context.Context) ([]pkg.StalledTab, error) {
		sessions, err := readFleetSnapshot(ctx, fleetSnapshotPath())
		if err != nil {
			return nil, err
		}
		rows := make([]pkg.StalledTab, 0, len(sessions))
		for _, sessionID := range sortedKeys(sessions) {
			rows = append(rows, stalledRowsForSession(ctx, run, vaults, sessionID, sessions[sessionID])...)
		}
		return rows, nil
	}
}

// stalledTabMinStallCount is the step's whole population test within one page kind.
const stalledTabMinStallCount = 2

// fleetSnapshotRow is one fleet-snapshot session entry.
type fleetSnapshotRow struct {
	StallCount int    `json:"stall_count"`
	TaskFile   string `json:"task_file"`
}

// fleetSnapshotPath is the snapshot the sweep writes and this leg reads.
func fleetSnapshotPath() string {
	if configured := os.Getenv("FLEET_SNAPSHOT_PATH"); configured != "" {
		return configured
	}
	return filepath.Join(os.Getenv("HOME"), ".claude", "state", "fleet-snapshot.json")
}

// readFleetSnapshot reads the snapshot's sessions map.
func readFleetSnapshot(ctx context.Context, path string) (map[string]fleetSnapshotRow, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read fleet snapshot %s failed", path)
	}
	document := struct {
		Sessions map[string]fleetSnapshotRow `json:"sessions"`
	}{}
	if unmarshalErr := json.Unmarshal(content, &document); unmarshalErr != nil {
		return nil, errors.Wrapf(ctx, unmarshalErr, "parse fleet snapshot %s failed", path)
	}
	return document.Sessions, nil
}

// sortedKeys makes the row order deterministic — a map range would make the
// reported "first row" change between ticks.
func sortedKeys(sessions map[string]fleetSnapshotRow) []string {
	keys := make([]string, 0, len(sessions))
	for key := range sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// isManagerPage reports whether path is a goal or topic page, which is never a
// restart candidate: restart-worker.py refuses busy-target, so a card for one is
// noise about a session nobody would restart.
func isManagerPage(path string) bool {
	switch pageTypeOf(path) {
	case "goal", "topic":
		return true
	}
	switch filepath.Base(filepath.Dir(path)) {
	case "22 Topics", "23 Topics", "23 Goals", "24 Goals":
		return true
	}
	return false
}

// pageTypeOf reads a page's `page_type` frontmatter, or "" when it carries none.
func pageTypeOf(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return ""
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			return ""
		}
		if strings.HasPrefix(line, "page_type:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "page_type:"))
			return strings.ToLower(strings.Trim(value, "\"'"))
		}
	}
	return ""
}

// stalledRowsForSession returns one row per verdict for a single snapshot entry, or
// a single `cannot-tell` row when the script or its output could not be read.
func stalledRowsForSession(
	ctx context.Context,
	run pkg.KeepaliveScriptRunner,
	vaults []pkg.Vault,
	sessionID string,
	row fleetSnapshotRow,
) []pkg.StalledTab {
	if row.StallCount < stalledTabMinStallCount || row.TaskFile == "" {
		return nil
	}
	// ⚠️ The snapshot stores `task_file` as a BARE PAGE NAME with no directory and no
	// `.md` (measured 2026-11-20: 3199 of 3248 rows, and the row carries no path
	// field at all). `filepath.Dir` on it yields "." — which made both manager-page
	// exclusions dead code and passed `--dir .` to the script. The path must be
	// RESOLVED by name across the configured vaults first.
	pagePath, vaultName, found := resolveStalledPagePath(ctx, vaults, row.TaskFile)
	if !found || isManagerPage(pagePath) {
		return nil
	}
	task := strings.TrimSuffix(filepath.Base(pagePath), ".md")
	undecidable := []pkg.StalledTab{{SessionID: sessionID, Task: task, Verdict: "cannot-tell"}}
	out, _, runErr := run(ctx,
		"--vault", vaultName, "--task", task, "--dir", filepath.Dir(pagePath),
		"--session-id", sessionID, "--stall-count", strconv.Itoa(row.StallCount),
		"--dry-run",
	)
	if runErr != nil {
		return undecidable
	}
	parsed, parseErr := pkg.ParseStalledTabRows(ctx, out)
	if parseErr != nil || len(parsed) == 0 {
		return undecidable
	}
	for i := range parsed {
		parsed[i].Task = task
		// The script prints an 8-char id; the snapshot holds the full one.
		parsed[i].SessionID = sessionID
	}
	return parsed
}

// resolveStalledPagePath finds `<name>.md` across the configured vaults' task, goal
// and topic folders and returns the page's full path with its vault name.
//
// The folder resolution goes through the same `resolve-folders.py` seam the listers
// use, so a renamed vault folder is honoured rather than guessed at. A name that
// resolves nowhere returns found=false and the row is skipped — acting on an
// unresolved page would pass `--dir .` again, which is the defect this replaces.
func resolveStalledPagePath(
	ctx context.Context,
	vaults []pkg.Vault,
	name string,
) (path string, vaultName string, found bool) {
	for _, require := range []string{"tasks_dir", "goals_dir", "topics_dir"} {
		folderFor := pkg.NewKeepaliveFolderFor(keepaliveScript("resolve-folders.py"), require)
		for _, vault := range vaults {
			dir, err := folderFor(ctx, vault)
			if err != nil || dir == "" {
				continue
			}
			candidate := filepath.Join(dir, name+".md")
			if _, statErr := os.Stat(candidate); statErr == nil {
				return candidate, vault.Name, true
			}
		}
	}
	return "", "", false
}
