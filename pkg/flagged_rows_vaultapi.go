// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/claude-wrapper/pkg/sweepgate"
)

// kindDirs are the folder names each kind may live under, in probe order.
//
// The vault generations name these differently (`25 Tasks` after the 2026-09-13
// renumbering, `24 Tasks` before it, `tasks` in the older vaults), so the lister
// probes all of them rather than assuming one.
var kindDirs = []struct {
	Kind string
	Dirs []string
}{
	{"task", []string{"25 Tasks", "24 Tasks", "tasks"}},
	{"goal", []string{"24 Goals", "23 Goals", "Goals"}},
	{"topic", []string{"23 Topics", "Topics"}},
}

// ChildLister lists the supervisor's child table.
//
// One method, not the whole `Supervisor`: this source reads children to derive
// liveness and never adopts, answers or spawns, so a narrower dependency is both
// honest about what it touches and cheap to fake.
type ChildLister interface {
	List(ctx context.Context) ([]Session, error)
}

// NativeFlaggedRowsSource answers the eligible flagged rows by reading the vaults
// directly, replacing the exec that ran `flagged-rows.py`.
//
// Every predicate it applies is one this repo already owns: `FlagSet` and
// `Refusals` for eligibility and provenance, `ResolveOwner` and `OwnerLiveness`
// for the owning subject, `ResolveBranch` for the branch, and `LivenessVerdicts`
// for the liveness column — the same authority the `/api/1.0/liveness` endpoint
// serves. That is the point of the port: one implementation of "which rows are
// eligible", so the tick and `/supervisor:open --flagged` cannot disagree.
type NativeFlaggedRowsSource struct {
	Vaults VaultLister
	// Supervisor supplies the child table, one of the two primary liveness
	// sources. It takes the one method this source uses rather than the whole
	// Supervisor surface: the scan reads children and nothing else, and a wider
	// dependency would make every fake carry methods it never exercises.
	Supervisor ChildLister
	// Heartbeats is corroboration only and may be nil: a store outage must not
	// hide a live worker, and it never creates a verdict.
	Heartbeats HeartbeatReader
	Registry   SessionRegistry
	// Parked reports a declared external wait on the row's own last closer. nil
	// means "none declared", which is what a caller with no park store knows.
	Parked func(sessionID string) bool
	// Now supplies the clock the defer check reads. nil means wall-clock, which is
	// what a caller with no injected clock (a test, a one-shot probe) expects.
	Now libtime.CurrentDateTimeGetter
}

// subjectIndex is what one scan of a vault's subject pages yields: the goal →
// topic map, and every subject's recorded manager session.
type subjectIndex struct {
	TopicOf  map[string]string
	Sessions map[string]string
}

func subjectKey(kind, name string) string { return kind + "/" + name }

// Rows scans every configured vault and returns the eligible flagged rows.
func (s NativeFlaggedRowsSource) Rows(ctx context.Context) ([]FlaggedRow, error) {
	children, err := s.Supervisor.List(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list children")
	}
	var heartbeats []HeartbeatRow
	if s.Heartbeats != nil {
		if rows, readErr := s.Heartbeats.Rows(ctx); readErr == nil {
			heartbeats = rows
		}
	}
	// A registry read failure is NOT fatal here: it makes every row's liveness
	// UNKNOWN, which is an answer the branch partition already carries (`unknown`
	// / `degraded`). Failing the whole scan would turn a probe outage into "no
	// rows", the direction that silently drops work.
	verdicts, verdictErr := LivenessVerdicts(ctx, s.Registry, children, heartbeats)
	liveByID, liveByName := indexVerdicts(verdicts)

	vaults, err := s.Vaults.List(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list vaults")
	}
	rows := make([]FlaggedRow, 0)
	for _, vault := range vaults {
		index := scanSubjects(vault.Path)
		vaultRows, scanErr := s.rowsForVault(ctx, vault, index, liveByID, liveByName, verdictErr)
		if scanErr != nil {
			return nil, scanErr
		}
		rows = append(rows, vaultRows...)
	}
	return rows, nil
}

// rowsForVault scans one vault's three subject folders.
func (s NativeFlaggedRowsSource) rowsForVault(
	ctx context.Context,
	vault Vault,
	index subjectIndex,
	liveByID map[string]LivenessState,
	liveByName map[string]bool,
	verdictErr error,
) ([]FlaggedRow, error) {
	rows := make([]FlaggedRow, 0)
	for _, kind := range kindDirs {
		dir := firstExistingDir(vault.Path, kind.Dirs)
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, errors.Wrapf(ctx, err, "read %s", dir)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			row, ok, rowErr := s.rowForFile(ctx, vault, kind.Kind, dir, entry.Name(), index, liveByID, liveByName, verdictErr)
			if rowErr != nil {
				return nil, rowErr
			}
			if ok {
				rows = append(rows, row)
			}
		}
	}
	return rows, nil
}

// rowForFile parses one page and projects it, reporting false when the page is
// not flagged.
func (s NativeFlaggedRowsSource) rowForFile(
	ctx context.Context,
	vault Vault,
	kind, dir, filename string,
	index subjectIndex,
	liveByID map[string]LivenessState,
	liveByName map[string]bool,
	verdictErr error,
) (FlaggedRow, bool, error) {
	path := filepath.Join(dir, filename)
	raw, err := os.ReadFile(path)
	if err != nil {
		return FlaggedRow{}, false, errors.Wrapf(ctx, err, "read %s", path)
	}
	text := string(raw)
	fm, ok := sweepgate.SplitFrontmatter(text)
	if !ok || !FlagSet(fm, kind) {
		return FlaggedRow{}, false, nil
	}
	name := strings.TrimSuffix(filename, ".md")
	task, ok := sweepgate.ParseTask(name, text, modTime(path))
	if !ok {
		return FlaggedRow{}, false, nil
	}
	if !admissible(s.now(), task) {
		return FlaggedRow{}, false, nil
	}
	role := normaliseRole(task.Role)
	live := rowLiveness(task.Session, liveByID, verdictErr)

	ownerCtx := OwnerContext{
		TopicOf: index.TopicOf,
		SubjectLive: func(_, name string) *bool {
			return subjectLive(name, liveByName, verdictErr)
		},
		SubjectLiveness: func(kind, name string) *bool {
			return subjectLiveness(index, kind, name, liveByID, verdictErr)
		},
		FleetLive: fleetLive(liveByName, verdictErr),
	}
	owner := ResolveOwner(kind, name, fm, ownerCtx)
	ownerLive, chainLive := OwnerLiveness(owner, kind, name, ownerCtx)

	branch, branchRefused := ResolveBranch(BranchInput{
		Kind:       kind,
		Role:       role,
		SessionID:  task.Session,
		Live:       live,
		ChainLive:  chainLive,
		BlockRole:  strings.TrimSpace(sweepgate.FMScalar(fm, "role")),
		Owner:      ownerName(owner),
		OwnerLive:  ownerLive,
		RegistryOK: verdictErr == nil,
		Parked:     s.parked(task.Session),
	})
	// Provenance first: it is the per-kind rule and is independent of the branch
	// partition, so a row can carry both tokens.
	refused := append(Refusals(kind, fm), branchRefused...)

	return FlaggedRow{
		Kind:      kind,
		Vault:     vault.Name,
		Name:      name,
		Role:      role,
		Mode:      task.Mode,
		SessionID: task.Session,
		Liveness:  live,
		Branch:    branch,
		BlockedBy: blockedByOf(task.BlockedBy, dir, kind),
		Refused:   refused,
	}, true, nil
}

// scanSubjects reads a vault's goal and topic pages once, building the goal →
// topic map and every subject's recorded manager session.
func scanSubjects(vaultPath string) subjectIndex {
	index := subjectIndex{TopicOf: map[string]string{}, Sessions: map[string]string{}}
	for _, kind := range kindDirs {
		if kind.Kind == "task" {
			continue
		}
		scanSubjectDir(firstExistingDir(vaultPath, kind.Dirs), kind.Kind, &index)
	}
	return index
}

// scanSubjectDir indexes every page in one subject folder. An unreadable folder
// contributes nothing: the goal → topic map is an OPTIMISATION of the ladder, and
// a folder it cannot read degrades the ladder to its next rung rather than
// failing the scan.
func scanSubjectDir(dir, kind string, index *subjectIndex) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		indexSubjectPage(index, kind, dir, entry.Name())
	}
}

// indexSubjectPage records one subject page's manager session, and — for a topic
// — the goals it declares.
func indexSubjectPage(index *subjectIndex, kind, dir, filename string) {
	name := strings.TrimSuffix(filename, ".md")
	path := filepath.Join(dir, filename)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	fm, ok := sweepgate.SplitFrontmatter(string(raw))
	if !ok {
		return
	}
	if sid := strings.Trim(sweepgate.FMScalar(fm, "claude_session_id"), `'"`); sid != "" {
		index.Sessions[subjectKey(kind, name)] = sid
	}
	if kind != "topic" {
		return
	}
	members, err := sweepgate.DeclaredMembers(path)
	if err != nil {
		return
	}
	for _, goal := range members {
		index.TopicOf[goal] = name
	}
}

// indexVerdicts splits the verdicts into the two indexes the projection reads:
// by session id, and the set of names a live manager registered under.
func indexVerdicts(verdicts []LivenessVerdict) (map[string]LivenessState, map[string]bool) {
	byID := make(map[string]LivenessState, len(verdicts))
	byName := make(map[string]bool, len(verdicts))
	for _, v := range verdicts {
		byID[v.SessionID] = v.Liveness
		if v.Name != "" && v.Liveness == LivenessLive {
			byName[v.Name] = true
		}
	}
	return byID, byName
}

// admissible reports whether a parsed page is work the tick may open: only a page
// at status `in_progress` whose `defer_date` is not in the future.
//
// The rule matches `/supervisor:open --flagged`'s predicate for tasks; goals and
// topics carry the same status/due rule. Every other status — `completed`,
// `aborted`, `hold`, `backlog`, `next` — and any future `defer_date` is excluded
// here, before the per-tick row budget is spent. Admitting them is the defect
// measured 2026-11-21, where 50 terminal rows filled the 50-row budget and no due
// row was reached. The task-terminal spawn guard downstream stays as a backstop.
func admissible(now time.Time, task sweepgate.Task) bool {
	if task.Status != "in_progress" {
		return false
	}
	return !sweepgate.IsDeferred(now, task)
}

// now returns the injected clock's time, or wall-clock when none is wired.
func (s NativeFlaggedRowsSource) now() time.Time {
	if s.Now != nil {
		return s.Now.Now().Time()
	}
	return time.Now()
}

// rowLiveness maps a row's own session onto the predicate's vocabulary.
func rowLiveness(sessionID string, liveByID map[string]LivenessState, verdictErr error) string {
	if sessionID == "" {
		return "none"
	}
	if verdictErr != nil {
		return "UNKNOWN"
	}
	if liveByID[sessionID] == LivenessLive {
		return "LIVE"
	}
	return "ABSENT"
}

// subjectLive answers "is a manager for this subject live?" by registry NAME —
// the signal that selects an owner, never the recorded session.
func subjectLive(name string, liveByName map[string]bool, verdictErr error) *bool {
	if verdictErr != nil {
		return nil
	}
	live := liveByName[name]
	return &live
}

// subjectLiveness answers "is the session this subject's page records alive?".
//
// A subject with NO recorded manager session is a proven false — absence is a
// fact, while an unanswerable probe is not, and collapsing the second into false
// is the fold `branch: unknown` exists to reject.
func subjectLiveness(
	index subjectIndex,
	kind, name string,
	liveByID map[string]LivenessState,
	verdictErr error,
) *bool {
	sid := index.Sessions[subjectKey(kind, name)]
	if sid == "" {
		live := false
		return &live
	}
	if verdictErr != nil {
		return nil
	}
	live := liveByID[sid] == LivenessLive
	return &live
}

// fleetLive answers the fleet sentinel's own liveness.
func fleetLive(liveByName map[string]bool, verdictErr error) *bool {
	if verdictErr != nil {
		return nil
	}
	live := liveByName[FleetOwner]
	return &live
}

// normaliseRole maps an unrecognised `role:` onto the fleet default, which is what
// `commands/open.md`'s window table can act on.
func normaliseRole(raw string) string {
	switch trimmed := strings.TrimSpace(raw); trimmed {
	case RoleManager, RoleHuman:
		return trimmed
	}
	return RoleAgent
}

// ownerName is the BranchInput's owner string: the subject's name, or the fleet
// sentinel.
func ownerName(owner Owner) string {
	if owner.IsFleet() {
		return FleetOwner
	}
	return owner.Name
}

// parked reports the declared wait on the row's own last closer.
func (s NativeFlaggedRowsSource) parked(sessionID string) bool {
	if s.Parked == nil || sessionID == "" {
		return false
	}
	return s.Parked(sessionID)
}

// firstExistingDir returns the first of the candidate folder names that exists.
func firstExistingDir(vaultPath string, candidates []string) string {
	for _, name := range candidates {
		dir := filepath.Join(vaultPath, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// blockedByOf drops blockers whose task file reads completed — "cannot verify it
// is done" stays blocked, and only a task's own blockers are resolved.
func blockedByOf(blockers []string, dir, kind string) []string {
	if len(blockers) == 0 {
		return nil
	}
	if kind != "task" {
		return blockers
	}
	out := make([]string, 0, len(blockers))
	for _, name := range blockers {
		if !blockerCompleted(name, dir) {
			out = append(out, name)
		}
	}
	return out
}

// blockerCompleted reports whether the blocker's task file reads `status:
// completed`. False on any doubt.
func blockerCompleted(title, tasksDir string) bool {
	title = blockerName(title)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if !strings.EqualFold(strings.TrimSuffix(entry.Name(), ".md"), title) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
		if err != nil {
			return false
		}
		fm, ok := sweepgate.SplitFrontmatter(string(raw))
		return ok && strings.Trim(sweepgate.FMScalar(fm, "status"), `'"`) == "completed"
	}
	return false
}

// blockerName reduces a blocker entry to the task name it refers to: wikilink
// brackets, an alias or an anchor dropped, and case folded.
func blockerName(raw string) string {
	name := strings.TrimSpace(raw)
	if links := wikilinkNameRe.FindStringSubmatch(name); links != nil {
		name = links[1]
	}
	return strings.ToLower(strings.Trim(name, `'"`))
}

// modTime returns the file's mtime, or the zero time when it cannot be read.
func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
