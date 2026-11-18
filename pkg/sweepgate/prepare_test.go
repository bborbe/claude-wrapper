// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"path/filepath"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fixedClock returns the pinned clock the Prepare specs inject.
func fixedClock() libtime.CurrentDateTimeGetter {
	return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		return libtime.DateTime(fixedNow)
	})
}

// prepareConfig builds a config over the checked-in fixture vault and inputs.
func prepareConfig() Config {
	return Config{
		Vault:         fixtureVault(),
		Subject:       "Parity Topic",
		StateDir:      ginkgo.GinkgoT().TempDir(),
		FeedDir:       filepath.Join("..", "..", "testdata", "sweepgate", "inputs", "feed"),
		HoldsPath:     filepath.Join("..", "..", "testdata", "sweepgate", "inputs", "session-holds.json"),
		RegistryDir:   ginkgo.GinkgoT().TempDir(),
		HeartbeatDir:  ginkgo.GinkgoT().TempDir(),
		ToolMarkerDir: ginkgo.GinkgoT().TempDir(),
		TranscriptDir: ginkgo.GinkgoT().TempDir(),
		StoreURL:      "http://127.0.0.1:1",
		Now:           fixedClock(),
		ReadTimeout:   DefaultReadTimeout,
	}
}

var _ = ginkgo.Describe("prepares one subject", func() {
	ctx := context.Background()

	ginkgo.It("runs the pipeline and digests the enriched rows", func() {
		prepared, err := Prepare(ctx, prepareConfig())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prepared.Snapshot.Branch).To(gomega.Equal("topic"))
		gomega.Expect(prepared.Tasks).To(gomega.HaveLen(12))
		gomega.Expect(prepared.Digest).To(gomega.Equal(Digest(prepared.Tasks)))
		gomega.Expect(prepared.ParkBasis).To(gomega.ContainSubstring("fixed 1800s"))
		gomega.Expect(prepared.ParkSeconds).To(gomega.Equal(1800))
		gomega.Expect(prepared.Holds).To(gomega.HaveKey("held-session-0001"))
		gomega.Expect(prepared.Busy).NotTo(gomega.BeNil())
	})

	ginkgo.It("renders the fixture rows through the classification", func() {
		prepared, err := Prepare(ctx, prepareConfig())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		labels := map[string]string{}
		for _, task := range prepared.Tasks {
			label, _ := Bucket(fixedNow, prepared.Holds, task)
			labels[task.Name] = label
		}
		gomega.Expect(labels["Aborted Task"]).To(gomega.Equal("✅ done · aborted"))
		gomega.Expect(labels["Completed Task"]).To(gomega.Equal("✅ done"))
		gomega.Expect(labels["Hold Task"]).To(gomega.Equal("⏸️ blocked/hold"))
		gomega.Expect(labels["Backlog Task"]).To(gomega.Equal("—"))
		gomega.Expect(labels["Declared Wait Task"]).To(gomega.Equal("⌛ waiting-declared"))
		gomega.Expect(labels["Deferred Task"]).To(gomega.Equal("⏸️ deferred"))
		gomega.Expect(labels["Human Execution Task"]).To(gomega.Equal("👤 YOURS"))
		gomega.Expect(labels["Human Review Task"]).To(gomega.Equal("⌛ waiting-on-human"))
		gomega.Expect(labels["Next Task"]).To(gomega.Equal("🚀 ready-to-start"))
		gomega.Expect(labels["Planning Task"]).To(gomega.Equal("🚀 ready-to-start"))
		gomega.Expect(labels["Multi Goal Task"]).To(gomega.Equal("🚀 ready-to-start"))
	})

	ginkgo.It("errors when the subject cannot resolve", func() {
		cfg := prepareConfig()
		cfg.Subject = "Missing Subject"
		_, err := Prepare(ctx, cfg)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("read timeout"))
	})
})

var _ = ginkgo.Describe("labels a bare status", func() {
	ginkgo.It("reuses the bucket mapping", func() {
		gomega.Expect(StatusLabel(fixedNow, Task{Status: "completed"})).
			To(gomega.Equal("✅ done"))
		gomega.Expect(StatusLabel(fixedNow, Task{Status: "aborted"})).
			To(gomega.Equal("✅ done · aborted"))
		gomega.Expect(StatusLabel(fixedNow, Task{Status: "in_progress"})).
			To(gomega.Equal("🔄 progressing"))
	})
})
