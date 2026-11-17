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

	ginkgo.It("resolves a session id to the name a peer send would address", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntryNamed(dir, "a", registryFile{PID: os.Getpid(), SessionID: "6d16758d", Name: "claude-1f"})
		writeRegistryEntryNamed(dir, "b", registryFile{PID: os.Getpid(), SessionID: "other", Name: "claude-2a"})

		names, err := NewSessionRegistry(dir).NamesForSession(context.Background(), "6d16758d")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(names).To(gomega.Equal([]string{"claude-1f"}))
	})

	ginkgo.It("drops a crashed session's leftover entry, because a name handed out must answer", func() {
		// The direction decides the pid check: `Named` keeps presence (a stale title
		// still blocks a new one), while this join must not hand out an address that
		// fails at the caller — the unperformable remedy the join exists to remove.
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{PID: 999999, SessionID: "dead", Name: "claude-dead"})

		names, err := NewSessionRegistry(dir).NamesForSession(context.Background(), "dead")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(names).To(gomega.BeEmpty())
	})

	ginkgo.It("keeps every distinct name for one id, so the ambiguity stays visible", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntryNamed(dir, "a", registryFile{PID: os.Getpid(), SessionID: "s1", Name: "claude-a"})
		writeRegistryEntryNamed(dir, "b", registryFile{PID: os.Getpid(), SessionID: "s1", Name: "claude-a"})
		writeRegistryEntryNamed(dir, "c", registryFile{PID: os.Getpid(), SessionID: "s1", Name: "claude-b"})

		names, err := NewSessionRegistry(dir).NamesForSession(context.Background(), "s1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(names).To(gomega.ConsistOf("claude-a", "claude-b"))
	})

	ginkgo.It("reports an unreadable registry as an error, never as no entry", func() {
		// An empty result means "the worker is gone"; an error means "retry". Folding
		// them would report a live worker as exited.
		names, err := NewSessionRegistry(
			ginkgo.GinkgoT().TempDir()+"/does-not-exist",
		).NamesForSession(context.Background(), "s1")
		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(names).To(gomega.BeNil())
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
		reader      TranscriptReader
		ctx         context.Context
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		projectsDir = ginkgo.GinkgoT().TempDir()
		reader = NewTranscriptReaderInDir(
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

		state, err := reader.State(ctx, "sess-1", ptrTo("/work/dir"))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(state.CurrentToolCall).NotTo(gomega.BeNil())
		gomega.Expect(state.CurrentToolCall.Name).To(gomega.Equal("Grep"))
		gomega.Expect(state.CurrentToolCall.InputSummary).To(gomega.ContainSubstring("pattern"))
	})

	ginkgo.It("reports null when the last tool_use is already answered", func() {
		writeTranscript(projectsDir, "/work/dir", "sess-1", []string{
			`{"type":"assistant","timestamp":"2026-11-07T10:00:00Z","message":{"content":[{"type":"tool_use","id":"done","name":"Bash","input":{}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"done"}]}}`,
		})

		state, err := reader.State(ctx, "sess-1", ptrTo("/work/dir"))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(state.CurrentToolCall).To(gomega.BeNil())
	})

	ginkgo.It("returns the last assistant text and skips tool-only records", func() {
		writeTranscript(projectsDir, "/work/dir", "sess-1", []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t"}]}}`,
		})

		state, err := reader.State(ctx, "sess-1", ptrTo("/work/dir"))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(state.LastMessage).NotTo(gomega.BeNil())
		gomega.Expect(*state.LastMessage).To(gomega.Equal("second"))
	})

	ginkgo.It("answers both fields from one read of the same window", func() {
		// The pair is one pass over one file read. Two methods read the same tail
		// twice, which measured 6.5 s of a 20 s roster sweep over 62 sessions.
		writeTranscript(projectsDir, "/work/dir", "sess-1", []string{
			`{"type":"assistant","timestamp":"2026-11-07T10:00:00Z","message":{"content":[{"type":"text","text":"working"},{"type":"tool_use","id":"live","name":"Read","input":{}}]}}`,
		})

		state, err := reader.State(ctx, "sess-1", ptrTo("/work/dir"))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(state.LastMessage).NotTo(gomega.BeNil())
		gomega.Expect(*state.LastMessage).To(gomega.Equal("working"))
		gomega.Expect(state.CurrentToolCall).NotTo(gomega.BeNil())
		gomega.Expect(state.CurrentToolCall.Name).To(gomega.Equal("Read"))
	})

	ginkgo.It("returns null rather than an error when the session has no transcript", func() {
		state, err := reader.State(ctx, "no-transcript", ptrTo("/work/dir"))
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(state.LastMessage).To(gomega.BeNil())
		gomega.Expect(state.CurrentToolCall).To(gomega.BeNil())
	})

	ginkgo.It("reads nothing when the registry listed no cwd for the session", func() {
		// No cwd means no path to derive. The empty path must come back as an
		// unread row, never as a read of some guessed file.
		state, err := reader.State(ctx, "sess-1", nil)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(state.LastMessage).To(gomega.BeNil())
		gomega.Expect(state.CurrentToolCall).To(gomega.BeNil())
	})
})

// ptrTo returns a pointer to a copy of value.
func ptrTo(value string) *string { return &value }
