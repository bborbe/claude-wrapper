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

	// writePage writes a page whose frontmatter carries the given `flag:` value.
	writePage := func(dir, name, flagValue string) {
		gomega.Expect(os.MkdirAll(dir, 0o755)).To(gomega.Succeed())
		body := "---\nstatus: in_progress\nflag: " + flagValue + "\n---\n\n# Impact\n"
		gomega.Expect(os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644)).
			To(gomega.Succeed())
	}

	// servedFixtures are empty vaults in the two served families, so a served set
	// naming them matches a configured vault (the check refuses a family that
	// matches none).
	servedFixtures := func() []Vault {
		return []Vault{
			{Name: "private-personal", Path: ginkgo.GinkgoT().TempDir(), TasksDir: "25 Tasks"},
			{Name: "seibert-brogrammers", Path: ginkgo.GinkgoT().TempDir(), TasksDir: "25 Tasks"},
		}
	}

	ginkgo.It("reports an unserved vault holding a flagged task", func() {
		root := ginkgo.GinkgoT().TempDir()
		writePage(filepath.Join(root, "24 Tasks"), "Flagged Work", "true")
		vaults := append(servedFixtures(),
			Vault{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"})

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.HaveLen(1))
		gomega.Expect(issues[0].Vault).To(gomega.Equal("assistant-data"))
		gomega.Expect(issues[0].Family).To(gomega.Equal("assistant"))
		gomega.Expect(issues[0].Page).To(gomega.Equal(filepath.Join("24 Tasks", "Flagged Work.md")))
	})

	ginkgo.It("reports an unserved vault holding a flagged goal, not only a task", func() {
		root := ginkgo.GinkgoT().TempDir()
		writePage(filepath.Join(root, "24 Goals"), "Flagged Goal", "true")
		vaults := append(servedFixtures(),
			Vault{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"})

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.HaveLen(1))
		gomega.Expect(issues[0].Page).To(gomega.Equal(filepath.Join("24 Goals", "Flagged Goal.md")))
	})

	ginkgo.It("accepts the flag spellings the row source accepts", func() {
		for _, spelling := range []string{"True", "yes", "true  # note"} {
			root := ginkgo.GinkgoT().TempDir()
			writePage(filepath.Join(root, "24 Tasks"), "Flagged Work", spelling)
			vaults := append(servedFixtures(),
				Vault{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"})

			issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(issues).To(gomega.HaveLen(1), "spelling %q", spelling)
		}
	})

	ginkgo.It("falls back to the lister's folders when tasks_dir is unset", func() {
		root := ginkgo.GinkgoT().TempDir()
		writePage(filepath.Join(root, "25 Tasks"), "Flagged Work", "true")
		vaults := append(servedFixtures(), Vault{Name: "assistant-data", Path: root})

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.HaveLen(1))
	})

	ginkgo.It("ignores an unserved vault with no flagged page", func() {
		root := ginkgo.GinkgoT().TempDir()
		writePage(filepath.Join(root, "24 Tasks"), "Unflagged Work", "false")
		vaults := append(servedFixtures(),
			Vault{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"})

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeEmpty())
	})

	ginkgo.It("ignores a served family even when it holds flagged rows", func() {
		root := ginkgo.GinkgoT().TempDir()
		writePage(filepath.Join(root, "25 Tasks"), "Flagged Work", "true")
		vaults := append(servedFixtures(),
			Vault{Name: "seibert-agent", Path: root, TasksDir: "25 Tasks"})

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeEmpty())
	})

	ginkgo.It("reports only the unserved vault when a served one also holds flagged rows", func() {
		servedRoot := ginkgo.GinkgoT().TempDir()
		writePage(filepath.Join(servedRoot, "25 Tasks"), "Served Work", "true")
		unservedRoot := ginkgo.GinkgoT().TempDir()
		writePage(filepath.Join(unservedRoot, "24 Tasks"), "Unserved Work", "true")
		vaults := append(servedFixtures(),
			Vault{Name: "seibert-agent", Path: servedRoot, TasksDir: "25 Tasks"},
			Vault{Name: "assistant-data", Path: unservedRoot, TasksDir: "24 Tasks"},
		)

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(issues).To(gomega.HaveLen(1))
		gomega.Expect(issues[0].Vault).To(gomega.Equal("assistant-data"))
	})

	ginkgo.It("ignores an unserved vault whose task folder does not exist", func() {
		root := ginkgo.GinkgoT().TempDir()
		vaults := append(servedFixtures(),
			Vault{Name: "assistant-data", Path: root, TasksDir: "24 Tasks"})

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

	ginkgo.It("refuses a served family that matches no configured vault", func() {
		vaults := []Vault{{Name: "assistant-data"}}

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"privte"})
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeNil())
	})

	ginkgo.It("ignores an unserved vault with no path only when it is unserved and empty", func() {
		// A pathless vault cannot be verified; the check must say so rather than
		// report it as holding no flagged row.
		vaults := []Vault{{Name: "assistant-data", Path: ""}}

		issues, err := CheckVaultCoverage(ctx, vaults, []string{"private", "seibert"})
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(issues).To(gomega.BeNil())
	})
})
