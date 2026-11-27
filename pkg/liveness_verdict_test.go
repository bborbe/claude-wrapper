// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("LivenessVerdicts", func() {
	ctx := context.Background()

	ginkgo.It("reads live when the registry lists the pid and a heartbeat corroborates", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{PID: os.Getpid(), SessionID: "s1", Status: "busy"})

		verdicts, err := LivenessVerdicts(
			ctx,
			NewSessionRegistry(dir),
			nil,
			[]HeartbeatRow{{SessionID: "s1", Live: true}},
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Liveness).To(gomega.Equal(LivenessLive))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("registry"))
		gomega.Expect(verdicts[0].Heartbeat).ToNot(gomega.BeNil())
		gomega.Expect(*verdicts[0].Heartbeat).To(gomega.BeTrue())
	})

	ginkgo.It("reads live when the registry lists the pid and there is NO heartbeat row", func() {
		// This is the bug being fixed: a live pid with no heartbeat row must not
		// read UNKNOWN. The registry alone is a primary source and settles it.
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{PID: os.Getpid(), SessionID: "s1", Status: "busy"})

		verdicts, err := LivenessVerdicts(ctx, NewSessionRegistry(dir), nil, nil)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Liveness).To(gomega.Equal(LivenessLive))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("registry"))
		gomega.Expect(verdicts[0].Heartbeat).To(gomega.BeNil())
	})

	ginkgo.It("reads not-live when the child exited and the registry does not list it", func() {
		dir := ginkgo.GinkgoT().TempDir()
		exitCode := 1

		verdicts, err := LivenessVerdicts(
			ctx,
			NewSessionRegistry(dir),
			[]Session{{ID: "s1", ExitCode: &exitCode}},
			nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Liveness).To(gomega.Equal(LivenessNotLive))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("child-table"))
	})

	ginkgo.It("reads live for a child with no exit code and no registry entry", func() {
		dir := ginkgo.GinkgoT().TempDir()

		verdicts, err := LivenessVerdicts(
			ctx,
			NewSessionRegistry(dir),
			[]Session{{ID: "s1"}},
			nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Liveness).To(gomega.Equal(LivenessLive))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("child-table"))
	})

	ginkgo.It("reads not-live for an adopted child the wrapper reaped (exited, no exit code)", func() {
		// An adopted child has no cmd.Wait, so reap settles it with a nil exit
		// code. The idle-reaper closing an adopted headless session left the
		// child table at `state: exited, exit_code: null`, and the verdict stayed
		// live — the flagged tick skipped the row `not-absent` on every cycle.
		dir := ginkgo.GinkgoT().TempDir()
		c := &child{subscribers: map[int]chan Event{}}
		c.session.ID = "s1"
		c.session.State = StateIdle
		c.reap(nil)

		verdicts, err := LivenessVerdicts(
			ctx,
			NewSessionRegistry(dir),
			[]Session{*c.snapshot()},
			nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Liveness).To(gomega.Equal(LivenessNotLive))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("child-table"))
	})

	ginkgo.It("still reads live for an exited child the registry lists", func() {
		// The double-open guard: a live registry pid wins over the child row.
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{PID: os.Getpid(), SessionID: "s1", Status: "idle"})

		verdicts, err := LivenessVerdicts(
			ctx,
			NewSessionRegistry(dir),
			[]Session{{ID: "s1", State: StateExited}},
			nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Liveness).To(gomega.Equal(LivenessLive))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("registry"))
	})

	ginkgo.It("reads not-live for a session present ONLY in the heartbeat store", func() {
		// The heartbeat is corroboration only. It must never create a live verdict
		// on its own — a stale or self-posted row would otherwise resurrect a dead
		// session.
		dir := ginkgo.GinkgoT().TempDir()

		verdicts, err := LivenessVerdicts(
			ctx,
			NewSessionRegistry(dir),
			nil,
			[]HeartbeatRow{{SessionID: "s1", Live: true}},
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Liveness).To(gomega.Equal(LivenessNotLive))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("none"))
		gomega.Expect(verdicts[0].Heartbeat).ToNot(gomega.BeNil())
		gomega.Expect(*verdicts[0].Heartbeat).To(gomega.BeTrue())
	})

	ginkgo.It("returns an error and no verdicts when the registry cannot be read", func() {
		verdicts, err := LivenessVerdicts(
			ctx,
			NewSessionRegistry(ginkgo.GinkgoT().TempDir()+"/does-not-exist"),
			[]Session{{ID: "s1"}},
			nil,
		)
		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.BeNil())
	})

	ginkgo.It("returns verdicts sorted by session id", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntryNamed(dir, "b", registryFile{PID: os.Getpid(), SessionID: "b"})
		writeRegistryEntryNamed(dir, "a", registryFile{PID: os.Getpid(), SessionID: "a"})

		verdicts, err := LivenessVerdicts(ctx, NewSessionRegistry(dir), nil, nil)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(2))
		gomega.Expect(verdicts[0].SessionID).To(gomega.Equal("a"))
		gomega.Expect(verdicts[1].SessionID).To(gomega.Equal("b"))
	})

	ginkgo.It("carries the registry tab title on a registry verdict", func() {
		// `flagged-rows.py` decides "is this subject's manager live?" by NAME, so a
		// verdict without the name forces a second derivation of the identity map.
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{
			PID: os.Getpid(), SessionID: "s1", Status: "busy", Name: "Fleet Manager",
		})

		verdicts, err := LivenessVerdicts(ctx, NewSessionRegistry(dir), nil, nil)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Name).To(gomega.Equal("Fleet Manager"))
	})

	ginkgo.It("leaves the name empty on a child-table verdict", func() {
		// The name exists only on a registry entry; a child-table verdict must not
		// invent one, because a consumer treats a name as an identity match.
		dir := ginkgo.GinkgoT().TempDir()

		verdicts, err := LivenessVerdicts(ctx, NewSessionRegistry(dir), []Session{{ID: "s1"}}, nil)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(verdicts).To(gomega.HaveLen(1))
		gomega.Expect(verdicts[0].Source).To(gomega.Equal("child-table"))
		gomega.Expect(verdicts[0].Name).To(gomega.BeEmpty())
	})
})
