// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fixtureVault is the checked-in parity vault, relative to this package's test
// working directory.
func fixtureVault() string {
	return filepath.Join("..", "..", "testdata", "sweepgate", "vault")
}

var _ = ginkgo.Describe("resolves the tracked set from a fixture vault", func() {
	ctx := context.Background()

	ginkgo.It("resolves the topic subject to its declared members", func() {
		branch, members, err := ResolveSubject(ctx, fixtureVault(), "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(branch).To(gomega.Equal("topic"))
		gomega.Expect(members).To(gomega.Equal([]string{"Parity Goal"}))
	})

	ginkgo.It("resolves the goal subject by folder alone", func() {
		branch, members, err := ResolveSubject(ctx, fixtureVault(), "Parity Goal")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(branch).To(gomega.Equal("goal"))
		gomega.Expect(members).To(gomega.Equal([]string{"Parity Goal"}))
	})

	ginkgo.It("returns exactly the expected tracked names, sorted", func() {
		_, members, err := ResolveSubject(ctx, fixtureVault(), "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		tasks, err := ResolveTracked(ctx, fixtureVault(), members)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		names := make([]string, 0, len(tasks))
		for _, task := range tasks {
			names = append(names, task.Name)
		}
		gomega.Expect(names).To(gomega.Equal([]string{
			"Aborted Task",
			"Backlog Task",
			"Blocked Task",
			"Completed Task",
			"Declared Wait Task",
			"Deferred Task",
			"Hold Task",
			"Human Execution Task",
			"Human Review Task",
			"Multi Goal Task",
			"Next Task",
			"Planning Task",
		}))
	})

	ginkgo.It("lists a multi-goal task once", func() {
		_, members, err := ResolveSubject(ctx, fixtureVault(), "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		tasks, err := ResolveTracked(ctx, fixtureVault(), members)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		count := 0
		for _, task := range tasks {
			if task.Name == "Multi Goal Task" {
				count++
			}
		}
		gomega.Expect(count).To(gomega.Equal(1))
	})

	ginkgo.It("keeps the untracked-goal control out of the set", func() {
		_, members, err := ResolveSubject(ctx, fixtureVault(), "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		tasks, err := ResolveTracked(ctx, fixtureVault(), members)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, task := range tasks {
			gomega.Expect(task.Name).NotTo(gomega.Equal("Untracked Goal Task"))
		}
	})

	ginkgo.It("returns an empty set with no error when 25 Tasks is missing", func() {
		vault := ginkgo.GinkgoT().TempDir()
		tasks, err := ResolveTracked(ctx, vault, []string{"Parity Goal"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(tasks).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an ambiguous subject naming both paths", func() {
		vault := ginkgo.GinkgoT().TempDir()
		gomega.Expect(os.MkdirAll(filepath.Join(vault, "24 Goals"), 0o750)).To(gomega.Succeed())
		gomega.Expect(os.MkdirAll(filepath.Join(vault, "23 Topics"), 0o750)).To(gomega.Succeed())
		goal := filepath.Join(vault, "24 Goals", "Both.md")
		topic := filepath.Join(vault, "23 Topics", "Both.md")
		gomega.Expect(os.WriteFile(goal, []byte("---\n---\n"), 0o600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(topic, []byte("---\npage_type: topic\n---\n"), 0o600)).
			To(gomega.Succeed())
		_, _, err := ResolveSubject(ctx, vault, "Both")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(goal))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(topic))
	})

	ginkgo.It("refuses a subject that resolves no page", func() {
		_, _, err := ResolveSubject(ctx, ginkgo.GinkgoT().TempDir(), "Missing")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("reads the task mtime from the descriptor", func() {
		task, ok, err := ReadTask(ctx, filepath.Join(fixtureVault(), "25 Tasks", "Next Task.md"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(task.Name).To(gomega.Equal("Next Task"))
		gomega.Expect(task.Mtime.IsZero()).To(gomega.BeFalse())
	})

	ginkgo.It("returns false with no error for an unreadable task", func() {
		_, ok, err := ReadTask(ctx, filepath.Join(ginkgo.GinkgoT().TempDir(), "missing.md"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ok).To(gomega.BeFalse())
	})

	ginkgo.It("composes the snapshotter", func() {
		snapshotter := NewVaultSnapshotter(Config{Vault: fixtureVault()})
		snapshot, err := snapshotter.Snapshot(ctx, "Parity Topic")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snapshot.Branch).To(gomega.Equal("topic"))
		gomega.Expect(snapshot.Members).To(gomega.Equal([]string{"Parity Goal"}))
		gomega.Expect(snapshot.Tasks).To(gomega.HaveLen(12))
	})
})
