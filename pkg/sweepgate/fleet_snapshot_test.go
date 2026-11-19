// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
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
