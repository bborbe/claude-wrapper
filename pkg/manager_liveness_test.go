// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ManagerLivenessJob", func() {
	ctx := context.Background()

	newJob := func(dir string, code int, output string) (*ManagerLivenessJob, *[]string, *int) {
		delivered := &[]string{}
		publishes := new(int)
		return &ManagerLivenessJob{
			Dir: dir,
			Check: func(context.Context) (string, int, error) {
				return output, code, nil
			},
			Publish: func(context.Context, string) error {
				*publishes++
				return nil
			},
			MarkDelivered: func(_ context.Context, slug string) error {
				*delivered = append(*delivered, slug)
				return nil
			},
		}, delivered, publishes
	}

	ginkgo.It("records a quiet poll and acks nothing", func() {
		dir := ginkgo.GinkgoT().TempDir()
		job, delivered, publishes := newJob(dir, 0, "LIVE fleet")
		slugs, notified, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(slugs).To(gomega.BeEmpty())
		gomega.Expect(notified).To(gomega.Equal("-"))
		gomega.Expect(*publishes).To(gomega.Equal(0))
		gomega.Expect(*delivered).To(gomega.BeEmpty())
		raw, readErr := os.ReadFile(filepath.Join(dir, "ledger.txt"))
		gomega.Expect(readErr).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("\t0\t"))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("\t-\twrapper"))
	})

	ginkgo.It("publishes an owed lapse and acks it only after the send", func() {
		dir := ginkgo.GinkgoT().TempDir()
		job, delivered, publishes := newJob(dir, lapseOwedExitCode, "STALE slug-a crossing=5m")
		slugs, notified, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(slugs).To(gomega.Equal([]string{"slug-a"}))
		gomega.Expect(notified).To(gomega.Equal("sent"))
		gomega.Expect(*publishes).To(gomega.Equal(1))
		gomega.Expect(*delivered).To(gomega.Equal([]string{"slug-a"}))
		raw, _ := os.ReadFile(filepath.Join(dir, "ledger.txt"))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("\tsent\twrapper"))
	})

	ginkgo.It("leaves the lapse owed when the send fails", func() {
		dir := ginkgo.GinkgoT().TempDir()
		job, delivered, _ := newJob(dir, lapseOwedExitCode, "STALE slug-b")
		job.Publish = func(context.Context, string) error {
			return errors.New(ctx, "transport down")
		}
		slugs, notified, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(slugs).To(gomega.Equal([]string{"slug-b"}))
		gomega.Expect(notified).To(gomega.HavePrefix("FAILED:"))
		// The ack is the only thing that stops re-emission, so a failed send must
		// never reach it.
		gomega.Expect(*delivered).To(gomega.BeEmpty())
		raw, _ := os.ReadFile(filepath.Join(dir, "ledger.txt"))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("FAILED:"))
	})

	ginkgo.It("treats a recovered-but-unreported lapse as owed", func() {
		dir := ginkgo.GinkgoT().TempDir()
		job, delivered, _ := newJob(dir, lapseOwedExitCode, "RECOVERED-PENDING slug-c")
		slugs, _, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(slugs).To(gomega.Equal([]string{"slug-c"}))
		gomega.Expect(*delivered).To(gomega.Equal([]string{"slug-c"}))
	})

	ginkgo.It("writes a heartbeat the reader can age", func() {
		dir := ginkgo.GinkgoT().TempDir()
		job, _, _ := newJob(dir, 0, "LIVE")
		_, _, err := job.Tick(ctx)
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		raw, readErr := os.ReadFile(filepath.Join(dir, "heartbeat"))
		gomega.Expect(readErr).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(strings.TrimSpace(string(raw))).To(gomega.HaveSuffix("rc=0"))
	})
})
