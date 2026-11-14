// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// The manager-liveness tick reports a lapsed manager loop to the operator, and
// acts only on their answer.
//
// A lapse used to be measured, announced once to a log, then suppressed for
// every minute after until a human looked at a screen. The tick that fixes that
// ran as `com.bborbe.manager-liveness-notify` + `manager-liveness-notify-tick.sh`
// from outside the fleet; it now runs here.
//
// ⚠️ **A failed send is NOT consumed.** The lapse stays pending and is
// re-emitted on the next poll, so a transport that was down at detection time is
// retried without anyone touching it. Only `--delivered` — called here, and only
// after the transport returns success — stops the re-emission. Measured
// 2026-11-07: 43 failed sends sat in this ledger, none retried, because the old
// tick marked the lapse reported at DETECTION time and every later poll was
// silent.
const (
	// DefaultManagerLivenessDir holds the tick's own ledger and heartbeat.
	DefaultManagerLivenessDir = "~/.claude/state/manager-liveness-loop"
	// DefaultManagerLivenessInterval matches the shell tick's cadence.
	DefaultManagerLivenessInterval = 60 * time.Second
	// ManagerLivenessJobName is the name this job writes in its `job=` lines.
	ManagerLivenessJobName = "manager-liveness-notify"
	// lapseOwedExitCode is what the checker returns when a lapse is owed a report.
	lapseOwedExitCode = 10
	// maxVerdictRunes bounds the verdict column of one ledger row.
	maxVerdictRunes = 300
	// maxPublishRunes bounds the escalation message, so one bad poll cannot
	// produce an unbounded payload.
	maxPublishRunes = 1000
)

// LapseCheck runs the checker and reports its output and exit code.
//
// The predicate stays in `manager-liveness.py` for this iteration, the same way
// the flagged tick execs `flagged-rows.py`: re-deriving a 1000-line liveness
// rule in Go would put two implementations of "is this loop dead" in the fleet,
// and they would disagree.
type LapseCheck func(ctx context.Context) (output string, exitCode int, err error)

// LapsePublisher delivers one escalation. A non-nil error means the send FAILED
// and the lapse stays owed.
type LapsePublisher func(ctx context.Context, message string) error

// LapseDelivered marks one slug delivered, which is the only thing that stops
// the checker re-emitting it.
type LapseDelivered func(ctx context.Context, slug string) error

// ManagerLivenessJob runs the manager-liveness tick inside the daemon.
type ManagerLivenessJob struct {
	Check         LapseCheck
	Publish       LapsePublisher
	MarkDelivered LapseDelivered
	// Dir holds `ledger.txt` and `heartbeat`. Empty means
	// DefaultManagerLivenessDir.
	Dir string
	// Interval is how often the check runs. Zero means
	// DefaultManagerLivenessInterval.
	Interval time.Duration
	// Now supplies the clock. Nil means the process clock.
	Now libtime.CurrentDateTimeGetter
	// Logf writes the job's lines. Nil means log.Printf.
	Logf func(line string)
}

// Run ticks until ctx is done.
func (j *ManagerLivenessJob) Run(ctx context.Context) error {
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultManagerLivenessInterval
	}
	return RunTick(ctx, interval, j.TickAndLog)
}

// TickAndLog runs one poll and writes its lines.
func (j *ManagerLivenessJob) TickAndLog(ctx context.Context) {
	slugs, notified, err := j.Tick(ctx)
	if err != nil {
		j.logf(JobLine(ManagerLivenessJobName, "failed", "error", err.Error()))
		return
	}
	j.logf(JobLine(ManagerLivenessJobName, "tick",
		"lapses", strconv.Itoa(len(slugs)),
		"notified", notified,
	))
}

// Tick runs one poll. It returns the lapsed slugs it saw and how delivery went.
func (j *ManagerLivenessJob) Tick(ctx context.Context) ([]string, string, error) {
	output, code, checkErr := j.check(ctx)
	if checkErr != nil {
		return nil, "check-failed", errors.Wrap(ctx, checkErr, "liveness check failed")
	}
	slugs := lapsedSlugs(output)
	notified := "-"
	// 0 is "nothing owed", 10 is "a lapse is owed". Any other code is the checker
	// failing, and recording it as a quiet row would make a crashed checker look
	// like a healthy loop.
	if code != 0 && code != lapseOwedExitCode {
		notified = "check-failed"
		if recordErr := j.record(ctx, code, output, notified); recordErr != nil {
			return slugs, notified, errors.Wrap(ctx, recordErr, "record liveness row failed")
		}
		return slugs, notified, errors.Errorf(ctx, "liveness check exited %d", code)
	}
	if code == lapseOwedExitCode && len(slugs) > 0 {
		notified = j.deliver(ctx, slugs, output)
	}
	if recordErr := j.record(ctx, code, output, notified); recordErr != nil {
		return slugs, notified, errors.Wrap(ctx, recordErr, "record liveness row failed")
	}
	return slugs, notified, nil
}

// deliver reports the lapses and acks them only on a successful send.
//
// ⚠️ The ack is the ONLY thing that stops re-emission, so it must follow the
// transport's success and never precede it. A failed send leaves every slug
// pending, which is what makes the next poll retry it.
func (j *ManagerLivenessJob) deliver(
	ctx context.Context, slugs []string, output string,
) string {
	message := capRunes("Manager loop lapsed: "+strings.Join(lapsedLines(output), ";"), maxPublishRunes)
	if err := j.Publish(ctx, message); err != nil {
		j.logf(JobLine(ManagerLivenessJobName, "send-failed",
			"slugs", strings.Join(slugs, ","), "error", err.Error()))
		return "FAILED: " + err.Error()
	}
	for _, slug := range slugs {
		if err := j.MarkDelivered(ctx, slug); err != nil {
			// A delivered report whose ack failed is still delivered; it will be
			// re-sent once, which is the safe direction.
			j.logf(JobLine(ManagerLivenessJobName, "ack-failed",
				"slug", slug, "error", err.Error()))
		}
	}
	return "sent"
}

// lapsedSlugs pulls the slug from every owed line.
//
// RECOVERED-PENDING counts: the manager came back, but nobody was ever told it
// lapsed, and a recovery does not cancel an unreported lapse.
func lapsedSlugs(output string) []string {
	var slugs []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !isLapseKind(fields[0]) {
			continue
		}
		slugs = append(slugs, fields[1])
	}
	return slugs
}

// lapsedLines is the owed lines joined for the message, capped so one bad poll
// cannot produce an unbounded payload.
func lapsedLines(output string) []string {
	var owed []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 1 || !isLapseKind(fields[0]) {
			continue
		}
		owed = append(owed, strings.TrimSpace(line))
	}
	return owed
}

func isLapseKind(word string) bool {
	return word == "STALE" || word == "INVALID" || word == "RECOVERED-PENDING"
}

// record appends the ledger row and refreshes the heartbeat.
//
// Columns: ts, rc, verdict, notified, source. `source` is the constant `wrapper`
// here rather than a derived parent-process name: the tick's parent is now the
// daemon, and a row claiming `claude-wrapper` is the fact that makes "this fired
// unattended" checkable.
func (j *ManagerLivenessJob) record(
	ctx context.Context, code int, output string, notified string,
) error {
	dir := j.dir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create %s failed", dir)
	}
	now := j.now()
	stamp := now.UTC().Format("2006-01-02T15:04:05Z")
	verdict := verdictField(output)
	row := strings.Join([]string{stamp, strconv.Itoa(code), verdict, notified, "wrapper"}, "\t")
	if err := appendLine(ctx, filepath.Join(dir, "ledger.txt"), row); err != nil {
		return err
	}
	heartbeat := strconv.FormatInt(now.Unix(), 10) + " " + stamp + " rc=" + strconv.Itoa(code)
	return appendLine(ctx, filepath.Join(dir, "heartbeat"), heartbeat)
}

func appendLine(ctx context.Context, path string, line string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrapf(ctx, err, "open %s failed", path)
	}
	defer func() { _ = file.Close() }()
	if _, writeErr := file.WriteString(line + "\n"); writeErr != nil {
		return errors.Wrapf(ctx, writeErr, "write %s failed", path)
	}
	return nil
}

// capRunes truncates on a rune boundary, so a multi-byte character is never cut
// in half.
func capRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

func (j *ManagerLivenessJob) check(ctx context.Context) (string, int, error) {
	if j.Check == nil {
		return "", 0, errors.New(ctx, "liveness check is not configured")
	}
	return j.Check(ctx)
}

// verdictField flattens the checker's output into ONE tab-separated column.
//
// ⚠️ Tabs and newlines are replaced, not just newlines: the row is split on tabs,
// so a tab inside the verdict would shift every later column and make `notified`
// read as part of the verdict. The cut is on a rune boundary — slicing bytes at
// 300 can split a multi-byte character and leave the line invalid UTF-8.
func verdictField(output string) string {
	flat := strings.NewReplacer("\n", ";", "\t", " ", "\r", "").Replace(strings.TrimSpace(output))
	runes := []rune(flat)
	if len(runes) > maxVerdictRunes {
		runes = runes[:maxVerdictRunes]
	}
	return string(runes)
}

func (j *ManagerLivenessJob) dir() string {
	if j.Dir != "" {
		return expandedDir(j.Dir)
	}
	return expandedDir(DefaultManagerLivenessDir)
}

func (j *ManagerLivenessJob) now() time.Time {
	if j.Now == nil {
		return time.Now()
	}
	return j.Now.Now().Time()
}

func (j *ManagerLivenessJob) logf(line string) {
	if j.Logf != nil {
		j.Logf(line)
		return
	}
	log.Printf("%s", line)
}
