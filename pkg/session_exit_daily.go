// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// The session-exit daily metric answers "how many sessions died today, and how
// many of them for a reason nobody recorded" without a human assembling a
// pipeline over the store.
//
// It replaces `com.bborbe.session-exit-daily`, which ran
// `session-exit-record.py daily --line --days 1` from launchd at 23:50. The
// store held a jump from 2 unexplained deaths on 2026-10-27 to 37 on
// 2026-10-28 and nobody saw it for four days, because seeing it meant
// remembering to run a shell pipeline over 200+ files. A metric that only
// exists while someone is looking is not a metric.
//
// ⚠️ **Three behaviours are preserved deliberately, and one is widened.** The
// widened half is stated here rather than left to be discovered: the operator's
// hold on the plist (card `a2cd2a3a`) is on the *reading*, and a reading that
// changes silently is not a preserved fallback.
//
//  1. **One line per calendar day, zero-filled.** A day with no deaths prints
//     `ends=0` rather than vanishing, which is the difference between a metric
//     and a report: a missing line then means the job did not run, and nothing
//     else. A window wider than one day would re-print earlier days on every
//     run and make a stopped job indistinguishable from a quiet one.
//
//  2. **Bucketed by UTC, scheduled by local time.** Every record's `ts` is UTC
//     and the bucket is its first ten characters; the fire is at 23:50 local.
//     At 23:50 in Berlin the UTC day has not rolled over, so the row is
//     provisional until it does; the next evening's line is the final one.
//
//  3. **The line's field names are the script's.** `day`, `ends`, `unknown_end`
//     and `unknown_end_pct` carry the same meaning and the same formatting, so a
//     reader keyed on them keeps working. The `job=… action=metric` prefix is
//     added because that is how every job ported into this service is counted,
//     and a line that cannot be attributed to the job that wrote it is not
//     evidence.
//
//  4. ⚠️ **Widened: the daemon's own records now count.** The script read
//     `pane-*.log` only — the records its own trap and observer write — and
//     recognised only its own vocabulary (`exited`, `signalled kill`,
//     `unknown-end`). The daemon writes `session-<sid>.log` with
//     `kind=signalled` and `kind=unknown`, so the metric read **zero** for every
//     child the fleet actually owns. Measured 2026-11-16: the script printed
//     `ends=11 unknown_end=0` while the store held 315 distinct ends and 27
//     unknown ones for that day. A migration that kept the script's population
//     would have moved a blind metric into the service and called it parity.
const (
	// DefaultSessionExitDailyAt is the local time of day the metric fires, as
	// `HH:MM`. It matches the plist's StartCalendarInterval, so the reading lands
	// at the same wall-clock moment as the job it replaces.
	DefaultSessionExitDailyAt = "23:50"
	// DefaultSessionExitDailyPollInterval is how often the job checks whether its
	// time has come. A daily job inside a long-running process is a poll with a
	// calendar gate, not a timer: the gate is what makes the fire survive a
	// restart that lands mid-day.
	DefaultSessionExitDailyPollInterval = time.Minute
	// SessionExitDailyStateFile holds the last local date this job emitted, in the
	// store directory. ⚠️ It is a file rather than a field because a daemon
	// restart must not re-emit the day: the plist's whole premise is one line per
	// day, and an in-memory guard turns every `make install` into a second line
	// for the same day. No reader picks it up — both record families are globbed
	// by `pane-` / `session-` prefix and this file carries neither.
	SessionExitDailyStateFile = "daily-last-fired"
)

// The four buckets a record falls into. `closed-tab` is kept although no writer
// emits it today: dropping it would leave the columns not summing to the total
// the day one appears, and a table that quietly under-reports is worse than one
// carrying a zero column.
const (
	sessionExitBucketExited    = "exited"
	sessionExitBucketSignal    = "signalled"
	sessionExitBucketUnknown   = "unknown-end"
	sessionExitBucketClosedTab = "closed-tab"
)

// SessionExitDailyJob emits one end-kind line per local day.
type SessionExitDailyJob struct {
	// Dir holds the record store. Empty means DefaultSessionExitDir.
	Dir string
	// At is the local time of day to fire, as `HH:MM`. Empty means
	// DefaultSessionExitDailyAt.
	At string
	// Poll is how often the calendar gate is checked. Zero means
	// DefaultSessionExitDailyPollInterval.
	Poll time.Duration
	// Now supplies the clock. Nil means the process clock.
	Now libtime.CurrentDateTimeGetter
	// Logf writes the job's line. Nil means log.Printf.
	Logf func(line string)
}

// Run checks the calendar gate until ctx is done.
func (j *SessionExitDailyJob) Run(ctx context.Context) error {
	interval := j.Poll
	if interval <= 0 {
		interval = DefaultSessionExitDailyPollInterval
	}
	return RunTick(ctx, interval, j.TickAndLog)
}

// TickAndLog runs one check and writes the line only when the metric is due.
//
// ⚠️ A poll that is not due writes nothing, and that conditional is the job: an
// unconditional line would print a reading every minute and destroy the property
// the metric exists for — that a line means the job ran, and a missing line
// means it did not.
func (j *SessionExitDailyJob) TickAndLog(ctx context.Context) {
	line, fired, err := j.Tick(ctx)
	if err != nil {
		j.logf(JobLine(SessionExitJobName, "failed", "error", err.Error()))
		return
	}
	if !fired {
		return
	}
	j.logf(line)
}

// Tick emits the day's metric when the gate is open, and reports whether it did.
func (j *SessionExitDailyJob) Tick(ctx context.Context) (string, bool, error) {
	now := j.now()
	local := now.In(time.Local)
	localDay := local.Format("2006-01-02")
	lastFired := j.lastFired()
	if lastFired == localDay {
		return "", false, nil
	}
	at, err := parseSessionExitClock(ctx, j.at())
	if err != nil {
		return "", false, errors.Wrap(ctx, err, "parse schedule failed")
	}
	// An empty state file is the first run of a freshly installed job, which is
	// the plist's RunAtLoad baseline: it fires once regardless of the hour, so
	// the metric carries a reading from the moment it is wired.
	if lastFired != "" && local.Hour()*60+local.Minute() < at {
		return "", false, nil
	}
	day := now.UTC().Format("2006-01-02")
	ends, unknown, err := j.counts(ctx, day)
	if err != nil {
		return "", false, err
	}
	// The state is written before the line is returned: a line emitted without a
	// recorded date would be re-emitted on the next poll, and a duplicate reading
	// is worse than a missed one — a missed one shows up as a gap.
	if stateErr := j.markFired(localDay); stateErr != nil {
		return "", false, errors.Wrapf(ctx, stateErr, "write %s failed", j.statePath())
	}
	return JobLine(
		SessionExitJobName, "metric",
		"day", day,
		"ends", strconv.Itoa(ends),
		"unknown_end", strconv.Itoa(unknown),
		"unknown_end_pct", sessionExitUnknownPct(ends, unknown),
	), true, nil
}

// counts tallies one UTC day's ends across both record families.
func (j *SessionExitDailyJob) counts(ctx context.Context, day string) (int, int, error) {
	dir := expandedDir(j.dir())
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A store that does not exist yet is a day with no deaths, not a failure:
		// the zero-filled line is the whole point of the metric.
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, errors.Wrapf(ctx, err, "read %s failed", dir)
	}
	seen := make(map[string]struct{}, len(entries))
	ends, unknown := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || !isSessionExitRecord(entry.Name()) {
			continue
		}
		fileEnds, fileUnknown := scanSessionExitFile(filepath.Join(dir, entry.Name()), day, seen)
		ends += fileEnds
		unknown += fileUnknown
	}
	return ends, unknown, nil
}

// scanSessionExitFile counts one record file's ends for `day`.
//
// ⚠️ A file that cannot be read degrades the reading rather than aborting it, the
// same trade the script makes: one unreadable file must not cost the whole
// unattended run its line.
func scanSessionExitFile(path, day string, seen map[string]struct{}) (int, int) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	ends, unknown := 0, 0
	for scanner.Scan() {
		bucket, key, ok := parseSessionExitRecord(scanner.Text(), day)
		if !ok {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		ends++
		if bucket == sessionExitBucketUnknown {
			unknown++
		}
	}
	return ends, unknown
}

// parseSessionExitRecord returns a record line's bucket and dedupe key, and says
// whether the line counts at all.
//
// The dedupe key is the ended process, not the line: the same death is written
// twice when a session carries both a pane record and a daemon record (measured
// 2026-11-16: 24 sessions do), and counting both would inflate every day by the
// overlap. A record with no session id falls back to its pane, which is all the
// pane-keyed writer has.
func parseSessionExitRecord(line, day string) (string, string, bool) {
	if strings.TrimSpace(line) == "" {
		return "", "", false
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 2 || !isSessionExitDay(fields[0], day) {
		return "", "", false
	}
	values := make(map[string]string, len(fields)-1)
	for _, field := range fields[1:] {
		if name, value, ok := strings.Cut(field, "="); ok {
			values[name] = value
		}
	}
	bucket, isEnd := sessionExitEndBucket(values["kind"])
	if !isEnd {
		return "", "", false
	}
	return bucket, sessionExitRecordKey(values), true
}

// sessionExitEndBucket maps a record's `kind=` to the bucket it is counted in,
// and says whether it is an end at all.
//
// ⚠️ **Two vocabularies, one store.** `session-exit-record.py` writes
// `signalled kill` and `unknown-end`; the daemon writes `signalled` and
// `unknown`. Both are ends, and recognising only one spelling is how a reader
// silently reports zero. `spawn` and `heartbeat` are markers, not ends: folding
// them in would make the denominator track the fleet's size instead of its
// deaths.
func sessionExitEndBucket(kind string) (string, bool) {
	switch kind {
	case "exited":
		return sessionExitBucketExited, true
	case "signalled", "signalled kill":
		return sessionExitBucketSignal, true
	case "unknown", "unknown-end":
		return sessionExitBucketUnknown, true
	case "closed-tab":
		return sessionExitBucketClosedTab, true
	}
	return "", false
}

// sessionExitRecordKey is the ended process this line describes.
func sessionExitRecordKey(values map[string]string) string {
	if sid := values["session"]; sid != "" && sid != "-" {
		return "session:" + sid + ":" + values["pid"]
	}
	return "pane:" + values["pane"] + ":" + values["pid"]
}

// isSessionExitDay reports whether a record's timestamp is `day`, and doubles as
// the guard against a torn or hand-edited line being bucketed under a junk key.
func isSessionExitDay(timestamp, day string) bool {
	if len(timestamp) < 10 {
		return false
	}
	stamp := timestamp[:10]
	if _, err := time.Parse("2006-01-02", stamp); err != nil {
		return false
	}
	return stamp == day
}

// isSessionExitRecord reports whether a store entry is a record file. Both
// families live in one directory: the script writes `pane-<id>.log`, the daemon
// writes `session-<sid>.log`.
func isSessionExitRecord(name string) bool {
	if !strings.HasSuffix(name, ".log") {
		return false
	}
	return strings.HasPrefix(name, "pane-") || strings.HasPrefix(name, "session-")
}

// sessionExitUnknownPct is the unexplained share of the day's deaths, to one
// decimal place, and `0.0` on a day with no deaths rather than a division.
func sessionExitUnknownPct(ends, unknown int) string {
	percent := 0.0
	if ends > 0 {
		percent = math.Round(1000*float64(unknown)/float64(ends)) / 10
	}
	return strconv.FormatFloat(percent, 'f', 1, 64)
}

// parseSessionExitClock turns `HH:MM` into minutes past local midnight.
func parseSessionExitClock(ctx context.Context, value string) (int, error) {
	hourText, minuteText, ok := strings.Cut(value, ":")
	if !ok {
		return 0, errors.Errorf(ctx, "schedule must be HH:MM, got %q", value)
	}
	hour, err := strconv.Atoi(hourText)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "parse hour of %q failed", value)
	}
	minute, err := strconv.Atoi(minuteText)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "parse minute of %q failed", value)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, errors.Errorf(ctx, "schedule out of range: %q", value)
	}
	return hour*60 + minute, nil
}

// lastFired is the last local date emitted, or empty when the job has not run.
func (j *SessionExitDailyJob) lastFired() string {
	raw, err := os.ReadFile(j.statePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// markFired records the local date, so a restart cannot re-emit it.
func (j *SessionExitDailyJob) markFired(date string) error {
	path := j.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(date+"\n"), 0o600)
}

func (j *SessionExitDailyJob) statePath() string {
	return filepath.Join(expandedDir(j.dir()), SessionExitDailyStateFile)
}

func (j *SessionExitDailyJob) dir() string {
	if j.Dir == "" {
		return DefaultSessionExitDir
	}
	return j.Dir
}

func (j *SessionExitDailyJob) at() string {
	if j.At == "" {
		return DefaultSessionExitDailyAt
	}
	return j.At
}

func (j *SessionExitDailyJob) now() time.Time {
	if j.Now == nil {
		return time.Now()
	}
	return j.Now.Now().Time()
}

func (j *SessionExitDailyJob) logf(line string) {
	if j.Logf != nil {
		j.Logf(line)
		return
	}
	log.Printf("%s", line)
}
