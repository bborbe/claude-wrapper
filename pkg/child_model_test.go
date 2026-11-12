// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("resolveChildModel", func() {
	var launcher string
	var missing string

	ginkgo.BeforeEach(func() {
		dir := ginkgo.GinkgoT().TempDir()
		launcher = filepath.Join(dir, "cc-fixture")
		missing = filepath.Join(dir, "does-not-exist")
		script := "#!/bin/bash\n" +
			"# a launch that passes no --model inherits the ambient default\n" +
			"export ANTHROPIC_DEFAULT_OPUS_MODEL=\"deepseek-v4-flash-max[1m]\"\n" +
			"claude \\\n--model \"${ANTHROPIC_DEFAULT_OPUS_MODEL}\" \\\n\"$@\"\n"
		gomega.Expect(os.WriteFile(launcher, []byte(script), 0o600)).To(gomega.Succeed())
	})

	ginkgo.It("keeps an explicit model without consulting the fallback", func() {
		gomega.Expect(resolveChildModel("opus", missing)).To(gomega.Equal("opus"))
	})

	ginkgo.It("falls back to the launcher's model when none is given", func() {
		gomega.Expect(resolveChildModel("", launcher)).To(gomega.Equal("deepseek-v4-flash-max[1m]"))
	})

	ginkgo.It("treats a whitespace-only model as none", func() {
		gomega.Expect(resolveChildModel("  \t", launcher)).To(gomega.Equal("deepseek-v4-flash-max[1m]"))
	})

	ginkgo.It("returns empty when the fallback launcher cannot be read", func() {
		gomega.Expect(resolveChildModel("", missing)).To(gomega.BeEmpty())
	})

	ginkgo.It("puts the fallback model on the child's argv", func() {
		args := buildArgs(newID(), SpawnOptions{Model: resolveChildModel("", launcher)}, "/tmp/generated.json")
		i := slices.Index(args, "--model")
		gomega.Expect(i).To(gomega.BeNumerically(">=", 0))
		gomega.Expect(args[i+1]).To(gomega.Equal("deepseek-v4-flash-max[1m]"))
	})
})
