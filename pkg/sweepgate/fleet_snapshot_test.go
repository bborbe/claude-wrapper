// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/fleetsnapshot"
)

var _ = ginkgo.Describe("FleetSnapshot", func() {
	base := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	ginkgo.It("fans a task's session ids out to one entry each", func() {
		// The projection is a fan-out, not a rename: the frame is task-keyed and
		// the snapshot is session-keyed, and a task may own several sessions.
		prepared := Prepared{
			Tasks: []Task{
				{Name: "A Task", SessionIDs: []string{"s1", "s2"}, Mtime: base},
				{Name: "No Session"},
			},
			Registry: map[string]RegistryRecord{
				"s1": {Status: "busy"},
				"s2": {Status: "idle"},
			},
		}

		snap := FleetSnapshot(prepared, "2026-11-19T10:00:00Z", fleetsnapshot.Snapshot{})

		gomega.Expect(snap.Sessions).To(gomega.HaveLen(2))
		gomega.Expect(snap.Sessions["s1"].Name).To(gomega.Equal("A Task"))
		gomega.Expect(snap.Sessions["s1"].Status).To(gomega.Equal("busy"))
		gomega.Expect(snap.Sessions["s2"].Status).To(gomega.Equal("idle"))
		gomega.Expect(snap.SweptAt).To(gomega.Equal("2026-11-19T10:00:00Z"))
	})

	ginkgo.It("skips an empty session id rather than keying an entry on it", func() {
		prepared := Prepared{
			Tasks: []Task{{Name: "A", SessionIDs: []string{"", "s1"}, Mtime: base}},
		}

		snap := FleetSnapshot(prepared, "x", fleetsnapshot.Snapshot{})

		gomega.Expect(snap.Sessions).To(gomega.HaveLen(1))
		gomega.Expect(snap.Sessions).To(gomega.HaveKey("s1"))
	})
})

var _ = ginkgo.Describe("nextStallCount", func() {
	busy := fleetsnapshot.Entry{Status: "busy", TaskMtime: "2026-11-19T10:00:00Z", StallCount: 2}

	ginkgo.It("increments on consecutive busy sweeps with an unmoved task file", func() {
		gomega.Expect(nextStallCount(busy, busy)).To(gomega.Equal(3))
	})

	ginkgo.It("resets when the task file advanced", func() {
		next := busy
		next.TaskMtime = "2026-11-19T10:05:00Z"
		gomega.Expect(nextStallCount(busy, next)).To(gomega.Equal(0))
	})

	ginkgo.It("resets when the status changed", func() {
		next := busy
		next.Status = "shell"
		gomega.Expect(nextStallCount(busy, next)).To(gomega.Equal(0))
	})

	ginkgo.It("resets when the session is no longer busy or shell", func() {
		// A session that is not busy cannot be stalled — the count must not carry
		// through a waiting/idle sweep and then resume.
		for _, status := range []string{"idle", "waiting", ""} {
			next := busy
			next.Status = status
			gomega.Expect(nextStallCount(busy, next)).To(gomega.Equal(0),
				"status %q must reset the counter", status)
		}
	})
})

var _ = ginkgo.Describe("persistFleetSnapshot", func() {
	ctx := context.Background()

	// The store path is the env override, so each spec drives a temp file rather
	// than the live snapshot.
	var store string
	var stderr *bytes.Buffer

	ginkgo.BeforeEach(func() {
		store = filepath.Join(ginkgo.GinkgoT().TempDir(), "fleet-snapshot.json")
		ginkgo.GinkgoT().Setenv("FLEET_SNAPSHOT_PATH", store)
		stderr = &bytes.Buffer{}
	})

	cfgWith := func() Config { return Config{Now: fixedClock()} }

	preparedFor := func(sessionID, status string) Prepared {
		return Prepared{
			Tasks: []Task{{
				Name:       "A Task",
				SessionIDs: []string{sessionID},
				Mtime:      fixedNow,
			}},
			Registry: map[string]RegistryRecord{sessionID: {Status: status}},
		}
	}

	ginkgo.It("writes the store, then accumulates the counter on the next round", func() {
		// The persist path itself, which the review found untested. Round 1
		// creates the file; round 2 reads it back and increments, which is the
		// whole point of a cross-round counter.
		persistFleetSnapshot(ctx, cfgWith(), preparedFor("s1", "busy"), stderr)

		first, err := fleetsnapshot.Load(ctx, store)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(first.Sessions).To(gomega.HaveKey("s1"))
		gomega.Expect(first.Sessions["s1"].Status).To(gomega.Equal("busy"))
		gomega.Expect(first.Sessions["s1"].StallCount).To(gomega.Equal(0))

		persistFleetSnapshot(ctx, cfgWith(), preparedFor("s1", "busy"), stderr)

		second, err := fleetsnapshot.Load(ctx, store)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(second.Sessions["s1"].StallCount).To(gomega.Equal(1))
	})

	ginkgo.It("keeps another subject's sessions when this subject's tick runs", func() {
		// The store is fleet-wide and the tick is per-subject: a replace would
		// leave only the last subject's sessions and the counter could never
		// accumulate.
		other := preparedFor("s2", "idle")
		other.Tasks[0].Name = "Another Task"
		persistFleetSnapshot(ctx, cfgWith(), other, stderr)
		persistFleetSnapshot(ctx, cfgWith(), preparedFor("s1", "busy"), stderr)

		snap, err := fleetsnapshot.Load(ctx, store)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(snap.Sessions).To(gomega.HaveKey("s1"))
		gomega.Expect(snap.Sessions).To(gomega.HaveKey("s2"), "the other subject's session must survive")
	})

	ginkgo.It("names a failed load on stderr and leaves the corrupt file in place", func() {
		gomega.Expect(os.WriteFile(store, []byte("{not json"), 0o600)).To(gomega.Succeed())

		persistFleetSnapshot(ctx, cfgWith(), preparedFor("s1", "busy"), stderr)

		gomega.Expect(stderr.String()).To(gomega.ContainSubstring("could not persist fleet snapshot"))
		after, err := os.ReadFile(store)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(string(after)).To(gomega.Equal("{not json"))
	})
})
