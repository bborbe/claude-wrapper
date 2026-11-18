// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("SessionExitFileRecorder", func() {
	code := func(n int) *int { return &n }

	record := func(status ExitStatus) string {
		dir := ginkgo.GinkgoT().TempDir()
		recorder := &SessionExitFileRecorder{Dir: dir}
		recorder.Record(Session{ID: "sid-1"}, 4242, status)
		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-1.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		return string(raw)
	}

	fields := func(line string) []string {
		return strings.Split(strings.TrimSuffix(line, "\n"), "\t")
	}

	ginkgo.It("writes a tab-separated record marked as the wrapper's", func() {
		line := record(ExitStatus{Code: code(0)})
		gomega.Expect(line).To(gomega.HaveSuffix("\n"))
		gomega.Expect(fields(line)).To(gomega.HaveLen(9))
		gomega.Expect(line).To(gomega.ContainSubstring("kind=exited"))
		gomega.Expect(line).To(gomega.ContainSubstring("session=sid-1"))
		gomega.Expect(line).To(gomega.ContainSubstring("pid=4242"))
		gomega.Expect(line).To(gomega.ContainSubstring("rc=0"))
		gomega.Expect(line).To(gomega.ContainSubstring("writer=wrapper"))
		// A headless child has no pane; the field stays present so a tab-splitting
		// reader sees a stable field count.
		gomega.Expect(line).To(gomega.ContainSubstring("pane=-"))
	})

	// SC2 of the startup-deadline task: a child killed before its init event
	// carries where it was waiting, so the next occurrence diagnoses itself from
	// the record instead of a post-mortem.
	ginkgo.It("records the stall point of a child killed before its init event", func() {
		dir := ginkgo.GinkgoT().TempDir()
		recorder := &SessionExitFileRecorder{Dir: dir}
		recorder.Record(Session{
			ID:           "sid-2",
			PendingTurns: 1,
			Stall: &Stall{
				LastEvent:    "system/hook_response",
				PendingHooks: []string{"SessionStart:startup"},
			},
		}, 4343, ExitStatus{Code: code(-1), Signal: syscall.SIGKILL, Sender: "startup-deadline"})
		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-2.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		line := string(raw)
		gomega.Expect(fields(line)).To(gomega.HaveLen(13))
		gomega.Expect(line).To(gomega.ContainSubstring("stall_phase=hook"))
		gomega.Expect(line).To(gomega.ContainSubstring("stall_last_event=system/hook_response"))
		gomega.Expect(line).To(gomega.ContainSubstring("stall_pending_hooks=SessionStart:startup"))
		// A turn was written and never closed: the child was given work and did
		// not start. The phase alone cannot say that, which is why the count is
		// carried into the record.
		gomega.Expect(line).To(gomega.ContainSubstring("stall_pending_turns=1"))
	})

	// An initialised session's line is unchanged. The nine-field record is the
	// contract every existing reader parses, so the stall fields must be absent
	// rather than empty.
	ginkgo.It("omits the stall fields for a session that initialised", func() {
		line := record(ExitStatus{Code: code(0)})
		gomega.Expect(line).NotTo(gomega.ContainSubstring("stall_"))
	})

	// The worst case has no event to hang the point on: a child that emitted
	// nothing at all still reports where it was waiting.
	ginkgo.It("records a child that emitted no event at all", func() {
		dir := ginkgo.GinkgoT().TempDir()
		recorder := &SessionExitFileRecorder{Dir: dir}
		recorder.Record(Session{ID: "sid-3", Stall: &Stall{}}, 4444, ExitStatus{
			Code:   code(-1),
			Signal: syscall.SIGKILL,
			Sender: "startup-deadline",
		})
		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-3.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		line := string(raw)
		gomega.Expect(line).To(gomega.ContainSubstring("stall_phase=no-event"))
		gomega.Expect(line).To(gomega.ContainSubstring("stall_last_event=-"))
		gomega.Expect(line).To(gomega.ContainSubstring("stall_pending_hooks=-"))
		// No turn was ever written, so the record separates this from the
		// given-work-and-never-started case above.
		gomega.Expect(line).To(gomega.ContainSubstring("stall_pending_turns=0"))
	})

	// SC5: the full field set AND ORDER for a signalled record with a named
	// sender. The order is pinned because `session-exit-record.py` already emits
	// `signal`/`sender` in this position, and one reader parses both families.
	ginkgo.It("pins the field order for a signalled record with a named sender", func() {
		line := record(ExitStatus{
			Code:   code(-1),
			Signal: syscall.SIGKILL,
			Sender: IdleReaperJobName,
		})
		gomega.Expect(fields(line)).To(gomega.HaveLen(9))
		gomega.Expect(fields(line)[1]).To(gomega.Equal("kind=signalled"))
		gomega.Expect(fields(line)[2]).To(gomega.Equal("pane=-"))
		gomega.Expect(fields(line)[3]).To(gomega.Equal("session=sid-1"))
		gomega.Expect(fields(line)[4]).To(gomega.Equal("pid=4242"))
		gomega.Expect(fields(line)[5]).To(gomega.Equal("rc=-1"))
		gomega.Expect(fields(line)[6]).To(gomega.Equal("signal=SIGKILL"))
		gomega.Expect(fields(line)[7]).To(gomega.Equal("sender=idle-reaper"))
		gomega.Expect(fields(line)[8]).To(gomega.Equal("writer=wrapper"))
	})

	ginkgo.It("records a negative exit code as a signal", func() {
		line := record(ExitStatus{Code: code(-1), Signal: syscall.SIGTERM})
		gomega.Expect(line).To(gomega.ContainSubstring("kind=signalled"))
		gomega.Expect(line).To(gomega.ContainSubstring("signal=SIGTERM"))
	})

	ginkgo.It("names an external signal's sender as unavailable rather than inventing one", func() {
		line := record(ExitStatus{Code: code(-1), Signal: syscall.SIGKILL})
		gomega.Expect(line).To(gomega.ContainSubstring("signal=SIGKILL"))
		gomega.Expect(line).To(gomega.ContainSubstring("sender=unavailable(external-signal)"))
	})

	ginkgo.It("records an adopted child as unknown rather than inventing a status", func() {
		line := record(ExitStatus{})
		gomega.Expect(line).To(gomega.ContainSubstring("kind=unknown"))
		gomega.Expect(line).To(gomega.ContainSubstring("rc=-"))
		gomega.Expect(line).To(gomega.ContainSubstring("signal=unavailable(no-wait-status)"))
		gomega.Expect(line).To(gomega.ContainSubstring("sender=none"))
	})

	ginkgo.It("appends rather than replacing, so a re-used id keeps both ends", func() {
		dir := ginkgo.GinkgoT().TempDir()
		recorder := &SessionExitFileRecorder{Dir: dir}
		recorder.Record(Session{ID: "sid-2"}, 1, ExitStatus{Code: code(0)})
		recorder.Record(Session{ID: "sid-2"}, 2, ExitStatus{Code: code(1)})
		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-2.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(strings.Count(string(raw), "writer=wrapper")).To(gomega.Equal(2))
	})
})
