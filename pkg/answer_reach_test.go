// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("RecordAnswerReach", func() {
	var (
		ctx     context.Context
		dir     string
		now     time.Time
		reachAt string
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
		gomega.Expect(os.Setenv(answerReachDirEnv, dir)).To(gomega.Succeed())
		now = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
		reachAt = filepath.Join(dir, "sid-1."+AnswerReachLegMCP+".json")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(os.Unsetenv(answerReachDirEnv)).To(gomega.Succeed())
	})

	ginkgo.It("writes the schema the Python probe reads", func() {
		gomega.Expect(RecordAnswerReach(ctx, "sid-1", "mcp-answer-permission", now)).To(gomega.Succeed())

		body, err := os.ReadFile(reachAt)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		var record AnswerReach
		gomega.Expect(json.Unmarshal(body, &record)).To(gomega.Succeed())
		gomega.Expect(record.SessionID).To(gomega.Equal("sid-1"))
		gomega.Expect(record.Leg).To(gomega.Equal("mcp"))
		gomega.Expect(record.Outcome).To(gomega.Equal("mcp-answer-permission"))
		// ⚠️ The timestamp format is load-bearing: `answer-path-check.py` parses it with
		// `datetime.fromisoformat`, and the JS writer emits the same shape.
		gomega.Expect(record.TS).To(gomega.Equal("2026-11-11T09:00:00.000Z"))
	})

	ginkgo.It("writes one file per leg so the two writers cannot erase each other", func() {
		gomega.Expect(RecordAnswerReach(ctx, "sid-1", "mcp-answer-permission", now)).To(gomega.Succeed())

		// The wrapper leg is written by a shell recorder, not here; simulate its file.
		wrapperAt := filepath.Join(dir, "sid-1.wrapper.json")
		gomega.Expect(os.WriteFile(wrapperAt, []byte("{}\n"), 0o644)).To(gomega.Succeed())

		entries, err := os.ReadDir(dir)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(entries).To(gomega.HaveLen(2))
	})

	ginkgo.It("refuses a session id that is not a bare filename", func() {
		for _, bad := range []string{"..", ".", "a/b", `a\b`} {
			gomega.Expect(RecordAnswerReach(ctx, bad, "mcp-answer-permission", now)).NotTo(gomega.Succeed())
		}
		entries, err := os.ReadDir(dir)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(entries).To(gomega.BeEmpty())
	})

	ginkgo.It("writes nothing for an empty session id", func() {
		// The spawn gate already refuses an unnamed session, so a reach keyed on nothing could
		// never be read back — fabricating one would manufacture evidence for a check that has
		// already refused.
		gomega.Expect(RecordAnswerReach(ctx, "", "mcp-answer-permission", now)).To(gomega.Succeed())
		entries, err := os.ReadDir(dir)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(entries).To(gomega.BeEmpty())
	})
})
