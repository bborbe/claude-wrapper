// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"slices"
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

// VaultAPIFlaggedRowsSource answers the eligible flagged rows by asking the local
// vault-api, replacing the scan that read every vault page on every tick.
//
// The eligibility predicate — flagged, open, due — now lives in the index, so the
// tick and every other consumer of "which rows are eligible" cannot disagree, and
// the tick no longer walks three vault directories to answer it.
//
// The enrichment stays here, because it is knowledge only this process has:
// liveness (registry, child table, heartbeats), owner/branch resolution and the
// spawn decision. vault-api answers "which rows are eligible", not "may this row
// open now".
type VaultAPIFlaggedRowsSource struct {
	// Client answers eligibility and the per-record frontmatter.
	Client VaultAPIClient
	// Supervisor supplies the child table, one of the two primary liveness
	// sources. It takes the one method this source uses rather than the whole
	// Supervisor surface.
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

// Rows asks vault-api which rows are eligible and enriches each answer.
//
// A vault-api outage returns ErrVaultAPIUnavailable, never an empty slice. The two
// are opposite answers — "nothing to do" versus "I do not know" — and a tick that
// read an outage as an empty set would silently drop every row the operator
// flagged. The caller decides: skip the tick, or serve the last good set.
func (s VaultAPIFlaggedRowsSource) Rows(ctx context.Context) ([]FlaggedRow, error) {
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

	index, err := s.subjectIndex(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]FlaggedRow, 0)
	for _, kind := range kindDirs {
		eligible, listErr := s.Client.List(ctx, kind.Kind)
		if listErr != nil {
			return nil, listErr
		}
		for _, item := range eligible {
			row, ok, rowErr := s.rowForItem(ctx, kind.Kind, item, index, liveByID, liveByName, verdictErr)
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

// subjectIndex builds the goal → topic map and every subject's recorded manager
// session from the unfiltered goal and topic lists.
//
// An outage is returned rather than swallowed: a half-built index resolves owners
// wrongly and silently, which is worse than a skipped tick.
func (s VaultAPIFlaggedRowsSource) subjectIndex(ctx context.Context) (subjectIndex, error) {
	index := subjectIndex{TopicOf: map[string]string{}, Sessions: map[string]string{}}
	for _, kind := range []string{"goal", "topic"} {
		items, err := s.Client.ListAll(ctx, kind)
		if err != nil {
			return subjectIndex{}, err
		}
		for _, item := range items {
			if item.SessionID != "" {
				index.Sessions[subjectKey(kind, item.Title)] = item.SessionID
			}
			if kind != "topic" {
				continue
			}
			for _, goal := range item.Goals {
				index.TopicOf[blockerName(goal)] = item.Title
			}
		}
	}
	return index, nil
}

// rowForItem projects one eligible row into the tick's vocabulary.
//
// Only a task carries enrichment frontmatter — role, mode, blocked_by,
// flag_set_by, defer_date — and only the task endpoint serves it. A goal or topic
// is its list projection, which is everything the tick reads for those kinds: the
// branch partition resolves their manager chain from the recorded session, not
// from a page.
func (s VaultAPIFlaggedRowsSource) rowForItem(
	ctx context.Context,
	kind string,
	item VaultAPIRow,
	index subjectIndex,
	liveByID map[string]LivenessState,
	liveByName map[string]bool,
	verdictErr error,
) (FlaggedRow, bool, error) {
	var role, mode, flagSetBy, deferDate string
	var blockedBy []string
	if kind == "task" {
		page, err := s.Client.Page(ctx, kind, item.ID)
		if err != nil {
			return FlaggedRow{}, false, err
		}
		role = frontmatterString(page.Frontmatter, "role")
		mode = frontmatterString(page.Frontmatter, "mode")
		flagSetBy = frontmatterString(page.Frontmatter, "flag_set_by")
		deferDate = frontmatterString(page.Frontmatter, "defer_date")
		blockedBy = frontmatterStrings(page.Frontmatter, "blocked_by")
	}

	task := sweepgate.Task{
		Name:      item.Title,
		Status:    item.Status,
		Role:      role,
		Mode:      mode,
		Session:   item.SessionID,
		Goals:     item.Goals,
		BlockedBy: blockedBy,
		DeferDate: deferDate,
	}
	// The index already applied this filter. Re-applying it keeps the rule in one
	// place: a predicate that changed on one side and not the other would otherwise
	// admit a row here that the goal's own criterion says is not eligible.
	if !admissible(s.now(), task) {
		return FlaggedRow{}, false, nil
	}

	normalisedRole := normaliseRole(task.Role)
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
	// The frontmatter block is passed empty: the owner ladder reads only the
	// subject name and the goal→topic map, and the page's raw YAML is no longer
	// available here.
	owner := ResolveOwner(kind, item.Title, "", ownerCtx)
	ownerLive, chainLive := OwnerLiveness(owner, kind, item.Title, ownerCtx)

	branch, branchRefused := ResolveBranch(BranchInput{
		Kind:       kind,
		Role:       normalisedRole,
		SessionID:  task.Session,
		Live:       live,
		ChainLive:  chainLive,
		BlockRole:  role,
		Owner:      ownerName(owner),
		OwnerLive:  ownerLive,
		RegistryOK: verdictErr == nil,
		Parked:     s.parked(task.Session),
	})
	refused := append(refusalsForFlagSetBy(flagSetBy), branchRefused...)

	return FlaggedRow{
		Kind:      kind,
		Vault:     item.Vault,
		Name:      item.Title,
		Role:      normalisedRole,
		Mode:      task.Mode,
		SessionID: task.Session,
		Liveness:  live,
		Branch:    branch,
		BlockedBy: blockedBy,
		Refused:   refused,
	}, true, nil
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
func (s VaultAPIFlaggedRowsSource) now() time.Time {
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

// frontmatterString reads one scalar out of the record endpoint's frontmatter map
// with quotes stripped, the empty string when the key is absent or not a string.
func frontmatterString(frontmatter map[string]any, key string) string {
	if frontmatter == nil {
		return ""
	}
	switch value := frontmatter[key].(type) {
	case string:
		return strings.Trim(value, `"'`)
	case nil:
		return ""
	default:
		return strings.Trim(strings.TrimSpace(fmt.Sprint(value)), `"'`)
	}
}

// frontmatterStrings reads one list out of the frontmatter map. A single scalar is
// accepted as a one-element list, which is how a hand-edited `blocked_by: X` reads.
func frontmatterStrings(frontmatter map[string]any, key string) []string {
	if frontmatter == nil {
		return nil
	}
	switch value := frontmatter[key].(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, entry := range value {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return value
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return []string{value}
	}
	return nil
}

// refusalsForFlagSetBy is the provenance refusal for a row whose flag_set_by the
// record endpoint reported.
//
// It mirrors Refusals' rule without a frontmatter block to parse: the row is
// already known flagged (the eligibility query selected it), so only the
// provenance half remains. An absent value admits nothing, exactly as
// flagSetByOf's empty string does.
func refusalsForFlagSetBy(flagSetBy string) []string {
	if slices.Contains(OpeningActors, strings.TrimSpace(flagSetBy)) {
		return []string{}
	}
	return []string{RefusalAgentSetFlag}
}

// parked reports the declared wait on the row's own last closer.
func (s VaultAPIFlaggedRowsSource) parked(sessionID string) bool {
	if s.Parked == nil || sessionID == "" {
		return false
	}
	return s.Parked(sessionID)
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

