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

func resolveFixture(ctx context.Context, vault string) []Task {
	_, members, err := ResolveSubject(ctx, vault, "Parity Topic")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	tasks, err := ResolveTracked(ctx, vault, members)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return tasks
}

var _ = ginkgo.Describe("reads a state file written by the python", func() {
	ctx := context.Background()

	ginkgo.It("matches the Python digest for the fixture vault", func() {
		file := filepath.Join("..", "..", "testdata", "sweepgate", "state", "parity-topic.json")
		pythonDigest, failReason, err := LoadStored(ctx, file)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failReason).To(gomega.BeEmpty())

		tasks := resolveFixture(ctx, fixtureVault())
		goDigest := Digest(tasks)

		ginkgo.GinkgoWriter.Printf("file=%s python_digest=%s go_digest=%s equal=%t\n",
			file, pythonDigest, goDigest, pythonDigest == goDigest)
		gomega.Expect(goDigest).To(gomega.Equal(pythonDigest))
	})
})

var _ = ginkgo.Describe("computes the digest from the rendered state", func() {
	base := func() []Task {
		return []Task{
			{
				Name: "Alpha", Status: "in_progress", Phase: "execution",
				Session: "session-alpha", ProgressHash: "progress-alpha",
			},
			{
				Name: "Beta", Status: "next", Phase: "", Session: "",
				ProgressHash: "progress-beta",
			},
		}
	}

	ginkgo.It("moves on a status change", func() {
		changed := base()
		changed[0].Status = "completed"
		gomega.Expect(Digest(changed)).NotTo(gomega.Equal(Digest(base())))
	})

	ginkgo.It("moves on a phase change", func() {
		changed := base()
		changed[0].Phase = "human_review"
		gomega.Expect(Digest(changed)).NotTo(gomega.Equal(Digest(base())))
	})

	ginkgo.It("moves on a session id change", func() {
		changed := base()
		changed[0].Session = "session-other"
		gomega.Expect(Digest(changed)).NotTo(gomega.Equal(Digest(base())))
	})

	ginkgo.It("moves when a task is added", func() {
		changed := append(base(), Task{Name: "Gamma"})
		gomega.Expect(Digest(changed)).NotTo(gomega.Equal(Digest(base())))
	})

	ginkgo.It("moves when a task is removed", func() {
		changed := base()[:1]
		gomega.Expect(Digest(changed)).NotTo(gomega.Equal(Digest(base())))
	})

	ginkgo.It("moves on a new Progress entry", func() {
		changed := base()
		changed[0].ProgressHash = "progress-alpha-updated"
		gomega.Expect(Digest(changed)).NotTo(gomega.Equal(Digest(base())))
	})

	ginkgo.It("does not move when a task outside the tracked set changes", func() {
		vault := fixtureVault()
		before := Digest(resolveFixture(context.Background(), vault))

		copied := ginkgo.GinkgoT().TempDir()
		copyTree(copied, vault)
		untracked := filepath.Join(copied, "25 Tasks", "Untracked Goal Task.md")
		gomega.Expect(os.WriteFile(untracked,
			[]byte("---\nstatus: completed\nphase: execution\ngoals: ['[[Untracked Goal]]']\n---\n"),
			0o600)).To(gomega.Succeed())

		after := Digest(resolveFixture(context.Background(), copied))
		gomega.Expect(after).To(gomega.Equal(before))
	})

	ginkgo.It("is independent of the input order", func() {
		reversed := []Task{base()[1], base()[0]}
		gomega.Expect(Digest(reversed)).To(gomega.Equal(Digest(base())))
	})

	ginkgo.It("defaults liveness to none and stuck to false", func() {
		enriched := base()
		for i := range enriched {
			enriched[i].Liveness = "none"
			enriched[i].Stuck = false
		}
		gomega.Expect(Digest(enriched)).To(gomega.Equal(Digest(base())))
	})
})

// copyTree recursively copies src into dst.
func copyTree(dst, src string) {
	entries, err := os.ReadDir(src)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			gomega.Expect(os.MkdirAll(dstPath, 0o750)).To(gomega.Succeed())
			copyTree(dstPath, srcPath)
			continue
		}
		raw, err := os.ReadFile(srcPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(dstPath, raw, 0o600)).To(gomega.Succeed())
	}
}
