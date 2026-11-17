// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// parkedProbeTimeout bounds one probe that shells out or reads the store.
const parkedProbeTimeout = 10 * time.Second

// parkedDialTimeout bounds one TCP dial of a declared host.
const parkedDialTimeout = 2 * time.Second

// ParkedObservation is one probe's reading of its observable.
type ParkedObservation struct {
	// Fingerprint identifies the observable's current state. The gate records
	// it and re-probes on a later tick; a changed fingerprint means the
	// observable moved.
	Fingerprint string
	// Satisfied reports that the observable the closer waits on has happened,
	// independent of any recorded fingerprint.
	Satisfied bool
}

// ParkedProbe observes one class's observable.
//
// An error means the observable could not be read. The caller holds the row
// on an error and never opens it.
type ParkedProbe interface {
	Probe(
		ctx context.Context,
		row FlaggedRow,
		declaration ParkedDeclaration,
	) (ParkedObservation, error)
}

// CardProbe observes whether the row's own session still owns an open
// attention card.
type CardProbe struct {
	Cards OpenCardReader
}

// Probe reads the open-card set and reports whether the row's session is in it.
//
// A nil reader or an empty session id is an error rather than an "answered"
// observation: both mean there is no card to look for, and holding on that
// basis would be a guess. A store read error is wrapped and returned, so the
// caller fails closed.
func (p CardProbe) Probe(
	ctx context.Context,
	row FlaggedRow,
	_ ParkedDeclaration,
) (ParkedObservation, error) {
	if p.Cards == nil {
		return ParkedObservation{}, errors.Errorf(ctx, "attention store is not configured")
	}
	if row.SessionID == "" {
		return ParkedObservation{}, errors.Errorf(ctx, "row has no session id to look for a card")
	}
	ids, err := p.Cards.OpenSessionIDs(ctx)
	if err != nil {
		return ParkedObservation{}, errors.Wrapf(ctx, err, "read open attention cards failed")
	}
	if _, open := ids[row.SessionID]; open {
		return ParkedObservation{Fingerprint: "open"}, nil
	}
	return ParkedObservation{Fingerprint: "answered", Satisfied: true}, nil
}

// PRProbe observes a pull request's head, review decision and state.
type PRProbe struct {
	Runner CommandRunner
}

// Probe asks `gh pr view` for the three fields that make up the observable.
//
// The argv is fixed: the declaration contributes three separate elements and
// never a shell, so no token can become a flag. A runner failure, an
// undecodable body or an expired bound is an error and never a satisfied
// observation.
func (p PRProbe) Probe(
	ctx context.Context,
	_ FlaggedRow,
	declaration ParkedDeclaration,
) (ParkedObservation, error) {
	if p.Runner == nil {
		return ParkedObservation{}, errors.Errorf(ctx, "command runner is not configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, parkedProbeTimeout)
	defer cancel()
	out, err := p.Runner.Run(
		probeCtx,
		"gh", "pr", "view",
		strconv.Itoa(declaration.PR.Number),
		"--repo", declaration.PR.Owner+"/"+declaration.PR.Repo,
		"--json", "headRefOid,reviewDecision,state",
	)
	if err != nil {
		return ParkedObservation{}, errors.Wrapf(
			ctx, err, "read pull request %s/%s#%d failed",
			declaration.PR.Owner, declaration.PR.Repo, declaration.PR.Number,
		)
	}
	if probeErr := probeCtx.Err(); probeErr != nil {
		return ParkedObservation{}, errors.Wrapf(ctx, probeErr, "probe pull request timed out")
	}
	var payload struct {
		HeadRefOID     string `json:"headRefOid"`
		ReviewDecision string `json:"reviewDecision"`
		State          string `json:"state"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return ParkedObservation{}, errors.Wrapf(
			ctx, err, "decode pull request %s/%s#%d failed",
			declaration.PR.Owner, declaration.PR.Repo, declaration.PR.Number,
		)
	}
	return ParkedObservation{
		Fingerprint: payload.HeadRefOID + "|" + payload.ReviewDecision + "|" + payload.State,
		Satisfied:   payload.State == "MERGED" || payload.State == "CLOSED",
	}, nil
}

// HostProbe observes whether a declared host accepts a TCP connection.
type HostProbe struct{}

// Probe dials the declared host once, bounded by parkedDialTimeout.
//
// The dial writes no bytes and a refused or timed-out connection is NOT an
// error: an unreachable host is the declared state, and the gate holds on the
// unsatisfied observation.
func (HostProbe) Probe(
	ctx context.Context,
	_ FlaggedRow,
	declaration ParkedDeclaration,
) (ParkedObservation, error) {
	if declaration.Host == "" {
		return ParkedObservation{}, errors.Errorf(ctx, "host declaration names no host")
	}
	address := net.JoinHostPort(declaration.Host, strconv.Itoa(declaration.Port))
	dialer := &net.Dialer{Timeout: parkedDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return ParkedObservation{Fingerprint: "unreachable"}, nil
	}
	_ = conn.Close()
	return ParkedObservation{Fingerprint: "reachable", Satisfied: true}, nil
}

// ClockProbe observes whether a declared clock has passed.
type ClockProbe struct {
	Now libtime.CurrentDateTimeGetter
}

// Probe compares the declared deadline against the injected clock.
//
// A nil deadline is reported as unprobeable with a nil error: the one-hour
// expiry that applies to such a declaration is the gate's rule, not the
// probe's. The clock is always read from the injected getter.
func (p ClockProbe) Probe(
	ctx context.Context,
	_ FlaggedRow,
	declaration ParkedDeclaration,
) (ParkedObservation, error) {
	if p.Now == nil {
		return ParkedObservation{}, errors.Errorf(ctx, "clock is not configured")
	}
	if declaration.Deadline == nil {
		return ParkedObservation{Fingerprint: "unprobeable"}, nil
	}
	now := p.Now.Now().Time()
	return ParkedObservation{
		Fingerprint: declaration.Deadline.Format(time.RFC3339),
		Satisfied:   !now.Before(*declaration.Deadline),
	}, nil
}
