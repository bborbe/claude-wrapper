// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fleetsnapshot

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Persist", func() {
	ctx := context.Background()

	ginkgo.It("refuses to persist when the previous snapshot was not loaded", func() {
		// This is the whole reason the package exists. `stall_count` is computed
		// from the previous snapshot, so persisting without one resets every
		// counter and makes every session read first-seen — and the file it writes
		// is well-formed, so the loss is silent.
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")
		const existing = `{"swept_at":"2026-11-19T10:00:00Z","sessions":{"s1":{"name":"A","status":"busy","stall_count":3}}}`
		gomega.Expect(os.WriteFile(path, []byte(existing), 0o600)).To(gomega.Succeed())

		err := Persist(ctx, path, Snapshot{
			SweptAt:  "2026-11-19T11:00:00Z",
			Sessions: map[string]Entry{"s1": {Name: "A", Status: "idle"}},
		}, false)

		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("not loaded"))
		// The refusal must leave the file byte-identical, not merely unmodified in
		// memory: the next round diffs against this file.
		after, readErr := os.ReadFile(path)
		gomega.Expect(readErr).To(gomega.BeNil())
		gomega.Expect(string(after)).To(gomega.Equal(existing))
	})

	ginkgo.It("persists when the previous snapshot WAS loaded", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")
		gomega.Expect(os.WriteFile(path, []byte(`{"swept_at":"x","sessions":{"s1":{"name":"A"}}}`), 0o600)).To(gomega.Succeed())

		_, loaded, err := Load(ctx, path)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(loaded).To(gomega.BeTrue())

		writeErr := Persist(ctx, path, Snapshot{
			SweptAt:  "2026-11-19T11:00:00Z",
			Sessions: map[string]Entry{"s1": {Name: "A", Status: "idle", StallCount: 0}},
		}, loaded)
		gomega.Expect(writeErr).To(gomega.BeNil())

		back, loadedBack, loadErr := Load(ctx, path)
		gomega.Expect(loadErr).To(gomega.BeNil())
		gomega.Expect(loadedBack).To(gomega.BeTrue())
		gomega.Expect(back.Sessions["s1"].Status).To(gomega.Equal("idle"))
	})

	ginkgo.It("refuses an empty snapshot even when the load succeeded", func() {
		// A failed read and an empty fleet must not look alike. This is a
		// different refusal from the load guard: the round CAN load correctly and
		// still hold no sessions.
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")

		err := Persist(ctx, path, Snapshot{Sessions: map[string]Entry{}}, true)

		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("empty fleet"))
		_, statErr := os.Stat(path)
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Load", func() {
	ctx := context.Background()

	ginkgo.It("reports a missing file as not-loaded without an error", func() {
		// A fresh install's first round has no previous snapshot. That is not a
		// failure — but it must still read as not-loaded, or the guard is bypassed
		// by the very first round.
		dir := ginkgo.GinkgoT().TempDir()

		snap, loaded, err := Load(ctx, filepath.Join(dir, "absent.json"))

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(loaded).To(gomega.BeFalse())
		gomega.Expect(snap.Sessions).ToNot(gomega.BeNil())
	})

	ginkgo.It("reports a corrupt file as not-loaded WITH an error", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")
		gomega.Expect(os.WriteFile(path, []byte("{not json"), 0o600)).To(gomega.Succeed())

		_, loaded, err := Load(ctx, path)

		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(loaded).To(gomega.BeFalse())
	})
})
