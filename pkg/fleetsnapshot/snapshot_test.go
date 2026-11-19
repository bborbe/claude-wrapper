// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fleetsnapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Persist", func() {
	ctx := context.Background()

	ginkgo.It("refuses when the previous snapshot could not be loaded", func() {
		// The whole reason the package exists. `stall_count` is computed from the
		// previous snapshot, so persisting after a FAILED load resets every counter
		// and makes every session read first-seen — and the file it writes is
		// well-formed, so the loss is silent.
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")
		const existing = `{"swept_at":"2026-11-19T10:00:00Z","sessions":{"s1":{"name":"A","status":"busy","stall_count":3}}}`
		gomega.Expect(os.WriteFile(path, []byte(existing), 0o600)).To(gomega.Succeed())

		loadErr := errors.New("boom")

		err := Persist(ctx, path, Snapshot{
			SweptAt:  "2026-11-19T11:00:00Z",
			Sessions: map[string]Entry{"s1": {Name: "A", Status: "idle"}},
		}, loadErr)

		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("could not be loaded"))
		// The refusal must leave the file byte-identical, not merely unmodified in
		// memory: the next round diffs against this file.
		after, readErr := os.ReadFile(path)
		gomega.Expect(readErr).To(gomega.BeNil())
		gomega.Expect(string(after)).To(gomega.Equal(existing))
	})

	ginkgo.It("persists on a FRESH INSTALL where no previous snapshot exists", func() {
		// The regression this spec exists for: an earlier shape refused here too,
		// because a missing file and a failed read shared one boolean. That made a
		// fresh install persist nothing, forever — a deadlock, not a safety
		// property.
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")

		prev, loadErr := Load(ctx, path)
		gomega.Expect(loadErr).To(gomega.BeNil())
		gomega.Expect(prev.Sessions).To(gomega.BeEmpty())

		writeErr := Persist(ctx, path, Snapshot{
			SweptAt:  "2026-11-19T11:00:00Z",
			Sessions: map[string]Entry{"s1": {Name: "A", Status: "busy"}},
		}, loadErr)
		gomega.Expect(writeErr).To(gomega.BeNil())

		_, statErr := os.Stat(path)
		gomega.Expect(statErr).To(gomega.BeNil())
	})

	ginkgo.It("persists after a successful load and round-trips", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")
		gomega.Expect(os.WriteFile(path, []byte(`{"swept_at":"x","sessions":{"s1":{"name":"A"}}}`), 0o600)).To(gomega.Succeed())

		prev, loadErr := Load(ctx, path)
		gomega.Expect(loadErr).To(gomega.BeNil())
		gomega.Expect(prev.Sessions).To(gomega.HaveKey("s1"))

		writeErr := Persist(ctx, path, Snapshot{
			SweptAt:  "2026-11-19T11:00:00Z",
			Sessions: map[string]Entry{"s1": {Name: "A", Status: "idle", StallCount: 0}},
		}, loadErr)
		gomega.Expect(writeErr).To(gomega.BeNil())

		back, loadBackErr := Load(ctx, path)
		gomega.Expect(loadBackErr).To(gomega.BeNil())
		gomega.Expect(back.Sessions["s1"].Status).To(gomega.Equal("idle"))
	})

	ginkgo.It("refuses an empty snapshot even when the load succeeded", func() {
		// A failed read and an empty fleet must not look alike. This is a
		// different refusal from the load guard: the round CAN load correctly and
		// still hold no sessions.
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")

		err := Persist(ctx, path, Snapshot{Sessions: map[string]Entry{}}, nil)

		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("empty fleet"))
		_, statErr := os.Stat(path)
		gomega.Expect(os.IsNotExist(statErr)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Load", func() {
	ctx := context.Background()

	ginkgo.It("reports a missing file as an empty snapshot WITHOUT an error", func() {
		// A fresh install's first round has no previous snapshot. That is not a
		// failure, and the empty snapshot it returns is a legitimate thing to diff
		// against.
		dir := ginkgo.GinkgoT().TempDir()

		snap, err := Load(ctx, filepath.Join(dir, "absent.json"))

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(snap.Sessions).ToNot(gomega.BeNil())
		gomega.Expect(snap.Sessions).To(gomega.BeEmpty())
	})

	ginkgo.It("reports a corrupt file WITH an error", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "fleet-snapshot.json")
		gomega.Expect(os.WriteFile(path, []byte("{not json"), 0o600)).To(gomega.Succeed())

		_, err := Load(ctx, path)

		gomega.Expect(err).ToNot(gomega.BeNil())
	})
})
