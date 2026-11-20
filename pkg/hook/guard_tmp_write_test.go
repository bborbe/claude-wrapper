// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/hook"
)

var _ = Describe("TmpWriteGuard", func() {
	guard := hook.NewTmpWriteGuard()
	input := func(tool string, path string) hook.Input {
		raw, err := json.Marshal(map[string]string{"file_path": path, "content": "x"})
		Expect(err).NotTo(HaveOccurred())
		return hook.Input{HookEventName: hook.EventPreToolUse, ToolName: tool, ToolInput: raw}
	}
	decide := func(tool string, path string) hook.Decision {
		in := input(tool, path)
		if !guard.Applies(in) {
			return hook.Decision{}
		}
		return guard.Evaluate(in)
	}

	DescribeTable("allows only a write that resolves inside /tmp",
		func(tool string, path string, allowed bool) {
			Expect(decide(tool, path).Allow).To(Equal(allowed))
		},
		Entry("Write under /tmp", "Write", "/tmp/pr-body-x.md", true),
		Entry("Edit under /tmp", "Edit", "/tmp/extract/main_test.go", true),
		Entry("Write under /private/tmp", "Write", "/private/tmp/x", true),
		Entry("a dot-dot escape", "Write", "/tmp/../etc/x", false),
		Entry("the settings file", "Write", filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"), false),
		Entry("a relative path", "Write", "tmp/x", false),
		Entry("a tilde path", "Write", "~/x", false),
		Entry("a Read is not a write", "Read", "/tmp/x", false),
		Entry("/tmp itself", "Write", "/tmp", false),
		Entry("a literal $$ in the name is just a name", "Write", "/tmp/pr-body-x-$$.md", true),
	)

	It("refuses a write through a /tmp symlink that points outside /tmp", func() {
		link := filepath.Join("/tmp", fmt.Sprintf("tmp-write-escape-%d", GinkgoRandomSeed()))
		Expect(os.Symlink(GinkgoT().TempDir(), link)).To(Succeed())
		DeferCleanup(os.Remove, link)
		target, err := filepath.EvalSymlinks(link)
		Expect(err).NotTo(HaveOccurred())
		if strings.HasPrefix(target, "/private/tmp/") || strings.HasPrefix(target, "/tmp/") {
			Skip("the test temp dir is itself under /tmp")
		}
		Expect(decide("Write", filepath.Join(link, "new.txt")).Allow).To(BeFalse())
	})

	It("is honoured by the default registry", func() {
		Expect(hook.NewDefaultRegistry().Evaluate(input("Write", "/tmp/x.md")).Allow).To(BeTrue())
	})
})
