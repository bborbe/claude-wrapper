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
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
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
	return g.holdOrRecord(ctx, row, declaration, observation, closer)
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
			// ⚠️ The new baseline is recorded on the movement branch, not only on a
			// first sighting. Without it the stored fingerprint stays at the FIRST
			// sighting forever, so a resumed session that repeats its closer wording
			// reopens on every later tick — exactly the churn this gate exists to
			// remove. Measured 2026-11-18 in review: held, open, then open again on
			// an unchanged third tick.
			g.record(ctx, key, row.SessionID, declaration, observation, hash)
			return "", false
		}
		return g.holdRecorded(declaration, observation, baseline)
	}
	g.record(ctx, key, row.SessionID, declaration, observation, hash)
	return string(declaration.Class), true
}

// holdRecorded applies the expiry rule to a matching, unmoved baseline.
func (g *parkedGate) holdRecorded(
	declaration ParkedDeclaration,
	observation ParkedObservation,
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
