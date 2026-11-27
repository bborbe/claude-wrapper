// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sort"

	"github.com/bborbe/errors"
)

// LivenessState is a session's binary liveness verdict.
//
// It is deliberately BINARY: `live` or `not-live`. There is no `unknown` value,
// because an unknown is the defect this type exists to remove — every caller
// that had to interpret one derived its own answer, and the answers disagreed. A
// probe that cannot run is an error (HTTP 500), never a third verdict.
type LivenessState string

const (
	// LivenessLive means a primary source proves the session is running.
	LivenessLive LivenessState = "live"
	// LivenessNotLive means no primary source proves the session is running.
	LivenessNotLive LivenessState = "not-live"
)

// LivenessVerdict is one session's authoritative liveness answer.
//
// Source names WHICH primary source produced the verdict — `registry`,
// `child-table`, or `none` when only a heartbeat row named the session. The
// heartbeat is CORROBORATION only: it is reported alongside a verdict and never
// creates one, so a session present only in the heartbeat store reads not-live.
type LivenessVerdict struct {
	SessionID string        `json:"session_id"`
	Liveness  LivenessState `json:"liveness"`
	Source    string        `json:"source"`
	PID       int           `json:"pid,omitempty"`
	Heartbeat *bool         `json:"heartbeat_live,omitempty"`
	// Name is the tab title the session registered under, when the registry lists
	// it. It is reported so a consumer can match a verdict to a SUBJECT by name
	// without a second read of the registry: `flagged-rows.py` decides "is this
	// subject's manager live?" by name, and before this field it had to shell out
	// to `session-liveness.py --list` for the identity map while taking the
	// verdicts from here — two derivations of the same fact.
	//
	// Empty when the registry does not list the session (a child-table or
	// heartbeat-only verdict), because the name exists only on a registry entry.
	// A consumer must treat an empty name as "no name known", never as a name.
	Name string `json:"name,omitempty"`
}

// LivenessVerdicts derives one verdict per session from the two primary sources,
// with the heartbeat store as corroboration only.
//
// A session is live when the registry lists it with a live pid (source
// `registry`) or its child row has no exit code (source `child-table`). Every
// other session — a child that exited, or one named only by a heartbeat row —
// reads not-live. The heartbeat's own Live flag is copied onto the verdict when
// a row exists and never consulted to decide liveness: a store outage must not
// hide a live worker, and a store that is behind must not resurrect a dead one.
//
// A registry read failure returns the error with no verdicts: an empty or
// partial set would read as "nothing is live", which is the direction that
// silently hides running sessions.
//
// ⚠️ The response is UNBOUNDED over the daemon's lifetime. `Supervisor.List`
// keeps every child it ever spawned — exited records are never removed — so the
// union grows with uptime and carries one not-live row per child that has ever
// run. `/api/1.0/sessions` already behaves this way, so this matches the existing
// surface rather than inventing a second retention rule; a caller that wants only
// live sessions filters on Liveness. Trimming exited rows here would make this
// endpoint disagree with the child table it reports from.
func LivenessVerdicts(
	ctx context.Context,
	registry SessionRegistry,
	children []Session,
	heartbeats []HeartbeatRow,
) ([]LivenessVerdict, error) {
	if registry == nil {
		return nil, errors.Errorf(ctx, "session registry is required")
	}
	live, err := registry.Live(ctx)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read live registry entries failed")
	}

	registryByID := registryEntriesByID(live)
	childrenByID := sessionsByID(children)
	heartbeatByID := heartbeatsByID(heartbeats)

	ids := unionIDs(registryByID, childrenByID, heartbeatByID)
	verdicts := make([]LivenessVerdict, 0, len(ids))
	for id := range ids {
		verdicts = append(verdicts, verdictFor(id, registryByID, childrenByID, heartbeatByID))
	}

	sort.Slice(verdicts, func(i, j int) bool {
		return verdicts[i].SessionID < verdicts[j].SessionID
	})
	return verdicts, nil
}

// verdictFor derives one session's verdict from the three source maps.
//
// The registry is the only source that supplies a pid, and it wins when it lists
// the session. Otherwise a child with no exit code is live; a child that exited
// is not-live; and an id named only by a heartbeat row is not-live with source
// `none` — corroboration never creates a live verdict.
//
// The registry-wins tie-break is DELIBERATE, and it is the one place this
// function can be wrong. If a child crashed and its pid was then reused by an
// unrelated process, a stale registry entry would read `live` while the child row
// records a real exit. It is left this way because the registry file is deleted on
// exit, so the window needs both a crash that skipped that deletion and a pid
// reuse — and preferring the child row instead would make every cleanly exited
// child permanent, since exited records are never removed from the child table.
// The registry is the only source that can say "still running" after this process
// stopped watching the child, which is the case that matters for a resumed or
// adopted session.
func verdictFor(
	id string,
	registryByID map[string]RegistryEntry,
	childrenByID map[string]Session,
	heartbeatByID map[string]HeartbeatRow,
) LivenessVerdict {
	verdict := LivenessVerdict{SessionID: id}
	if entry, ok := registryByID[id]; ok {
		verdict.Liveness = LivenessLive
		verdict.Source = "registry"
		verdict.PID = entry.PID
		verdict.Name = entry.Name
	} else {
		child, ok := childrenByID[id]
		verdict.Liveness, verdict.Source = childVerdict(child, ok)
	}
	if row, ok := heartbeatByID[id]; ok {
		live := row.Live
		verdict.Heartbeat = &live
	}
	return verdict
}

// childVerdict answers a session the registry did not list from its child row.
//
// A child row reads exited when its State is StateExited OR it carries an exit
// code. A missing child row means nothing primary proves the session is
// running, so it reads not-live with source `none`.
//
// ⚠️ ExitCode alone is NOT the question. An adopted child (one this process
// took over after a restart) has no cmd.Wait, so reap settles it with
// StateExited and a nil ExitCode — the watch goroutine refuses to invent a code.
// Keying on ExitCode alone read every adopted child the idle-reaper or the
// startup deadline closed as live forever, and the flagged tick skipped its row
// `not-absent` on every cycle. StateExited is set only by reap, after the pid
// has gone, so it is the settled answer.
func childVerdict(child Session, ok bool) (LivenessState, string) {
	if !ok {
		return LivenessNotLive, "none"
	}
	if child.State == StateExited || child.ExitCode != nil {
		return LivenessNotLive, "child-table"
	}
	return LivenessLive, "child-table"
}

// registryEntriesByID indexes live registry entries by session id, dropping
// empty ids.
func registryEntriesByID(entries []RegistryEntry) map[string]RegistryEntry {
	byID := make(map[string]RegistryEntry, len(entries))
	for _, entry := range entries {
		if entry.SessionID == "" {
			continue
		}
		byID[entry.SessionID] = entry
	}
	return byID
}

// sessionsByID indexes child rows by session id, dropping empty ids.
func sessionsByID(children []Session) map[string]Session {
	byID := make(map[string]Session, len(children))
	for _, child := range children {
		if child.ID == "" {
			continue
		}
		byID[child.ID] = child
	}
	return byID
}

// heartbeatsByID indexes heartbeat rows by session id, dropping empty ids.
func heartbeatsByID(rows []HeartbeatRow) map[string]HeartbeatRow {
	byID := make(map[string]HeartbeatRow, len(rows))
	for _, row := range rows {
		if row.SessionID == "" {
			continue
		}
		byID[row.SessionID] = row
	}
	return byID
}

// unionIDs returns every session id any source names.
func unionIDs(
	registryByID map[string]RegistryEntry,
	childrenByID map[string]Session,
	heartbeatByID map[string]HeartbeatRow,
) map[string]struct{} {
	ids := make(map[string]struct{}, len(registryByID)+len(childrenByID))
	for id := range registryByID {
		ids[id] = struct{}{}
	}
	for id := range childrenByID {
		ids[id] = struct{}{}
	}
	for id := range heartbeatByID {
		ids[id] = struct{}{}
	}
	return ids
}
