// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/claude-wrapper/pkg/sweepgate"
)

// ParkedHold decides whether a row's last session parked on an observable that
// is not yet satisfied.
//
// A nil ParkedHold on the tick means the gate is inactive.
type ParkedHold interface {
	// Hold reports whether the row must be held this tick, and the skip reason
	// when it must. The reason is the class token, optionally followed by
	// ": <cause>" when a probe failed.
	Hold(ctx context.Context, row FlaggedRow) (reason string, held bool)
	// Opened tells the gate that the row's reopen actually succeeded, which is the
	// only moment an advanced baseline may be committed.
	//
	// ⚠️ It is a second call, not part of Hold, because Hold runs BEFORE the caps
	// and rate guards. Recording the advanced fingerprint inside Hold meant a row
	// refused by caps kept the new baseline and was held on every later tick —
	// dark for days on a `parked-on-pr` row, which has no expiry. Measured
	// 2026-11-18 in review.
	Opened(ctx context.Context, row FlaggedRow)
}

// NewParkedGate wires the four probes and the baseline store.
func NewParkedGate(
	sessionsDir string,
	parkedDir string,
	cards OpenCardReader,
	runner CommandRunner,
	now libtime.CurrentDateTimeGetter,
) ParkedHold {
	return &parkedGate{
		SessionsDir: sessionsDir,
		ParkedDir:   parkedDir,
		Store:       NewParkedStore(parkedDir),
		Cards:       CardProbe{Cards: cards},
		PRs:         PRProbe{Runner: runner},
		Hosts:       HostProbe{},
		Clocks:      ClockProbe{Now: now},
		Now:         now,
		Logf:        log.Printf,
		Defer:       VaultCLIDeferWriter{Runner: runner},
	}
}

type parkedGate struct {
	SessionsDir string
	ParkedDir   string
	Store       ParkedStore
	Cards       CardProbe
	PRs         PRProbe
	Hosts       HostProbe
	Clocks      ClockProbe
	Now         libtime.CurrentDateTimeGetter
	Logf        func(format string, args ...any)
	// Defer writes a parked-until deadline back as the row's `defer_date`. A nil
	// Defer disables the write and leaves the hold exactly as it was before it
	// existed, which is the rollback.
	Defer DeferWriter

	// pendingMutex guards pending.
	pendingMutex sync.Mutex
	// pending holds the advanced baseline a movement produced, keyed by row, until
	// the tick confirms the row actually opened. A pending entry that is never
	// confirmed is simply dropped: the baseline keeps its old fingerprint, so the
	// next tick sees the movement again and retries.
	pending map[string]pendingBaseline
}

// pendingBaseline is one movement's uncommitted baseline.
type pendingBaseline struct {
	sessionID   string
	declaration ParkedDeclaration
	observation ParkedObservation
	hash        string
}

// rememberPending stashes a movement's baseline until the reopen is confirmed.
func (g *parkedGate) rememberPending(key string, p pendingBaseline) {
	g.pendingMutex.Lock()
	defer g.pendingMutex.Unlock()
	if g.pending == nil {
		g.pending = map[string]pendingBaseline{}
	}
	g.pending[key] = p
}

// takePending removes and returns the row's pending baseline.
func (g *parkedGate) takePending(key string) (pendingBaseline, bool) {
	g.pendingMutex.Lock()
	defer g.pendingMutex.Unlock()
	p, ok := g.pending[key]
	if ok {
		delete(g.pending, key)
	}
	return p, ok
}

// Opened commits the advanced baseline a movement produced.
//
// A row with no pending baseline — one that was not moved this tick — is a no-op.
func (g *parkedGate) Opened(ctx context.Context, row FlaggedRow) {
	pending, ok := g.takePending(row.Key())
	if !ok {
		return
	}
	g.record(ctx, row.Key(), pending.sessionID, pending.declaration, pending.observation, pending.hash)
}

// Hold answers whether the row is parked on something that has not happened
// yet, running at most one probe.
func (g *parkedGate) Hold(ctx context.Context, row FlaggedRow) (string, bool) {
	if row.SessionID == "" || !uuidPattern.MatchString(row.SessionID) {
		return "", false
	}
	closer := LastCloser(filepath.Join(g.SessionsDir, row.SessionID, sessionEventsName))
	if closer == "" {
		return "", false
	}
	declaration, parked := ClassifyParked(closer)
	if !parked {
		return "", false
	}
	observation, err := g.probe(ctx, row, declaration)
	if err != nil {
		return string(declaration.Class) + ": " + err.Error(), true
	}
	if observation.Satisfied {
		return "", false
	}
	// ⚠️ The write belongs HERE — after the probe says the clock has not passed, and
	// only for `parked-until` with a parsed deadline. A `parked-on-card` or
	// `parked-on-pr` row is released by a probe, not by a clock, so deferring it would
	// park it past the event that would have freed it. A nil deadline is the
	// "cannot address safely" fallback in ClassifyParked and must write nothing.
	if declaration.Class == ParkedUntil && declaration.Deadline != nil &&
		!g.alreadyDeferred(row) {
		g.writeDefer(ctx, row, *declaration.Deadline)
	}
	return g.holdOrRecord(ctx, row, declaration, observation, closer)
}

// alreadyDeferred reports whether the row carries a defer_date that has not expired.
//
// ⚠️ This is a CHECKED invariant, not a restatement of the query. The tick's own
// `DefaultVaultAPITaskQuery` (`due=now`) already excludes a future-dated row, so the
// guard normally never fires — but the write is the one thing here that destroys data
// someone else set, and an unguarded writer would silently re-park a row a human or
// another actor had just scheduled the moment that query loosened or a second source
// fed the gate. An unparseable or absent date is NOT deferred, matching
// `sweepgate.IsDeferred`, so a corrupt value cannot block the write forever.
func (g *parkedGate) alreadyDeferred(row FlaggedRow) bool {
	if row.DeferDate == "" || g.Now == nil {
		return false
	}
	return sweepgate.IsDeferred(g.Now.Now().Time(), sweepgate.Task{DeferDate: row.DeferDate})
}

// writeDefer records the deadline and never changes the hold.
//
// ⚠️ Fail-open on purpose: a missing `vault-cli`, an unwritable vault or a bad row
// name leaves the tick holding the row exactly as it did before this write existed.
// The cost of a failed write is one wasted reopen — the defect this fixes — while a
// hold that errored out would open a row whose wait has not passed.
func (g *parkedGate) writeDefer(ctx context.Context, row FlaggedRow, deadline time.Time) {
	if g.Defer == nil {
		return
	}
	if err := g.Defer.WriteDefer(ctx, row, deadline); err != nil {
		if g.Logf != nil {
			g.Logf("parked defer write failed row=%s: %v", row.Key(), err)
		}
		return
	}
	if g.Logf != nil {
		g.Logf("parked defer written row=%s defer_date=%s", row.Key(), deadline.Format(time.RFC3339))
	}
}

// probe runs the one probe the declaration's class selects.
func (g *parkedGate) probe(
	ctx context.Context,
	row FlaggedRow,
	declaration ParkedDeclaration,
) (ParkedObservation, error) {
	switch declaration.Class {
	case ParkedOnCard:
		return g.Cards.Probe(ctx, row, declaration)
	case ParkedOnPR:
		return g.PRs.Probe(ctx, row, declaration)
	case ParkedOnHost:
		return g.Hosts.Probe(ctx, row, declaration)
	case ParkedUntil:
		return g.Clocks.Probe(ctx, row, declaration)
	default:
		return ParkedObservation{}, errors.Errorf(ctx, "unknown park class %q", declaration.Class)
	}
}

// holdOrRecord compares the observation against the recorded baseline, or
// records a first sighting and holds.
func (g *parkedGate) holdOrRecord(
	ctx context.Context,
	row FlaggedRow,
	declaration ParkedDeclaration,
	observation ParkedObservation,
	closer string,
) (string, bool) {
	key := row.Key()
	hash := sha256hex(closer)
	baseline := g.Store.Load(ctx, key)
	if baseline != nil && baseline.SessionID == row.SessionID && baseline.CloserHash == hash {
		if baseline.Fingerprint != observation.Fingerprint {
			// ⚠️ The advanced baseline is PENDING here, not written. Hold runs before
			// the caps and rate guards, so a row that is then refused must keep its
			// old fingerprint and retry next tick. The write happens in Opened, once
			// the reopen has actually succeeded.
			g.rememberPending(key, pendingBaseline{
				sessionID:   row.SessionID,
				declaration: declaration,
				observation: observation,
				hash:        hash,
			})
			return "", false
		}
		return g.holdRecorded(declaration, baseline)
	}
	g.record(ctx, key, row.SessionID, declaration, observation, hash)
	return string(declaration.Class), true
}

// holdRecorded applies the expiry rule to a matching, unmoved baseline.
func (g *parkedGate) holdRecorded(
	declaration ParkedDeclaration,
	baseline *ParkedBaseline,
) (string, bool) {
	if declaration.Class == ParkedUntil && declaration.Deadline == nil &&
		g.now().Sub(baseline.FirstSeen) >= MAX_AGE_S {
		return "", false
	}
	return string(declaration.Class), true
}

// record writes a first-sighting baseline. A store failure is logged and never
// changes the answer: the next tick simply records again.
func (g *parkedGate) record(
	ctx context.Context,
	key string,
	sessionID string,
	declaration ParkedDeclaration,
	observation ParkedObservation,
	hash string,
) {
	baseline := ParkedBaseline{
		Class:       declaration.Class,
		Fingerprint: observation.Fingerprint,
		FirstSeen:   g.now(),
		SessionID:   sessionID,
		CloserHash:  hash,
	}
	if err := g.Store.Save(ctx, key, baseline); err != nil {
		g.Logf("parked gate: save baseline for %s failed: %v", key, err)
	}
}

// now mirrors FlaggedTick.now().
func (g *parkedGate) now() time.Time {
	if g.Now == nil {
		return time.Now()
	}
	return g.Now.Now().Time()
}

// sha256hex returns the sha256 hex of text.
func sha256hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
