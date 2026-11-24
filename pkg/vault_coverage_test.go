// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("vault coverage check", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	// writeTask writes a task page with the given frontmatter flag value.
	writeTask := func(dir, name, flagValue string) {
		gomega.Expect(os.MkdirAll(dir, 0o755)).To(gomega.Succeed())
		body := "---\nstatus: in_progress\nflag: " + flagValue + "\n---\n\n# Impact\n"
		gomega.Expect(os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644)).
			To(gomega.Succeed())
	}

	ginkgo.It("reports a vault in an unserved family that holds a flagged row", func() {
		root := ginkgo.GinkgoT().TempDir()
		writeTask(filepath.Join(root, "24 Tasks"), "Flagged Work", "true")
		vaults := []Vault{{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"}}

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.HaveLen(1))
		gomega.Expect(issues[0].Vault).To(gomega.Equal("assistant-data"))
		gomega.Expect(issues[0].Family).To(gomega.Equal("assistant"))
		gomega.Expect(issues[0].Task).To(gomega.Equal(filepath.Join("24 Tasks", "Flagged Work.md")))
	})

	ginkgo.It("ignores an unserved vault with no flagged row", func() {
		root := ginkgo.GinkgoT().TempDir()
		writeTask(filepath.Join(root, "24 Tasks"), "Unflagged Work", "false")
		vaults := []Vault{{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"}}

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeEmpty())
	})

	ginkgo.It("ignores a served family even when it holds flagged rows", func() {
		root := ginkgo.GinkgoT().TempDir()
		writeTask(filepath.Join(root, "25 Tasks"), "Flagged Work", "true")
		vaults := []Vault{{Name: "seibert-agent", Path: root, TasksDir: "25 Tasks"}}

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeEmpty())
	})

	ginkgo.It("ignores an unserved vault whose task folder does not exist", func() {
		root := ginkgo.GinkgoT().TempDir()
		vaults := []Vault{{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"}}

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an empty served set rather than passing on every vault", func() {
		vaults := []Vault{{Name: "assistant-data"}}

		issues, err := CheckVaultCoverage(ctx, vaults, nil)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeNil())
	})
})
