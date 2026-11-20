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

var _ = ginkgo.Describe("NewKeepalivePageSource", func() {
	ctx := context.Background()

	writePage := func(dir, name, body string) {
		path := filepath.Join(dir, name+".md")
		gomega.Expect(os.WriteFile(path, []byte(body), 0o600)).To(gomega.Succeed())
	}

	ginkgo.It("reads status and claude_session_id from each *.md page", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writePage(dir, "task-a", "---\nstatus: in_progress\nclaude_session_id: s1\n---\nbody\n")
		writePage(dir, "task-b", "---\nstatus: completed\n---\nbody\n")

		source := NewKeepalivePageSource(
			[]Vault{{Name: "v"}},
			func(context.Context, Vault) (string, error) { return dir, nil },
			nil,
		)

		pages, err := source(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(pages).To(gomega.ConsistOf(
			KeepalivePage{Vault: "v", Name: "task-a", SessionID: "s1", Status: "in_progress"},
			KeepalivePage{Vault: "v", Name: "task-b", SessionID: "", Status: "completed"},
		))
	})

	// A folderFor failure skips that vault (logged), and a healthy second vault
	// still contributes its pages.
	ginkgo.It("skips a vault whose folder lookup fails and continues", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writePage(dir, "task-a", "---\nstatus: in_progress\nclaude_session_id: s1\n---\n")

		logged := &[]string{}
		source := NewKeepalivePageSource(
			[]Vault{{Name: "bad"}, {Name: "good"}},
			func(_ context.Context, vault Vault) (string, error) {
				if vault.Name == "bad" {
					return "", os.ErrNotExist
				}
				return dir, nil
			},
			func(line string) { *logged = append(*logged, line) },
		)

		pages, err := source(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(pages).To(gomega.HaveLen(1))
		gomega.Expect(pages[0].Vault).To(gomega.Equal("good"))
		gomega.Expect(*logged).To(gomega.HaveLen(1))
		gomega.Expect((*logged)[0]).To(gomega.ContainSubstring("result=error"))
	})
})
