// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("tabClient.SendText", func() {
	var (
		ctx     context.Context
		dir     string
		logPath string
		client  TabClient
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
		logPath = filepath.Join(dir, "argv.log")
		stubPath := filepath.Join(dir, "wezterm")
		// one line per invocation: the stub's full argv
		script := "#!/bin/sh\n" +
			"printf '%s\\n' \"$*\" >> '" + logPath + "'\n"
		gomega.Expect(os.WriteFile(stubPath, []byte(script), 0o700)).To(gomega.Succeed())
		client = NewTabClient(stubPath)
	})

	ginkgo.It("types into the pane without activating it", func() {
		gomega.Expect(client.SendText(ctx, "42", "hello")).To(gomega.Succeed())

		raw, err := os.ReadFile(logPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		var lines []string
		for _, line := range strings.Split(string(raw), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}

		// Activating on every send pulls the operator's tab away, and a
		// 2026-11-09 measurement showed send-text reaches a background pane on
		// its own — so the send must be the ONLY invocation.
		gomega.Expect(lines).To(gomega.HaveLen(1))
		gomega.Expect(lines[0]).To(gomega.ContainSubstring("cli send-text --pane-id 42 --no-paste"))
		gomega.Expect(lines).NotTo(gomega.ContainElement(gomega.ContainSubstring("activate-pane")))
	})
})
