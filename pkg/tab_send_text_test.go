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
		client = newTabClientWithSubmitDelay(stubPath, 0)
	})

	readLines := func() []string {
		raw, err := os.ReadFile(logPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var lines []string
		for _, line := range strings.Split(string(raw), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		return lines
	}

	// Measured 2026-11-18 against freshly spawned Claude Code panes: text and
	// `\r` in ONE send-text submitted 0/5 times (the text sat in the composer),
	// while text, a pause, then `\r` as its own send-text submitted 5/5.
	ginkgo.It("types the text and submits the Enter as a separate send-text", func() {
		gomega.Expect(client.SendText(ctx, "42", "/color green")).To(gomega.Succeed())

		lines := readLines()
		gomega.Expect(lines).To(gomega.HaveLen(2))
		gomega.Expect(lines[0]).To(gomega.HaveSuffix("--no-paste /color green"))
		gomega.Expect(lines[0]).NotTo(gomega.ContainSubstring("\r"))
		gomega.Expect(lines[1]).To(gomega.HaveSuffix("--no-paste \r"))
	})

	ginkgo.It("strips a caller's trailing carriage return before splitting", func() {
		gomega.Expect(client.SendText(ctx, "42", "hello\r")).To(gomega.Succeed())

		lines := readLines()
		gomega.Expect(lines).To(gomega.HaveLen(2))
		gomega.Expect(lines[0]).To(gomega.HaveSuffix("--no-paste hello"))
		gomega.Expect(lines[1]).To(gomega.HaveSuffix("--no-paste \r"))
	})

	ginkgo.It("sends only the Enter for empty text", func() {
		gomega.Expect(client.SendText(ctx, "42", "")).To(gomega.Succeed())

		lines := readLines()
		gomega.Expect(lines).To(gomega.HaveLen(1))
		gomega.Expect(lines[0]).To(gomega.HaveSuffix("--no-paste \r"))
	})

	ginkgo.It("types into the pane without activating it", func() {
		gomega.Expect(client.SendText(ctx, "42", "hello")).To(gomega.Succeed())

		lines := readLines()

		// Activating on every send pulls the operator's tab away, and a
		// 2026-11-09 measurement showed send-text reaches a background pane on
		// its own — so the sends must be the ONLY invocations.
		for _, line := range lines {
			gomega.Expect(line).To(gomega.ContainSubstring("cli send-text --pane-id 42 --no-paste"))
		}
		gomega.Expect(lines).NotTo(gomega.ContainElement(gomega.ContainSubstring("activate-pane")))
	})
})
