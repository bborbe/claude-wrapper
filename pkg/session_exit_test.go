// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("SessionExitFileRecorder", func() {
	code := func(n int) *int { return &n }

	record := func(exitCode *int) string {
		dir := ginkgo.GinkgoT().TempDir()
		recorder := &SessionExitFileRecorder{Dir: dir}
		recorder.Record(Session{ID: "sid-1"}, 4242, exitCode)
		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-1.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		return string(raw)
	}

	ginkgo.It("writes a tab-separated record marked as the wrapper's", func() {
		line := record(code(0))
		gomega.Expect(line).To(gomega.HaveSuffix("\n"))
		gomega.Expect(strings.Split(strings.TrimSuffix(line, "\n"), "\t")).To(gomega.HaveLen(7))
		gomega.Expect(line).To(gomega.ContainSubstring("kind=exited"))
		gomega.Expect(line).To(gomega.ContainSubstring("session=sid-1"))
		gomega.Expect(line).To(gomega.ContainSubstring("pid=4242"))
		gomega.Expect(line).To(gomega.ContainSubstring("rc=0"))
		gomega.Expect(line).To(gomega.ContainSubstring("writer=wrapper"))
		// A headless child has no pane; the field stays present so a tab-splitting
		// reader sees a stable field count.
		gomega.Expect(line).To(gomega.ContainSubstring("pane=-"))
	})

	ginkgo.It("records a negative exit code as a signal", func() {
		gomega.Expect(record(code(-1))).To(gomega.ContainSubstring("kind=signalled"))
	})

	ginkgo.It("records an adopted child as unknown rather than inventing a status", func() {
		line := record(nil)
		gomega.Expect(line).To(gomega.ContainSubstring("kind=unknown"))
		gomega.Expect(line).To(gomega.ContainSubstring("rc=-"))
	})

	ginkgo.It("appends rather than replacing, so a re-used id keeps both ends", func() {
		dir := ginkgo.GinkgoT().TempDir()
		recorder := &SessionExitFileRecorder{Dir: dir}
		recorder.Record(Session{ID: "sid-2"}, 1, code(0))
		recorder.Record(Session{ID: "sid-2"}, 2, code(1))
		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-2.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(strings.Count(string(raw), "writer=wrapper")).To(gomega.Equal(2))
	})
})
