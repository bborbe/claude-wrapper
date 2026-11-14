// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("StateLedgerJob", func() {
	age := func(seconds int) *int { return &seconds }
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

	ginkgo.Describe("buildLines", func() {
		// step runs one poll and installs the returned state, which is what Tick
		// does after a successful append.
		step := func(job *StateLedgerJob, sessions []Session, at time.Time) []stateLedgerLine {
			lines, next := job.buildLines(sessions, at)
			job.seen = next
			return lines
		}

		ginkgo.It("writes a transition and a heartbeat on first sight of a session", func() {
			job := &StateLedgerJob{}
			lines := step(job, []Session{
				{ID: "s1", State: StateIdle, Role: "agent", LastEventAgeSeconds: age(60)},
			}, at)
			gomega.Expect(lines).To(gomega.HaveLen(2))
			gomega.Expect(lines[0].Kind).To(gomega.Equal("transition"))
			gomega.Expect(lines[0].Since).To(gomega.Equal("2026-11-13T11:59:00Z"))
			gomega.Expect(lines[1].Kind).To(gomega.Equal("heartbeat"))
			gomega.Expect(lines[1].LastSeen).To(gomega.Equal("2026-11-13T12:00:00Z"))
		})

		ginkgo.It("writes no second transition while the state holds", func() {
			job := &StateLedgerJob{}
			sessions := []Session{{ID: "s1", State: StateIdle}}
			gomega.Expect(step(job, sessions, at)).To(gomega.HaveLen(2))
			second := step(job, sessions, at.Add(time.Minute))
			gomega.Expect(second).To(gomega.HaveLen(1))
			gomega.Expect(second[0].Kind).To(gomega.Equal("heartbeat"))
		})

		ginkgo.It("writes a transition when the state changes", func() {
			job := &StateLedgerJob{}
			step(job, []Session{{ID: "s1", State: StateIdle}}, at)
			lines := step(job, []Session{{ID: "s1", State: StateGenerating}}, at)
			gomega.Expect(lines[0].Kind).To(gomega.Equal("transition"))
			gomega.Expect(lines[0].State).To(gomega.Equal("generating"))
		})

		ginkgo.It("skips a session that has emitted no state yet", func() {
			job := &StateLedgerJob{}
			lines, _ := job.buildLines([]Session{{ID: "s1"}}, at)
			gomega.Expect(lines).To(gomega.BeEmpty())
		})

		ginkgo.It("forgets a session that is gone, so a reused id transitions again", func() {
			job := &StateLedgerJob{}
			step(job, []Session{{ID: "s1", State: StateIdle}}, at)
			step(job, []Session{}, at)
			lines := step(job, []Session{{ID: "s1", State: StateIdle}}, at)
			gomega.Expect(lines[0].Kind).To(gomega.Equal("transition"))
		})

		ginkgo.It("does not advance the recorded state when the write fails", func() {
			// A failed append must leave `seen` untouched, or the transition is lost
			// for good and the file disagrees with the process from then on.
			job := &StateLedgerJob{seen: map[string]string{"s1": string(StateIdle)}}
			_, next := job.buildLines([]Session{{ID: "s1", State: StateGenerating}}, at)
			gomega.Expect(job.seen["s1"]).To(gomega.Equal(string(StateIdle)))
			gomega.Expect(next["s1"]).To(gomega.Equal(string(StateGenerating)))
		})
	})

	ginkgo.Describe("appendLines", func() {
		ginkgo.It("appends parseable jsonl to the daily file", func() {
			dir := ginkgo.GinkgoT().TempDir()
			job := &StateLedgerJob{Dir: dir}
			lines, _ := job.buildLines([]Session{{ID: "s1", State: StateIdle}}, at)
			gomega.Expect(job.appendLines(context.Background(), lines, "2026-11-13")).To(gomega.Succeed())

			file, err := os.Open(filepath.Join(dir, "events-2026-11-13.jsonl"))
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			defer func() { _ = file.Close() }()

			scanner := bufio.NewScanner(file)
			var read []stateLedgerLine
			for scanner.Scan() {
				var line stateLedgerLine
				gomega.Expect(json.Unmarshal(scanner.Bytes(), &line)).To(gomega.Succeed())
				read = append(read, line)
			}
			gomega.Expect(read).To(gomega.HaveLen(len(lines)))
			gomega.Expect(read[0].SID).To(gomega.Equal("s1"))
		})
	})
})
