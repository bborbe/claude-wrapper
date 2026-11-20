// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// stubScriptRunner returns a fixed (output, exit code) and records the args.
type stubScriptRunner struct {
	output string
	code   int
}

func (r stubScriptRunner) run(context.Context, ...string) (string, int, error) {
	return r.output, r.code, nil
}

var _ = ginkgo.Describe("NewKeepaliveFolderFor", func() {
	ctx := context.Background()
	vault := Vault{Name: "private-agent", Path: "/tmp/private-agent"}

	// ⚠️ `UNKNOWN vault '<name>' does not configure <key>` (exit 1) is an ABSENT
	// folder, not a failure. Measured 2026-11-20: the first live ticks logged
	// `result=error` for NINE such vaults, which reads as a broken tick when it
	// is a real answer.
	ginkgo.It("treats resolve-folders' does-not-configure refusal as an absent folder", func() {
		folderFor := NewKeepaliveFolderFor(
			stubScriptRunner{output: "UNKNOWN vault 'private-agent' does not configure topics_dir", code: 1}.run,
			"topics_dir",
		)

		dir, err := folderFor(ctx, vault)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(dir).To(gomega.BeEmpty())
	})

	ginkgo.It("keeps every other non-zero exit an error", func() {
		folderFor := NewKeepaliveFolderFor(
			stubScriptRunner{output: "vault-cli config list exited 1", code: 1}.run,
			"topics_dir",
		)

		_, err := folderFor(ctx, vault)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("reads the required field on success", func() {
		folderFor := NewKeepaliveFolderFor(
			stubScriptRunner{output: "/v/23 Topics|/v/24 Goals|/v/25 Tasks|private-agent", code: 0}.run,
			"tasks_dir",
		)

		dir, err := folderFor(ctx, vault)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(dir).To(gomega.Equal("/v/25 Tasks"))
	})
})
