// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("SessionRegistry", func() {
	ginkgo.It("excludes an entry whose pid is gone", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{
			PID: os.Getpid(), SessionID: "live-session", Status: "busy", Cwd: "/tmp",
		})
		// A pid nothing can be running under. The registry file is present, so a
		// presence-only read would count it — which is the reading this test
		// exists to exclude.
		writeRegistryEntry(dir, registryFile{
			PID: 999999, SessionID: "dead-session", Status: "busy", Cwd: "/tmp",
		})

		live, err := NewSessionRegistry(dir).Live(context.Background())
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.HaveLen(1))
		gomega.Expect(live[0].SessionID).To(gomega.Equal("live-session"))
	})

	ginkgo.It("returns nil status for an unlisted session rather than a value", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{PID: os.Getpid(), SessionID: "listed", Status: "idle"})

		status, err := NewSessionRegistry(dir).Status(context.Background(), "not-listed")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(status).To(gomega.BeNil())
	})
})

var _ = ginkgo.Describe("TranscriptReader", func() {
	var (
		projectsDir string
		registryDir string
		reader      TranscriptReader
		ctx         context.Context
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		projectsDir = ginkgo.GinkgoT().TempDir()
		registryDir = ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(registryDir, registryFile{
			PID: os.Getpid(), SessionID: "sess-1", Status: "busy", Cwd: "/work/dir",
		})
		reader = NewTranscriptReaderInDir(
			NewSessionRegistry(registryDir),
			libtime.NewCurrentDateTime(),
			projectsDir,
		)
	})

	ginkgo.It("picks the LAST unmatched tool_use, not the first abandoned one", func() {
		writeTranscript(projectsDir, "/work/dir", "sess-1", []string{
			// An interrupted call: tool_use with no result, then the conversation
			// carried on. A scan reporting "any unmatched tool_use" names this one.
			`{"type":"assistant","timestamp":"2026-11-07T10:00:00Z","message":{"content":[{"type":"tool_use","id":"old-call","name":"Bash","input":{"command":"ls"}}]}}`,
			`{"type":"assistant","timestamp":"2026-11-07T10:05:00Z","message":{"content":[{"type":"tool_use","id":"live-call","name":"Read","input":{"file_path":"/tmp/x"}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"live-call"}]}}`,
			`{"type":"assistant","timestamp":"2026-11-07T10:06:00Z","message":{"content":[{"type":"tool_use","id":"newest-call","name":"Grep","input":{"pattern":"x"}}]}}`,
		})

		call, err := reader.CurrentToolCall(ctx, "sess-1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(call).NotTo(gomega.BeNil())
		gomega.Expect(call.Name).To(gomega.Equal("Grep"))
		gomega.Expect(call.InputSummary).To(gomega.ContainSubstring("pattern"))
	})

	ginkgo.It("reports null when the last tool_use is already answered", func() {
		writeTranscript(projectsDir, "/work/dir", "sess-1", []string{
			`{"type":"assistant","timestamp":"2026-11-07T10:00:00Z","message":{"content":[{"type":"tool_use","id":"done","name":"Bash","input":{}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"done"}]}}`,
		})

		call, err := reader.CurrentToolCall(ctx, "sess-1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(call).To(gomega.BeNil())
	})

	ginkgo.It("returns the last assistant text and skips tool-only records", func() {
		writeTranscript(projectsDir, "/work/dir", "sess-1", []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t"}]}}`,
		})

		text, err := reader.LastAssistantText(ctx, "sess-1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(text).NotTo(gomega.BeNil())
		gomega.Expect(*text).To(gomega.Equal("second"))
	})

	ginkgo.It("returns null rather than an error when the session has no transcript", func() {
		text, err := reader.LastAssistantText(ctx, "no-transcript")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(text).To(gomega.BeNil())
	})
})
