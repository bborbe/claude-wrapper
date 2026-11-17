// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("SessionExitDailyJob", func() {
	// The gate is local time and the bucket is UTC, so every expected day is
	// derived from the same instant the job reads: a test that hard-codes a date
	// passes only on a host in the right zone.
	at := func(hour, minute int) time.Time {
		return time.Date(2026, 11, 16, hour, minute, 0, 0, time.Local)
	}
	// dayOf is the day the job reports for an instant: the last COMPLETE UTC day.
	// The current UTC day still has hours to run at a 23:50-local fire, and no
	// later run re-reads it, so its tail would be lost for good.
	dayOf := func(instant time.Time) string {
		return instant.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	}
	record := func(day, kind, session, pid string) string {
		return day + "T20:00:00Z\tkind=" + kind + "\tpane=-" +
			"\tsession=" + session + "\tpid=" + pid + "\trc=-\n"
	}
	write := func(dir, name, content string) {
		gomega.Expect(os.WriteFile(
			filepath.Join(dir, name), []byte(content), 0o600,
		)).To(gomega.Succeed())
	}
	// state seeds the last-fired file, so a poll can be other than the first run.
	state := func(dir, date string) {
		write(dir, SessionExitDailyStateFile, date+"\n")
	}
	newJob := func(dir string, now time.Time) *SessionExitDailyJob {
		return &SessionExitDailyJob{
			Dir:  dir,
			Now:  fixedNow(now),
			Logf: func(string) {},
		}
	}
	metric := func(day, ends, unknown, pct string) string {
		return "job=session-exit-daily action=metric day=" + day +
			" ends=" + ends + " unknown_end=" + unknown + " unknown_end_pct=" + pct
	}
	// withZone pins time.Local for the duration of fn. The gate reads
	// `now.In(time.Local)` and the bucket reads UTC, so a spec about the split
	// between them must own the zone rather than inherit the host's: on a UTC host
	// the two dates coincide and the premise silently disappears.
	withZone := func(zone *time.Location, fn func()) {
		previous := time.Local
		time.Local = zone
		defer func() { time.Local = previous }()
		fn()
	}
	eastOfUTC := time.FixedZone("test-utc+1", 3600)

	ginkgo.It("writes nothing on a poll that is not due", func() {
		dir := ginkgo.GinkgoT().TempDir()
		state(dir, "2026-11-01")
		line, fired, err := newJob(dir, at(12, 0)).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(fired).To(gomega.BeFalse())
		gomega.Expect(line).To(gomega.BeEmpty())
	})

	ginkgo.It("writes nothing a second time for the same reported day", func() {
		dir := ginkgo.GinkgoT().TempDir()
		state(dir, dayOf(at(23, 59)))
		_, fired, err := newJob(dir, at(23, 59)).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(fired).To(gomega.BeFalse())
	})

	ginkgo.It("fires at the scheduled minute and not one minute earlier", func() {
		early := ginkgo.GinkgoT().TempDir()
		state(early, "2026-11-01")
		_, firedEarly, err := newJob(early, at(23, 49)).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(firedEarly).To(gomega.BeFalse())

		onTime := ginkgo.GinkgoT().TempDir()
		state(onTime, "2026-11-01")
		_, firedOnTime, err := newJob(onTime, at(23, 50)).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(firedOnTime).To(gomega.BeTrue())
	})

	ginkgo.It("fires once the clock passes the scheduled time", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 51)
		state(dir, "2026-11-01")
		line, fired, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(fired).To(gomega.BeTrue())
		gomega.Expect(line).To(gomega.Equal(metric(dayOf(now), "0", "0", "0.0")))
	})

	ginkgo.It("still reports the evening's day when a baseline fired earlier that local day", func() {
		dir := ginkgo.GinkgoT().TempDir()
		withZone(eastOfUTC, func() {
			clock := libtime.NewCurrentDateTime()
			job := &SessionExitDailyJob{Dir: dir, Now: clock, Logf: func(string) {}}
			// A restart just after local midnight reports the UTC day before
			// yesterday and must not consume the slot of the day the evening fire
			// reports. Pinned east of UTC because that is where the two differ.
			clock.SetNow(libtime.DateTime(
				time.Date(2026, 11, 17, 0, 10, 0, 0, eastOfUTC)))
			_, firstFired, err := job.Tick(ginkgo.GinkgoT().Context())
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(firstFired).To(gomega.BeTrue())

			clock.SetNow(libtime.DateTime(
				time.Date(2026, 11, 17, 23, 50, 0, 0, eastOfUTC)))
			line, secondFired, err := job.Tick(ginkgo.GinkgoT().Context())
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(secondFired).To(gomega.BeTrue())
			gomega.Expect(line).To(gomega.ContainSubstring("day=2026-11-16 "))
		})
	})

	ginkgo.It("fires a baseline on the first run whatever the hour", func() {
		dir := ginkgo.GinkgoT().TempDir()
		_, fired, err := newJob(dir, at(6, 0)).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(fired).To(gomega.BeTrue())
	})

	ginkgo.It("records the day, so a restart cannot re-emit it", func() {
		dir := ginkgo.GinkgoT().TempDir()
		job := newJob(dir, at(23, 55))
		_, firstFired, err := job.Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(firstFired).To(gomega.BeTrue())
		_, secondFired, err := job.Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(secondFired).To(gomega.BeFalse())
	})

	ginkgo.It("reports the last complete UTC day, not the day still running", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		line, _, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		// The current UTC day still has time to run at 23:50 local, and the next
		// fire is a different day, so reporting it would lose its tail silently.
		gomega.Expect(line).To(gomega.ContainSubstring(
			"day=" + now.UTC().AddDate(0, 0, -1).Format("2006-01-02")))
		gomega.Expect(line).To(gomega.Not(gomega.ContainSubstring(
			"day=" + now.UTC().Format("2006-01-02") + " ")))
	})

	ginkgo.It("buckets by UTC, which is a different date from local at local midnight", func() {
		dir := ginkgo.GinkgoT().TempDir()
		withZone(eastOfUTC, func() {
			// 00:30 east of UTC is the previous UTC day, so a local bucket would
			// report 2026-11-16 here and the UTC one reports 2026-11-15. Pinned to a
			// fixed zone so the spec runs on a UTC runner instead of skipping.
			now := time.Date(2026, 11, 17, 0, 30, 0, 0, eastOfUTC)
			line, fired, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(fired).To(gomega.BeTrue())
			gomega.Expect(line).To(gomega.ContainSubstring("day=2026-11-15 "))
		})
	})

	ginkgo.It("zero-fills a day with no records, so a quiet day is not a missing line", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		line, fired, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(fired).To(gomega.BeTrue())
		gomega.Expect(line).To(gomega.Equal(metric(dayOf(now), "0", "0", "0.0")))
	})

	ginkgo.It("counts the daemon's own vocabulary, not only the script's", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		day := dayOf(now)
		// The daemon writes `session-<sid>.log` with `signalled` and `unknown`. A
		// reader that knows only the script's spelling reports zero here, which is
		// how the metric read 0 unexplained deaths against a store holding 27.
		write(dir, "session-sid-a.log", record(day, "unknown", "sid-a", "100"))
		write(dir, "session-sid-b.log", record(day, "signalled", "sid-b", "200"))
		write(dir, "session-sid-c.log", record(day, "exited", "sid-c", "300"))
		line, _, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(line).To(gomega.Equal(metric(day, "3", "1", "33.3")))
	})

	ginkgo.It("counts the script's vocabulary too, so one store keeps both writers", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		day := dayOf(now)
		write(dir, "pane-1.log", record(day, "unknown-end", "sid-a", "100"))
		write(dir, "pane-2.log", record(day, "signalled kill", "sid-b", "200"))
		line, _, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(line).To(gomega.Equal(metric(day, "2", "1", "50.0")))
	})

	ginkgo.It("counts one death once when both families recorded it", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		day := dayOf(now)
		// The real shape: the pane's trap records the shell's pid, the daemon's reap
		// records the child's, so the pids DIFFER for one death. Measured on the
		// store 2026-11-16: 24 sessions in both families, 0 sharing a pid — a
		// session+pid key collapses none of them.
		write(dir, "pane-7.log", record(day, "signalled kill", "sid-a", "100"))
		write(dir, "session-sid-a.log", record(day, "signalled", "sid-a", "999"))
		line, _, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(line).To(gomega.Equal(metric(day, "1", "0", "0.0")))
	})

	ginkgo.It("upgrades a death to unknown when one record for it says so", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		day := dayOf(now)
		// One process recorded twice, once with a signal and once as unexplained.
		// Reporting the flattering half is the bias this metric exists to remove.
		write(dir, "session-sid-a.log",
			record(day, "signalled", "sid-a", "100")+
				record(day, "unknown", "sid-a", "100"),
		)
		line, _, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(line).To(gomega.Equal(metric(day, "1", "1", "100.0")))
	})

	ginkgo.It("counts two ends of one session when the pids differ", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		day := dayOf(now)
		// A respawned session is a new process; two panes are two ends.
		write(dir, "pane-1.log", record(day, "exited", "sid-a", "100"))
		write(dir, "pane-2.log", record(day, "exited", "sid-a", "200"))
		line, _, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(line).To(gomega.Equal(metric(day, "2", "0", "0.0")))
	})

	ginkgo.It("ignores markers and other days", func() {
		dir := ginkgo.GinkgoT().TempDir()
		now := at(23, 55)
		day := dayOf(now)
		earlier := now.AddDate(0, 0, -2).UTC().Format("2006-01-02")
		write(dir, "pane-1.log",
			record(day, "heartbeat", "sid-a", "100")+
				record(day, "spawn", "sid-b", "200")+
				record(earlier, "exited", "sid-c", "300")+
				record(day, "exited", "sid-d", "400"),
		)
		line, _, err := newJob(dir, now).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(line).To(gomega.Equal(metric(day, "1", "0", "0.0")))
	})

	ginkgo.It("treats a store that does not exist yet as a day with no deaths", func() {
		dir := filepath.Join(ginkgo.GinkgoT().TempDir(), "absent")
		line, fired, err := newJob(dir, at(23, 55)).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(fired).To(gomega.BeTrue())
		gomega.Expect(line).To(gomega.ContainSubstring("ends=0 unknown_end=0"))
	})

	ginkgo.It("refuses a schedule it cannot read rather than guessing one", func() {
		dir := ginkgo.GinkgoT().TempDir()
		state(dir, "2026-11-01")
		job := newJob(dir, at(23, 55))
		job.At = "half past"
		_, fired, err := job.Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(fired).To(gomega.BeFalse())
	})

	ginkgo.It("refuses to guess when the state file cannot be read", func() {
		dir := ginkgo.GinkgoT().TempDir()
		// A directory at the state path is a read error that is not IsNotExist.
		// Reading it as "never fired" would re-emit a day already emitted, which is
		// the one outcome the design says is worse than a missed line.
		gomega.Expect(os.Mkdir(
			filepath.Join(dir, SessionExitDailyStateFile), 0o750,
		)).To(gomega.Succeed())
		_, fired, err := newJob(dir, at(23, 55)).Tick(ginkgo.GinkgoT().Context())
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(fired).To(gomega.BeFalse())
	})
})
