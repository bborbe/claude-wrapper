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
		ginkgo.It("writes a transition and a heartbeat on first sight of a session", func() {
			job := &StateLedgerJob{}
			lines := job.buildLines([]Session{
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
			gomega.Expect(job.buildLines(sessions, at)).To(gomega.HaveLen(2))
			second := job.buildLines(sessions, at.Add(time.Minute))
			gomega.Expect(second).To(gomega.HaveLen(1))
			gomega.Expect(second[0].Kind).To(gomega.Equal("heartbeat"))
		})

		ginkgo.It("writes a transition when the state changes", func() {
			job := &StateLedgerJob{}
			job.buildLines([]Session{{ID: "s1", State: StateIdle}}, at)
			lines := job.buildLines([]Session{{ID: "s1", State: StateGenerating}}, at)
			gomega.Expect(lines[0].Kind).To(gomega.Equal("transition"))
			gomega.Expect(lines[0].State).To(gomega.Equal("generating"))
		})

		ginkgo.It("skips a session that has emitted no state yet", func() {
			job := &StateLedgerJob{}
			gomega.Expect(job.buildLines([]Session{{ID: "s1"}}, at)).To(gomega.BeEmpty())
		})

		ginkgo.It("forgets a session that is gone, so a reused id transitions again", func() {
			job := &StateLedgerJob{}
			job.buildLines([]Session{{ID: "s1", State: StateIdle}}, at)
			job.buildLines([]Session{}, at)
			lines := job.buildLines([]Session{{ID: "s1", State: StateIdle}}, at)
			gomega.Expect(lines[0].Kind).To(gomega.Equal("transition"))
		})
	})

	ginkgo.Describe("appendLines", func() {
		ginkgo.It("appends parseable jsonl to the daily file", func() {
			dir := ginkgo.GinkgoT().TempDir()
			job := &StateLedgerJob{Dir: dir}
			lines := job.buildLines([]Session{{ID: "s1", State: StateIdle}}, at)
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
