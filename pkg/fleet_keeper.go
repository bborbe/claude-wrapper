// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// The fleet keeper REPORTS a lapsed fleet round to the operator; it acts only on
// their answer.
//
// ⚠️ **Report-only, by operator decision (2026-11-01, card efc771fb).** An
// earlier version killed and resumed the Fleet Manager on a stale digest. On
// 2026-11-01 it killed the manager twice without being able to resume it, then
// SIGTERMed a healthy manager it had resumed six minutes earlier. The trigger
// cannot tell a dead loop from a slow round: across 10 measured rounds, healthy
// arm→digest time was median 18 min, p90 38 min and max 51 min, while the
// keepalive reads `stale` past 18 min — roughly half of all healthy rounds cross
// the trigger. So the keeper holds **no kill and no re-arm path**, and starts
// nothing on its own judgement. Its only route to a spawn is an answer-gated
// resume, which lives in the supervisor's card handling, not here.
//
// That ruling is why this job's whole surface is: check, and if a lapse is owed,
// raise exactly one card and log its id.
const (
	// DefaultFleetKeeperInterval matches the shell tick's cadence.
	DefaultFleetKeeperInterval = 5 * time.Minute
	// FleetKeeperJobName is the name this job writes in its `job=` lines.
	FleetKeeperJobName = "fleet-keeper"
	// fleetLapseOwedExitCode is what the keepalive returns when a lapse is owed.
	fleetLapseOwedExitCode = 10
)

// FleetCheck runs the keepalive and reports its output and exit code.
//
// The predicate stays in `fleet-keeper.py` for this iteration: the 18/38/51 min
// settle measurement above is encoded there, and a second implementation of
// "how stale is too stale" would drift from the one the operator's ruling was
// made against.
type FleetCheck func(ctx context.Context) (output string, exitCode int, err error)

// CardRaiser raises one attention card and returns its id.
//
// The id is what the job logs: a card that was raised but whose id was not
// recorded cannot be joined to the tick that raised it, which is exactly the
// evidence a 24 h observation needs.
type CardRaiser func(ctx context.Context, message string) (cardID string, err error)

// FleetKeeperJob runs the fleet keeper inside the daemon.
type FleetKeeperJob struct {
	Check FleetCheck
	Raise CardRaiser
	// Interval is how often the keepalive runs. Zero means
	// DefaultFleetKeeperInterval.
	Interval time.Duration
	// Logf writes the job's lines. Nil means log.Printf.
	Logf func(line string)
}

// Run ticks until ctx is done.
func (j *FleetKeeperJob) Run(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultFleetKeeperInterval
	}
	return RunTick(ctx, interval, j.TickAndLog)
}

// TickAndLog runs one poll and writes its line.
func (j *FleetKeeperJob) TickAndLog(ctx context.Context) {
	cardID, err := j.Tick(ctx)
	if err != nil {
		j.logf(JobLine(FleetKeeperJobName, "failed", "error", err.Error()))
		return
	}
	if cardID == "" {
		j.logf(JobLine(FleetKeeperJobName, "tick", "lapse", "none"))
		return
	}
	j.logf(JobLine(FleetKeeperJobName, "card-raised", "card", cardID))
}

// Tick runs one poll and returns the card id it raised, or "" when nothing was
// owed.
func (j *FleetKeeperJob) Tick(ctx context.Context) (string, error) {
	output, code, checkErr := j.check(ctx)
	if checkErr != nil {
		return "", errors.Wrap(ctx, checkErr, "fleet keepalive failed")
	}
	if code != fleetLapseOwedExitCode {
		return "", nil
	}
	if j.Raise == nil {
		return "", errors.New(ctx, "card raiser is not configured")
	}
	cardID, raiseErr := j.Raise(ctx, fleetLapseMessage(output))
	if raiseErr != nil {
		return "", errors.Wrap(ctx, raiseErr, "raise fleet lapse card failed")
	}
	if cardID == "" {
		// A card with no id is a card nobody can poll; refusing to log success is
		// the only way this stays checkable.
		return "", errors.New(ctx, "card raiser returned no id")
	}
	return cardID, nil
}

// fleetLapseMessage names the lapsed rounds, capped so one bad poll cannot
// produce an unbounded payload.
func fleetLapseMessage(output string) string {
	trimmed := strings.TrimSpace(output)
	if len(trimmed) > 500 {
		trimmed = trimmed[:500]
	}
	if trimmed == "" {
		return "Fleet round lapsed: keepalive reported stale with no detail"
	}
	return "Fleet round lapsed: " + trimmed
}

func (j *FleetKeeperJob) check(ctx context.Context) (string, int, error) {
	if j.Check == nil {
		return "", 0, errors.New(ctx, "fleet keepalive check is not configured")
	}
	return j.Check(ctx)
}

func (j *FleetKeeperJob) logf(line string) {
	if j.Logf != nil {
		j.Logf(line)
		return
	}
	log.Printf("%s", line)
}
