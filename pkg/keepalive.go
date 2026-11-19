// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"time"

	"github.com/bborbe/errors"
)

// The keep-alive tick runs the eight procedures the Fleet Manager's row 5b used
// to run inside a model round, so the fleet stays alive while the manager is
// busy, dead or out of context.
//
// ⚠️ **The line is not the evidence.** A tick that prints
// `job=keepalive action=<procedure> result=none` eight times satisfies the line
// shape and nothing else. The effect word is the contract: a procedure that had
// work to do reports `resumed`, `restarted`, `closed`, `capped` or `skipped`
// with the session id it acted on, and `none` means it found nothing to do —
// never that it did nothing about something it found.
const (
	// DefaultKeepaliveInterval matches the fleet round's cadence. It is the same
	// 15 minutes the model round ran on, so a ported procedure sees the same
	// population it saw before.
	DefaultKeepaliveInterval = 15 * time.Minute
	// KeepaliveJobName is the name this job writes in its `job=` lines.
	KeepaliveJobName = "keepalive"
)

// KeepaliveResult is the effect one procedure reports for one tick.
//
// The words are a closed set: a probe counts them per procedure, and a
// free-form word would make "did anything happen" unanswerable from the log.
type KeepaliveResult string

const (
	// KeepaliveResultResumed means a session that was not running now is.
	KeepaliveResultResumed KeepaliveResult = "resumed"
	// KeepaliveResultRestarted means a lapsed session was restarted in place.
	KeepaliveResultRestarted KeepaliveResult = "restarted"
	// KeepaliveResultClosed means a finished session was reaped.
	KeepaliveResultClosed KeepaliveResult = "closed"
	// KeepaliveResultNudged means a watcher was sent a nudge message. It is not
	// `resumed`: a nudge asks a session to act, and the session may not.
	KeepaliveResultNudged KeepaliveResult = "nudged"
	// KeepaliveResultKept means a candidate was examined and deliberately left
	// alone — a live session, a held one, a `STOPPED` one.
	KeepaliveResultKept KeepaliveResult = "kept"
	// KeepaliveResultSkipped means the procedure declined for a named reason it
	// carries in Reason.
	KeepaliveResultSkipped KeepaliveResult = "skipped"
	// KeepaliveResultCapped means the 3-resumes-in-30-minutes cap refused the act.
	KeepaliveResultCapped KeepaliveResult = "capped"
	// KeepaliveResultNone means the procedure found nothing to act on this tick.
	KeepaliveResultNone KeepaliveResult = "none"
	// KeepaliveResultError means the procedure could not decide. It is never
	// folded into `none`: a procedure that crashed is not a quiet fleet.
	KeepaliveResultError KeepaliveResult = "error"
)

// KeepaliveEffects lists every effect word a procedure may report. A word
// outside this set is a defect in the procedure, and the job logs it as an
// error rather than passing it through as a quiet tick.
var KeepaliveEffects = []KeepaliveResult{
	KeepaliveResultResumed,
	KeepaliveResultRestarted,
	KeepaliveResultClosed,
	KeepaliveResultNudged,
	KeepaliveResultKept,
	KeepaliveResultSkipped,
	KeepaliveResultCapped,
	KeepaliveResultNone,
	KeepaliveResultError,
}

// Known reports whether the effect is one the job understands.
func (r KeepaliveResult) Known() bool {
	for _, candidate := range KeepaliveEffects {
		if candidate == r {
			return true
		}
	}
	return false
}

// requiresSessionID reports whether an effect word names a session it changed.
//
// The ACT words do: a `resumed`, `restarted`, `nudged` or `closed` line with no
// id cannot be joined to the session that came back. The REPORT and REFUSAL words
// do not: a wake miss may be a subject with no manager at all, and a `capped` or
// `skipped` may name a subject rather than a session.
func requiresSessionID(result KeepaliveResult) bool {
	switch result {
	case KeepaliveResultResumed, KeepaliveResultRestarted, KeepaliveResultNudged, KeepaliveResultClosed:
		return true
	default:
		return false
	}
}

// KeepaliveEffect is what one procedure did in one tick.
//
// SessionID is the session the act was about. It is required for every effect
// except `none`, because a `resumed` line without an id cannot be joined to the
// session that came back — which is the only evidence the acceptance criterion
// reads.
type KeepaliveEffect struct {
	// Result is the effect word.
	Result KeepaliveResult
	// SessionID is the session the act was about, when there was one.
	SessionID string
	// Reason carries the refusal or the mechanism — `hold`, `capped`, `dead-pid`.
	Reason string
}

// KeepaliveProcedure is one of the eight keep-alive acts.
//
// Each implementation reads ONE population, decides with the same rule the
// Python leg used, and performs its own act. Splitting them this way is what
// lets a single procedure be tested in isolation: the guards in the task's
// Success Criteria each belong to exactly one procedure.
type KeepaliveProcedure interface {
	// Name is the `action=` value: `lapsed-topic-manager`,
	// `topicless-goal-manager`, `due-park-watch`, `decision-parked-worker`,
	// `park-nudge`, `dead-worker`, `stood-down-close`, `wake-miss`.
	Name() string
	// Run performs the procedure once and reports what it did.
	Run(ctx context.Context) (KeepaliveEffect, error)
}

// KeepaliveJob runs the procedures as one tick inside the daemon.
type KeepaliveJob struct {
	// Procedures are the eight acts. Order affects only the log's readability:
	// no procedure depends on another's effect in the same tick, which is why a
	// failing one does not stop the rest.
	Procedures []KeepaliveProcedure
	// Interval is how often the tick runs. Zero means DefaultKeepaliveInterval.
	Interval time.Duration
	// Logf writes the job's lines. Nil means log.Printf.
	Logf func(line string)
}

// Run ticks until ctx is done.
func (j *KeepaliveJob) Run(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultKeepaliveInterval
	}
	return RunTick(ctx, interval, j.TickAndLog)
}

// TickAndLog runs every procedure once and writes one line per procedure.
//
// ⚠️ A procedure's error is logged and the tick CONTINUES. The eight acts are
// independent: a lapsed-manager read that fails must not stop a dead worker from
// being resumed, because that would turn one broken store into a fleet-wide
// outage — the exact failure this port exists to remove.
func (j *KeepaliveJob) TickAndLog(ctx context.Context) {
	for _, procedure := range j.Procedures {
		if procedure == nil {
			continue
		}
		j.logf(j.runOne(ctx, procedure))
	}
}

// runOne runs one procedure and renders its line.
func (j *KeepaliveJob) runOne(ctx context.Context, procedure KeepaliveProcedure) string {
	effect, err := procedure.Run(ctx)
	if err != nil {
		return JobLine(KeepaliveJobName, procedure.Name(),
			"result", string(KeepaliveResultError),
			"reason", err.Error(),
		)
	}
	if !effect.Result.Known() {
		return JobLine(KeepaliveJobName, procedure.Name(),
			"result", string(KeepaliveResultError),
			"reason", "unknown result "+string(effect.Result),
		)
	}
	if requiresSessionID(effect.Result) && effect.SessionID == "" {
		// An ACT with no id cannot be joined to the session it changed, so it
		// reads as work and proves nothing — the same defect as a stub. A report
		// (`kept`) or a refusal (`skipped`, `capped`) legitimately has no session:
		// a wake miss may be a subject with no manager at all.
		return JobLine(KeepaliveJobName, procedure.Name(),
			"result", string(KeepaliveResultError),
			"reason", "result "+string(effect.Result)+" without a session id",
		)
	}
	kv := []string{"result", string(effect.Result)}
	if effect.SessionID != "" {
		kv = append(kv, "id", effect.SessionID)
	}
	if effect.Reason != "" {
		kv = append(kv, "reason", effect.Reason)
	}
	return JobLine(KeepaliveJobName, procedure.Name(), kv...)
}

// Tick runs every procedure once and returns the effects, which is the form a
// test and a fault-injection run both need.
func (j *KeepaliveJob) Tick(ctx context.Context) ([]KeepaliveEffect, error) {
	if len(j.Procedures) == 0 {
		return nil, errors.New(ctx, "keepalive has no procedures configured")
	}
	effects := make([]KeepaliveEffect, 0, len(j.Procedures))
	for _, procedure := range j.Procedures {
		if procedure == nil {
			continue
		}
		effect, err := procedure.Run(ctx)
		if err != nil {
			return effects, errors.Wrapf(ctx, err, "keepalive %s failed", procedure.Name())
		}
		effects = append(effects, effect)
	}
	return effects, nil
}

func (j *KeepaliveJob) logf(line string) {
	if j.Logf != nil {
		j.Logf(line)
		return
	}
	log.Printf("%s", line)
}
