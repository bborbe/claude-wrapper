// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fixedNow is the literal clock every classification spec pins. No spec reads the
// wall clock.
var fixedNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.Local)

var _ = ginkgo.Describe("classifies every bucket in the reference order", func() {
	emptyHolds := map[string]Hold{}

	type testCase struct {
		task  Task
		label string
		key   string
	}

	cases := []testCase{
		{Task{Status: "completed"}, "✅ done", "done"},
		{Task{Status: "aborted"}, "✅ done · aborted", "done"},
		{Task{Status: "hold"}, "⏸️ blocked/hold", "hold"},
		{Task{Status: "backlog"}, "—", "backlog"},
		{Task{Status: "in_progress", Phase: "human_review"},
			"⌛ waiting-on-human", "waiting"},
		{Task{Status: "in_progress", Liveness: LivenessParked},
			"⌛ waiting-on-human", "waiting"},
		{Task{Status: "in_progress", Phase: "todo"}, "⌛ waiting-approval", "approval"},
		{Task{Status: "in_progress", ExternalWait: true},
			"⌛ waiting-external", "external"},
		{Task{Status: "in_progress", InFlight: true}, "🔄 progressing", "progressing"},
		{Task{Status: "in_progress", DeclaredWaitActive: true},
			"⌛ waiting-declared", "declared"},
		{Task{Status: "in_progress", UnregisteredPark: true},
			"⌛ parked-on-unregistered-gate", "unregistered"},
		{Task{Status: "in_progress", Phase: "execution", Role: "human"},
			"👤 YOURS", "yours"},
		{Task{Status: "in_progress", Phase: "execution", Stuck: true},
			"⚠️ problem", "problem"},
		{Task{Status: "next", Liveness: LivenessLive}, "🔄 progressing", "progressing"},
		{Task{Status: "next", BlockedUpstream: true},
			"⏸️ blocked/upstream", "upstream"},
		{Task{Status: "next"}, "🚀 ready-to-start", "ready"},
		{Task{Status: "in_progress", Phase: "execution"},
			"🔄 progressing", "progressing"},
		{Task{Status: "weird", Phase: "unknown"}, "—", "other"},
	}

	for _, tc := range cases {
		tc := tc
		ginkgo.It("classifies "+tc.key+" "+tc.label, func() {
			label, key := Bucket(fixedNow, emptyHolds, tc.task)
			gomega.Expect(label).To(gomega.Equal(tc.label))
			gomega.Expect(key).To(gomega.Equal(tc.key))
		})
	}

	ginkgo.It("labels an operator-flagged todo as ordinary work", func() {
		task := Task{Status: "in_progress", Phase: "todo", OperatorFlagged: true}
		label, key := Bucket(fixedNow, emptyHolds, task)
		gomega.Expect(label).To(gomega.Equal("🔄 progressing"))
		gomega.Expect(key).To(gomega.Equal("progressing"))
	})
})

var _ = ginkgo.Describe("renders the held overlay with reason and age", func() {
	held := func(age time.Duration, stamp string) map[string]Hold {
		heldAt := stamp
		if stamp == "" {
			heldAt = fixedNow.Add(-age).UTC().Format(heldAtFormat)
		}
		return map[string]Hold{"held-session": {Reason: "operator parked it", HeldAt: heldAt}}
	}

	ginkgo.It("renders a hold under an hour in minutes", func() {
		label, key := Bucket(fixedNow, held(12*time.Minute, ""),
			Task{Status: "in_progress", Session: "held-session"})
		gomega.Expect(label).To(gomega.Equal("⏸️ HELD — operator parked it · 12m"))
		gomega.Expect(key).To(gomega.Equal("progressing"))
	})

	ginkgo.It("renders a hold under a day in hours", func() {
		label, _ := Bucket(fixedNow, held(4*time.Hour, ""),
			Task{Status: "in_progress", Session: "held-session"})
		gomega.Expect(label).To(gomega.Equal("⏸️ HELD — operator parked it · 4h"))
	})

	ginkgo.It("renders a hold of days in days", func() {
		label, _ := Bucket(fixedNow, held(72*time.Hour, ""),
			Task{Status: "in_progress", Session: "held-session"})
		gomega.Expect(label).To(gomega.Equal("⏸️ HELD — operator parked it · 3d"))
	})

	ginkgo.It("renders an unreadable stamp as a question mark", func() {
		label, _ := Bucket(fixedNow, held(0, "not-a-timestamp"),
			Task{Status: "in_progress", Session: "held-session"})
		gomega.Expect(label).To(gomega.Equal("⏸️ HELD — operator parked it · ?"))
	})

	ginkgo.It("leaves an unheld control row alone", func() {
		label, key := Bucket(fixedNow, map[string]Hold{},
			Task{Status: "completed", Session: "not-held"})
		gomega.Expect(label).To(gomega.Equal("✅ done"))
		gomega.Expect(key).To(gomega.Equal("done"))
	})
})

var _ = ginkgo.Describe("keeps the digest blind to the render-only flags", func() {
	ginkgo.It("does not move when every render-only flag is set", func() {
		base := []Task{{
			Name: "Alpha", Status: "in_progress", Phase: "execution",
			Session: "session-alpha", ProgressHash: "progress-alpha",
		}}
		flagged := []Task{{
			Name: "Alpha", Status: "in_progress", Phase: "execution",
			Session: "session-alpha", ProgressHash: "progress-alpha",
			InFlight: true, ExternalWait: true, DeclaredWaitActive: true,
			UnregisteredPark: true, FeedGate: true, BlockedUpstream: true,
			BlockedByUnmet: []string{"Blocker"},
		}}
		gomega.Expect(Digest(flagged)).To(gomega.Equal(Digest(base)))
	})
})

var _ = ginkgo.Describe("detects a row held from outside", func() {
	ginkgo.It("treats parked, blocked, deferred and declared as waiting", func() {
		gomega.Expect(IsWaiting(fixedNow, Task{Liveness: LivenessParked})).To(gomega.BeTrue())
		gomega.Expect(IsWaiting(fixedNow, Task{BlockedUpstream: true})).To(gomega.BeTrue())
		gomega.Expect(IsWaiting(fixedNow, Task{DeferDate: "2099-01-01"})).
			To(gomega.BeTrue())
		gomega.Expect(IsWaiting(fixedNow, Task{DeclaredWaitActive: true})).
			To(gomega.BeTrue())
		gomega.Expect(IsWaiting(fixedNow, Task{})).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("refuses", func() {
	emptyHolds := map[string]Hold{}

	ginkgo.It("refuses a non-live owner the ready offer", func() {
		// A live owner is on the row; the offer belongs to an unowned row.
		label, key := Bucket(fixedNow, emptyHolds,
			Task{Status: "next", Liveness: LivenessLive})
		gomega.Expect(label).To(gomega.Equal("🔄 progressing"))
		gomega.Expect(key).To(gomega.Equal("progressing"))
	})

	ginkgo.It("refuses a spawn offer to a non-spawnable role", func() {
		label, key := Bucket(fixedNow, emptyHolds,
			Task{Status: "next", Phase: "execution", Role: "manager"})
		gomega.Expect(label).To(gomega.Equal("👤 YOURS"))
		gomega.Expect(key).To(gomega.Equal("yours"))
	})

	ginkgo.It("refuses the problem cell to a waiting row", func() {
		label, key := Bucket(fixedNow, emptyHolds,
			Task{Status: "in_progress", Phase: "execution", Stuck: true,
				Liveness: LivenessParked})
		gomega.Expect(label).To(gomega.Equal("⌛ waiting-on-human"))
		gomega.Expect(key).To(gomega.Equal("waiting"))
	})

	ginkgo.It("refuses the deferred overlay to a definitive disposition", func() {
		label, key := Bucket(fixedNow, emptyHolds,
			Task{Status: "completed", DeferDate: "2099-01-01"})
		gomega.Expect(label).To(gomega.Equal("✅ done"))
		gomega.Expect(key).To(gomega.Equal("done"))
	})

	ginkgo.It("refuses an action to a held row", func() {
		holds := map[string]Hold{"held-session": {
			Reason: "operator parked it",
			HeldAt: fixedNow.Add(-time.Hour).UTC().Format(heldAtFormat),
		}}
		label, key := Bucket(fixedNow, holds,
			Task{Status: "next", Session: "held-session"})
		gomega.Expect(label).To(gomega.Equal("⏸️ HELD — operator parked it · 1h"))
		gomega.Expect(key).To(gomega.Equal("ready"))
	})

	ginkgo.It("refuses a spawn offer to an unapproved row", func() {
		label, key := Bucket(fixedNow, emptyHolds,
			Task{Status: "next", Phase: "todo"})
		gomega.Expect(label).To(gomega.Equal("⌛ waiting-approval"))
		gomega.Expect(key).To(gomega.Equal("approval"))
	})
})
