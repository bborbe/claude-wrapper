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
//  2. **Bucketed by UTC, fired at 23:50 local — and the row is the last
//     COMPLETE UTC day.** Every record's `ts` is UTC and the bucket is its first
//     ten characters. At 23:50 in Berlin the current UTC day still has ~70 min
//     (winter) to run, and the gate marks the local day fired, so the next fire
//     is a different UTC day and the tail is never counted: measured on the real
//     store, 31 of 125 ends lost on 2026-11-08 (24.8%) and 17 of 88 on
//     2026-11-09 (19.3%), silently, because the line looks like a whole day. The
//     row therefore reports yesterday (UTC), which is what the script's own
//     docstring meant by "the next evening's line is the final one" — it just
//     delivered it under today's date instead of re-reading it.
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
	// SessionExitDailyStateFile holds the last UTC day this job reported, in the
	// store directory. ⚠️ It is a file rather than a field because a daemon
	// restart must not re-emit the day: the plist's whole premise is one line per
	// day, and an in-memory guard turns every `make install` into a second line
	// for the same day. It records the REPORTED day rather than the fire's local
	// date, because a baseline fire before the schedule reports the previous UTC
	// day and a local-date key would then suppress the evening fire that reports
	// the same one. No reader picks it up — both record families are globbed by
	// `pane-` / `session-` prefix and this file carries neither.
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
	// The state keys on the day being REPORTED, not on the local day of the fire.
	// Keying on the local day loses a day whenever the baseline fires before the
	// schedule: a restart at 00:10 local reports the UTC day before yesterday,
	// marks that local day fired, and the evening fire that would have reported
	// yesterday is then suppressed — so yesterday is never reported at all.
	day := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	lastFired, err := j.lastFired(ctx)
	if err != nil {
		return "", false, err
	}
	if lastFired == day {
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
	ends, unknown, err := j.counts(ctx, day)
	if err != nil {
		return "", false, err
	}
	// The state is written before the line is returned: a line emitted without a
	// recorded date would be re-emitted on the next poll, and a duplicate reading
	// is worse than a missed one — a missed one shows up as a gap.
	if stateErr := j.markFired(ctx, day); stateErr != nil {
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
//
// ⚠️ **The two families overlap, and the overlap is not a pid match.** A tab
// session is recorded twice: the pane's `EXIT` trap writes `pane-*.log` with the
// shell's pid, and the daemon's reap writes `session-*.log` with the child's —
// different pids for one death. Measured 2026-11-16 on the real store: 24
// sessions appear in both families and **0** share a pid, so a `session:pid` key
// collapses none of them. The pane record is the better evidence (it carries the
// wait status and the signal), so a session that has a pane record on the day
// has its daemon record dropped rather than added.
//
// ⚠️ **Residual undercount, deliberate.** The drop is session-wide for the day,
// so a session that dies twice — one death the trap saw, the second recorded
// only by the daemon because the trap missed it — counts once. Keying the drop
// on the pane-recorded pid instead would keep that second death, but the two
// writers disagree about the pid for the *same* death (that is the whole reason
// the key is not a pid), so a pid-scoped rule would re-count the overlap it
// exists to remove. One certain double count against a rare missed death is the
// trade; the alternative is a metric that is wrong every night.
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
	tally := &sessionExitTally{
		ends:         make(map[string]bool, len(entries)),
		paneSessions: make(map[string]struct{}, len(entries)),
	}
	// Two passes, pane family first — see sessionExitFamily.
	for _, family := range []string{"pane", "session"} {
		for _, entry := range entries {
			if entry.IsDir() || sessionExitFamily(entry.Name()) != family {
				continue
			}
			scanSessionExitFile(filepath.Join(dir, entry.Name()), day, tally)
		}
	}
	ends, unknown := tally.counts()
	return ends, unknown, nil
}

// sessionExitFamily names the writer a store file belongs to: `pane` for the
// script's trap/observer, `session` for the daemon's reap, empty for anything
// else in the directory.
//
// ⚠️ The two are read in separate passes, never in directory order. The pane
// family has to be seen first because a session it covers must not be counted
// again from its daemon record, and `os.ReadDir`'s sort would only make that
// true by accident of the `p` < `s` prefix.
func sessionExitFamily(name string) string {
	if !strings.HasSuffix(name, ".log") {
		return ""
	}
	switch {
	case strings.HasPrefix(name, "pane-"):
		return "pane"
	case strings.HasPrefix(name, "session-"):
		return "session"
	}
	return ""
}

// sessionExitTally accumulates the day's ends.
//
// `ends` maps a dedupe key to whether any record for it was an unexplained end.
// A collision upgrades to `unknown` rather than keeping whichever line was read
// first: one process recorded twice, once as `signalled` and once as `unknown`,
// is a death whose cause was not established, and reporting the flattering half
// is the bias this metric exists to remove.
type sessionExitTally struct {
	ends map[string]bool
	// paneSessions are the sessions a pane record already accounts for on this
	// day. The daemon's record of the same session is the same death.
	paneSessions map[string]struct{}
}

// counts returns the day's totals once every file has been read.
func (t *sessionExitTally) counts() (int, int) {
	unknown := 0
	for _, isUnknown := range t.ends {
		if isUnknown {
			unknown++
		}
	}
	return len(t.ends), unknown
}

// scanSessionExitFile adds one record file's ends for `day` to the tally.
//
// ⚠️ A file that cannot be read degrades the reading rather than aborting it, the
// same trade the script makes: one unreadable file must not cost the whole
// unattended run its line.
func scanSessionExitFile(path, day string, tally *sessionExitTally) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	paneFamily := strings.HasPrefix(filepath.Base(path), "pane-")
	for scanner.Scan() {
		record, ok := parseSessionExitRecord(scanner.Text(), day)
		if !ok {
			continue
		}
		if paneFamily {
			tally.paneSessions[record.session] = struct{}{}
		} else if _, covered := tally.paneSessions[record.session]; covered && record.session != "" {
			continue
		}
		previous, seen := tally.ends[record.key]
		tally.ends[record.key] = record.isUnknown || (seen && previous)
	}
}

// sessionExitRecord is one parsed end line.
type sessionExitRecord struct {
	// key identifies the ended process: `session:pid` when the record names a
	// session, else `pane:pid`. A session keeps its pid, so a respawned session
	// that dies twice on one day counts twice — the pane writer and the daemon
	// are reconciled by session, not by key.
	key string
	// session is the session id, empty when the record carries none.
	session string
	// isUnknown marks a death whose cause was not established.
	isUnknown bool
}

// parseSessionExitRecord returns a record line's end, and says whether the line
// counts at all.
func parseSessionExitRecord(line, day string) (sessionExitRecord, bool) {
	if strings.TrimSpace(line) == "" {
		return sessionExitRecord{}, false
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 2 || !isSessionExitDay(fields[0], day) {
		return sessionExitRecord{}, false
	}
	values := make(map[string]string, len(fields)-1)
	for _, field := range fields[1:] {
		if name, value, ok := strings.Cut(field, "="); ok {
			values[name] = value
		}
	}
	bucket, isEnd := sessionExitEndBucket(values["kind"])
	if !isEnd {
		return sessionExitRecord{}, false
	}
	session := values["session"]
	if session == "-" {
		session = ""
	}
	return sessionExitRecord{
		key:       sessionExitRecordKey(session, values),
		session:   session,
		isUnknown: bucket == sessionExitBucketUnknown,
	}, true
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
func sessionExitRecordKey(session string, values map[string]string) string {
	if session != "" {
		return "session:" + session + ":" + values["pid"]
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

// lastFired is the last day REPORTED, or empty when the job has not run. The
// value is a UTC calendar day (the one the line carries), not the fire's local
// date — see the state-file constant for why that distinction is load-bearing.
//
// ⚠️ Only a missing file means "never fired". Any other read error is returned,
// because treating it as a first run re-emits a day that was already emitted —
// the one outcome worse than a missed line.
func (j *SessionExitDailyJob) lastFired(ctx context.Context) (string, error) {
	raw, err := os.ReadFile(j.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", errors.Wrapf(ctx, err, "read %s failed", j.statePath())
	}
	return strings.TrimSpace(string(raw)), nil
}

// markFired records the reported day, so a restart cannot re-emit it.
//
// The write is a temp file plus a rename: `os.WriteFile` truncates before it
// writes, so a process crash in between leaves an empty file that reads as
// "never fired" and re-emits the day. ⚠️ This is crash-safety, not durability:
// nothing is fsynced, so a host or power loss can still lose the last write (the
// job then re-reports one day, which the next fire corrects). A temp file left
// by such a loss is inert — no reader globs it — and is swept below.
func (j *SessionExitDailyJob) markFired(ctx context.Context, date string) error {
	path := j.statePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create %s failed", dir)
	}
	j.sweepStaleState(dir)
	temp, err := os.CreateTemp(dir, SessionExitDailyStateFile+".*")
	if err != nil {
		return errors.Wrapf(ctx, err, "create state temp in %s failed", dir)
	}
	name := temp.Name()
	if _, writeErr := temp.WriteString(date + "\n"); writeErr != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return errors.Wrapf(ctx, writeErr, "write state temp %s failed", name)
	}
	if closeErr := temp.Close(); closeErr != nil {
		_ = os.Remove(name)
		return errors.Wrapf(ctx, closeErr, "close state temp %s failed", name)
	}
	if renameErr := os.Rename(name, path); renameErr != nil {
		_ = os.Remove(name)
		return errors.Wrapf(ctx, renameErr, "rename state temp to %s failed", path)
	}
	return nil
}

// sweepStaleState removes temp files a previous crash left beside the state file.
//
// A temp is only ever written here, so one older than this run is debris. The
// sweep is best-effort: failing to clean up must never cost the day's line.
func (j *SessionExitDailyJob) sweepStaleState(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), SessionExitDailyStateFile+".") {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
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
