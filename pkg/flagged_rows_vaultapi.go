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

// taskDirsFromKindDirs returns the task folders, in probe order.
//
// The opener (`taskText`) reads this same list, so the row lister and the
// open path cannot disagree about where a task page lives. When they did, a
// row the lister had just read from `tasks/` was refused `missing-row` by the
// opener, which only knew `25 Tasks` and `24 Tasks`.
func taskDirsFromKindDirs() []string {
	for _, kind := range kindDirs {
		if kind.Kind == "task" {
			return kind.Dirs
		}
	}
	return nil
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
	// One unfiltered task read serves every blocker check this tick: the old scan
	// resolved each blocker by reading its file, and a completed blocker must still
	// be dropped or its row stays blocked forever.
	statuses, err := s.taskStatuses(ctx)
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
			row, ok, rowErr := s.rowForItem(
				ctx, kind.Kind, item, index, statuses, liveByID, liveByName, verdictErr,
			)
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
			// Keyed by vault. The scan this replaced was per-vault, so the same
			// subject name in two vaults never collided; a single global map would
			// let one vault's goal resolve the other's manager session, and the
			// collision is silent.
			if item.SessionID != "" {
				index.Sessions[vaultSubjectKey(item.Vault, kind, item.Title)] = item.SessionID
			}
			if kind != "topic" {
				continue
			}
			for _, goal := range item.Goals {
				// Case-preserving: ResolveOwner looks this up with the page's own
				// title, so a folded key would never match.
				index.TopicOf[vaultSubjectKey(item.Vault, "goal", wikilinkTitle(goal))] = item.Title
			}
		}
	}
	return index, nil
}

// vaultSubjectKey namespaces a subject by its vault, so two vaults that both have
// a goal called `Fleet Health` resolve to their own manager.
func vaultSubjectKey(vault, kind, name string) string {
	return vault + "\x00" + kind + "/" + name
}

// topicOfForVault projects the goal → topic map down to one vault's view, which is
// the shape ResolveOwner reads.
func topicOfForVault(index subjectIndex, vault string) map[string]string {
	prefix := vault + "\x00goal/"
	out := make(map[string]string, len(index.TopicOf))
	for key, topic := range index.TopicOf {
		if strings.HasPrefix(key, prefix) {
			out[strings.TrimPrefix(key, prefix)] = topic
		}
	}
	return out
}

// subjectLivenessInVault answers "is the session this subject's page records
// alive?" within one vault. A subject with no recorded manager is a proven false;
// an unanswerable probe is not.
func subjectLivenessInVault(
	index subjectIndex,
	vault, kind, name string,
	liveByID map[string]LivenessState,
	verdictErr error,
) *bool {
	sid := index.Sessions[vaultSubjectKey(vault, kind, name)]
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

// taskStatuses maps a vault-qualified task title to its status, so a blocker that
// has already completed is dropped rather than keeping its row blocked forever.
//
// The old scan resolved this by reading the blocker's file. The index knows the
// same fact, and reading it from the unfiltered task list is exact — a slug guess
// would not be, and a miss there silently keeps a row blocked.
func (s VaultAPIFlaggedRowsSource) taskStatuses(ctx context.Context) (map[string]string, error) {
	items, err := s.Client.ListAll(ctx, "task")
	if err != nil {
		return nil, err
	}
	statuses := make(map[string]string, len(items))
	for _, item := range items {
		statuses[vaultSubjectKey(item.Vault, "task", strings.ToLower(item.Title))] = item.Status
	}
	return statuses, nil
}

// openBlockersOf drops the blockers the index reports as completed. "Cannot verify
// it is done" stays blocked, and only a task's own blockers are resolved — the
// same rule the page-reading version applied.
func openBlockersOf(blockers []string, vault, kind string, statuses map[string]string) []string {
	if len(blockers) == 0 {
		return nil
	}
	if kind != "task" {
		return blockers
	}
	out := make([]string, 0, len(blockers))
	for _, name := range blockers {
		if statuses[vaultSubjectKey(vault, "task", blockerName(name))] == "completed" {
			continue
		}
		out = append(out, name)
	}
	return out
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
	statuses map[string]string,
	liveByID map[string]LivenessState,
	liveByName map[string]bool,
	verdictErr error,
) (FlaggedRow, bool, error) {
	// Every kind now carries the same five enrichment fields on the list
	// projection, so none of them is kind-gated. They were, and that was the
	// defect: a goal deferred to the future opened its manager early, a goal's own
	// role was ignored, and its blockers and provenance were invisible — all
	// because the page-reading source parsed those fields from every page and the
	// index-backed one read them only for tasks.
	// The two projections differ, so each kind is read from the one that carries
	// its fields. `TaskItem` serves neither role, mode, flag_set_by nor blocked_by
	// — a task's record endpoint does — while GoalItem/TopicItem carry all five on
	// the list. Reading the wrong side for either kind is silent: a missing value
	// looks like an absent field, so a task loses its provenance refusal and a goal
	// loses its due date.
	role := item.Role
	mode := item.Mode
	flagSetBy := item.FlagSetBy
	deferDate := item.DeferDate
	blockedBy := item.BlockedBy
	var block string
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
		// The owner ladder's task branch reads managed_by and goals off this block.
		block = ownerBlock(
			frontmatterString(page.Frontmatter, "managed_by"),
			frontmatterStrings(page.Frontmatter, "goals"),
		)
	}
	blockedBy = openBlockersOf(blockedBy, item.Vault, kind, statuses)

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

	branch, branchRefused := s.resolveBranch(
		kind, item, task, block, role, normalisedRole, live,
		index, liveByID, liveByName, verdictErr,
	)
	refused := append(refusalsFor(flagSetBy), branchRefused...)

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

// resolveBranch runs the owner ladder and the branch partition for one row.
//
// It is a method rather than inline code so rowForItem stays inside the length
// budget; the two steps are inseparable in meaning — the branch is chosen from the
// owner the ladder selected and that owner's liveness.
func (s VaultAPIFlaggedRowsSource) resolveBranch(
	kind string,
	item VaultAPIRow,
	task sweepgate.Task,
	block, rawRole, role, live string,
	index subjectIndex,
	liveByID map[string]LivenessState,
	liveByName map[string]bool,
	verdictErr error,
) (string, []string) {
	ownerCtx := OwnerContext{
		TopicOf: topicOfForVault(index, item.Vault),
		SubjectLive: func(_, name string) *bool {
			return subjectLive(name, liveByName, verdictErr)
		},
		SubjectLiveness: func(kind, name string) *bool {
			return subjectLivenessInVault(index, item.Vault, kind, name, liveByID, verdictErr)
		},
		FleetLive: fleetLive(liveByName, verdictErr),
	}
	owner := ResolveOwner(kind, item.Title, block, ownerCtx)
	ownerLive, chainLive := OwnerLiveness(owner, kind, item.Title, ownerCtx)

	return ResolveBranch(BranchInput{
		Kind:       kind,
		Role:       role,
		SessionID:  task.Session,
		Live:       live,
		ChainLive:  chainLive,
		BlockRole:  rawRole,
		Owner:      ownerName(owner),
		OwnerLive:  ownerLive,
		RegistryOK: verdictErr == nil,
		Parked:     s.parked(task.Session),
	})
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
func refusalsFor(flagSetBy string) []string {
	// The index serves flag_set_by for every kind, so an empty value means the
	// page carries no provenance — which admits nothing, exactly as the
	// page-reading version's flagSetByOf("") did. There is no longer an "unknown"
	// case to exempt: that exemption existed only while goals and topics were
	// missing the field.
	if slices.Contains(OpeningActors, strings.TrimSpace(flagSetBy)) {
		return []string{}
	}
	return []string{RefusalAgentSetFlag}
}

// ownerBlock renders the two frontmatter fields ResolveOwner's task branch reads,
// in the YAML shape its line regexes expect.
//
// Passing an empty block dropped both rungs, so a task whose goal manager was live
// resolved to the fleet instead and could be opened a second time.
func ownerBlock(managedBy string, goals []string) string {
	var builder strings.Builder
	if strings.TrimSpace(managedBy) != "" {
		builder.WriteString("managed_by: ")
		builder.WriteString(managedBy)
		builder.WriteString("\n")
	}
	if len(goals) > 0 {
		builder.WriteString("goals:\n")
		for _, goal := range goals {
			builder.WriteString("  - ")
			builder.WriteString(goal)
			builder.WriteString("\n")
		}
	}
	return builder.String()
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
	return strings.ToLower(wikilinkTitle(raw))
}

// wikilinkTitle reduces a wikilink entry to the page title it names, PRESERVING
// case.
//
// It is the shape a map keyed by subject name must use: `blockerName` folds case
// because a blocker lookup is a case-insensitive file match, but a goal→topic key
// is read back with the page's own Title-Case title. Lowercasing the key while the
// reader looks up the raw title made the declaring-topic rung unreachable for every
// Title-Case page, so the owner silently fell through to the fleet.
func wikilinkTitle(raw string) string {
	name := strings.TrimSpace(raw)
	if links := wikilinkNameRe.FindStringSubmatch(name); links != nil {
		name = links[1]
	}
	return strings.Trim(name, `'"`)
}
