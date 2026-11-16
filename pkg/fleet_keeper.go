// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"regexp"
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
	// maxFirstLineRunes bounds the checker's first line inside an error message.
	maxFirstLineRunes = 200
)

// FleetKeeperVerdict is one word the keeper prints on its own FIRST output line
// via `report()` in `fleet-keeper.py`.
//
// ⚠️ The exit code is NOT the contract. The keeper exits `1` for three different
// verdicts — `reported` (a card was raised), `cannot-tell` (it could not decide
// or persist) and `dead` (the fleet manager is ABSENT) — so a handler that maps
// "exit 1" to "card raised" logs two failures as successes. It exits `0` for
// `ok`, `stopped` and `pending`, which are the only quiet outcomes.
//
// `KEEPALIVE_EXIT` inside the script is the INNER `fleet-keepalive.py` probe's
// contract, not the keeper's own exit; conflating the two is the defect this
// split removes.
type FleetKeeperVerdict string

const (
	// FleetKeeperVerdictOK is a fleet round inside its limit — exit 0.
	FleetKeeperVerdictOK FleetKeeperVerdict = "ok"
	// FleetKeeperVerdictStopped is a deliberate /supervisor:stop — exit 0.
	FleetKeeperVerdictStopped FleetKeeperVerdict = "stopped"
	// FleetKeeperVerdictPending is a stale round short of the lapse threshold — exit 0.
	FleetKeeperVerdictPending FleetKeeperVerdict = "pending"
	// FleetKeeperVerdictReported is a lapse whose card was raised, or was already
	// raised this day — exit 1.
	FleetKeeperVerdictReported FleetKeeperVerdict = "reported"
	// FleetKeeperVerdictCannotTell is a keeper that could not decide or persist
	// its state — exit 1.
	FleetKeeperVerdictCannotTell FleetKeeperVerdict = "cannot-tell"
	// FleetKeeperVerdictDead is a fleet manager that is ABSENT — exit 1.
	FleetKeeperVerdictDead FleetKeeperVerdict = "dead"
)

// FleetKeeperVerdicts is a collection of FleetKeeperVerdict values.
type FleetKeeperVerdicts []FleetKeeperVerdict

// AvailableFleetKeeperVerdicts lists every verdict word the keeper can print.
// The handler uses it to tell a known verdict it must not treat as quiet from a
// word it has never seen, which is a script/handler drift rather than a keeper
// failure.
var AvailableFleetKeeperVerdicts = FleetKeeperVerdicts{
	FleetKeeperVerdictOK,
	FleetKeeperVerdictStopped,
	FleetKeeperVerdictPending,
	FleetKeeperVerdictReported,
	FleetKeeperVerdictCannotTell,
	FleetKeeperVerdictDead,
}

// Contains reports whether the collection holds the given verdict.
func (v FleetKeeperVerdicts) Contains(verdict FleetKeeperVerdict) bool {
	for _, candidate := range v {
		if candidate == verdict {
			return true
		}
	}
	return false
}

// String returns the verdict as the keeper printed it.
func (v FleetKeeperVerdict) String() string {
	return string(v)
}

// FleetCheck runs the keepalive and reports its output and exit code.
//
// The predicate stays in `fleet-keeper.py` for this iteration: the 18/38/51 min
// settle measurement above is encoded there, and a second implementation of
// "how stale is too stale" would drift from the one the operator's ruling was
// made against.
type FleetCheck func(ctx context.Context) (output string, exitCode int, err error)

// FleetKeeperJob runs the fleet keeper inside the daemon.
type FleetKeeperJob struct {
	// Check execs the keeper. ⚠️ The SCRIPT raises the card, not this job: it
	// holds the one-card-per-episode record (`lapse.json`) that stops a repeat
	// tick re-posting, and re-raising here would need a second copy of that
	// state. The id comes back in the script's own output as `(card <id>)`.
	Check FleetCheck
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

// Tick runs one poll and returns the card id it saw, or "" when nothing was
// owed.
//
// The keeper's verdict word decides the outcome; the exit code only has to agree
// with it. Reading the code alone is what made every genuine lapse log as a
// handler failure: the keeper exits `1` for `reported`, `cannot-tell` and `dead`
// alike, and never exits `10`.
func (j *FleetKeeperJob) Tick(ctx context.Context) (string, error) {
	output, code, checkErr := j.check(ctx)
	if checkErr != nil {
		return "", errors.Wrap(ctx, checkErr, "fleet keepalive failed")
	}
	verdict := FleetKeeperVerdict(firstLine(output))
	switch verdict {
	case FleetKeeperVerdictOK, FleetKeeperVerdictStopped, FleetKeeperVerdictPending:
		// The only quiet outcomes. A non-zero exit here means the script and this
		// handler disagree, which is a fault in its own right.
		if code != 0 {
			return "", errors.Errorf(ctx, "fleet keeper reported %q but exited %d: %s", verdict, code, keeperDetail(output))
		}
		return "", nil
	case FleetKeeperVerdictReported:
		if code != 1 {
			return "", errors.Errorf(ctx, "fleet keeper reported %q but exited %d: %s", verdict, code, keeperDetail(output))
		}
		cardID := fleetCardID(output)
		if cardID == "" {
			// A lapse the keeper judged owed but could not attribute is a card nobody
			// can poll; refusing to log a bare success is the only way this stays
			// checkable.
			return "", errors.Errorf(ctx, "lapse reported but no card id in the keeper output: %s", keeperDetail(output))
		}
		return cardID, nil
	}
	// None of these may be logged as a quiet tick or as a raised card: a crashed or
	// undecidable keeper must never read as a healthy fleet, and `dead` is the fleet
	// manager being ABSENT. The two classes are kept apart because they need
	// different fixes — a word this handler has never seen is script/handler drift,
	// while `cannot-tell` and `dead` are the keeper doing its job.
	if !AvailableFleetKeeperVerdicts.Contains(verdict) {
		return "", errors.Errorf(ctx, "fleet keeper printed an unrecognised verdict %q (exit %d): %s", verdict, code, keeperDetail(output))
	}
	return "", errors.Errorf(ctx, "fleet keeper exited %d with verdict %q: %s", code, verdict, keeperDetail(output))
}

// fleetCardID pulls the item id out of the keeper's sentence, which ends with
// `(card <id>)` when a card was posted.
func fleetCardID(output string) string {
	match := cardIDPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// cardIDPattern is the keeper's own attribution suffix.
var cardIDPattern = regexp.MustCompile(`\(card ([^)]+)\)`)

// firstLine is the checker's FIRST output line, which for this keeper is the
// verdict word itself — `report()` prints the word alone before its sentence.
// It cuts on a rune boundary, the same way manager_liveness's verdict column
// does — a byte cut can split a multi-byte character.
func firstLine(output string) string {
	trimmed := strings.TrimSpace(output)
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return capRunes(trimmed, maxFirstLineRunes)
}

// keeperDetail is the keeper's explanation — every output line after the verdict
// word, joined and capped. The word says which branch ran; the sentence says
// why, and for `cannot-tell` and `dead` that reason is the only thing that makes
// the error actionable.
func keeperDetail(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return "no explanation on the second line"
	}
	detail := strings.TrimSpace(strings.Join(lines[1:], " "))
	if detail == "" {
		return "no explanation on the second line"
	}
	return capRunes(detail, maxFirstLineRunes)
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
