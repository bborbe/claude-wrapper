// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ResolveBranch", func() {
	yes := true
	no := false

	ginkgo.It("reads degraded when the probe failed but the registry named an owner", func() {
		// The one exception to "unknown outranks everything": the registry proved the
		// process alive, so the owner IS known and throwing it away is the dead end.
		branch, _ := ResolveBranch(BranchInput{
			Live: "UNKNOWN", RegistryOK: true, Owner: "Some Goal",
		})
		gomega.Expect(branch).To(gomega.Equal("degraded"))
	})

	ginkgo.It("reads unknown when the registry could not be read", func() {
		// No registry means the owner is NOT known, so degraded would be a guess.
		for _, live := range []string{"UNKNOWN", "AMBIGUOUS", "MALFORMED"} {
			branch, _ := ResolveBranch(BranchInput{Live: live})
			gomega.Expect(branch).To(gomega.Equal("unknown"), "live=%s", live)
		}
	})

	ginkgo.It("reads direct for a human row before any task check", func() {
		branch, _ := ResolveBranch(BranchInput{Live: "ABSENT", Role: "human", Kind: "task", BlockRole: "manager"})
		gomega.Expect(branch).To(gomega.Equal("direct"))
	})

	ginkgo.It("refuses a manager-owned task row", func() {
		branch, refused := ResolveBranch(BranchInput{Kind: "task", BlockRole: "manager", Live: "ABSENT"})
		gomega.Expect(branch).To(gomega.Equal("skip"))
		gomega.Expect(refused).To(gomega.Equal([]string{RefusalManagerRow}))
	})

	ginkgo.It("opens a manager only on a PROVEN dead chain, never on an unanswerable one", func() {
		// nil is "could not answer" and must not fire open-manager — that is the
		// duplicate-spawn hazard the whole branch exists to avoid.
		proven, _ := ResolveBranch(BranchInput{Kind: "goal", ChainLive: &no})
		gomega.Expect(proven).To(gomega.Equal("open-manager"))

		unanswerable, _ := ResolveBranch(BranchInput{Kind: "goal", ChainLive: nil})
		gomega.Expect(unanswerable).To(gomega.Equal("create"))

		alive, _ := ResolveBranch(BranchInput{Kind: "topic", ChainLive: &yes})
		gomega.Expect(alive).To(gomega.Equal("create"))
	})

	ginkgo.It("jumps a live row with a pane and skips one without", func() {
		jump, _ := ResolveBranch(BranchInput{Live: "LIVE", Pane: "42"})
		gomega.Expect(jump).To(gomega.Equal("jump"))

		skip, _ := ResolveBranch(BranchInput{Live: "LIVE"})
		gomega.Expect(skip).To(gomega.Equal("skip"))
	})

	ginkgo.It("keeps a LIVE row open even when it declared a wait", func() {
		// parked sits AFTER every LIVE branch: a live row is open, and folding it into
		// parked would drop an existing session out of coverage's numerator.
		branch, _ := ResolveBranch(BranchInput{Live: "LIVE", Pane: "7", Parked: true})
		gomega.Expect(branch).To(gomega.Equal("jump"))
	})

	ginkgo.It("parks an ABSENT row with a declared wait instead of resuming it", func() {
		// This is what stops a 100% target driving the fleet back into resuming idle
		// workers on every tick.
		branch, _ := ResolveBranch(BranchInput{Live: "ABSENT", SessionID: "s1", Parked: true})
		gomega.Expect(branch).To(gomega.Equal("parked"))
	})

	ginkgo.It("resumes an ABSENT row that has a session id", func() {
		branch, _ := ResolveBranch(BranchInput{Live: "ABSENT", SessionID: "s1"})
		gomega.Expect(branch).To(gomega.Equal("resume"))
	})

	ginkgo.It("does not resume an ABSENT row with no session id", func() {
		branch, _ := ResolveBranch(BranchInput{Live: "ABSENT"})
		gomega.Expect(branch).To(gomega.Equal("create"))
	})

	ginkgo.It("holds a task whose owner is live, but never on the fleet sentinel", func() {
		// `Live: "none"`, not ABSENT+session: `resume` precedes this branch, so a task
		// with its own absent-but-recorded session resumes and never reaches the hold.
		// held-owner-live only catches a row that would otherwise CREATE.
		held, _ := ResolveBranch(BranchInput{
			Kind: "task", Live: "none", Owner: "Some Goal", OwnerLive: &yes,
		})
		gomega.Expect(held).To(gomega.Equal("held-owner-live"))

		// The sentinel owns every task whose goal and topic have no live manager;
		// holding on it would suppress the whole batch.
		fleet, _ := ResolveBranch(BranchInput{
			Kind: "task", Live: "none", Owner: FleetOwner, OwnerLive: &yes,
		})
		gomega.Expect(fleet).To(gomega.Equal("create"))

		// An unanswerable owner probe must not suppress work.
		unknown, _ := ResolveBranch(BranchInput{
			Kind: "task", Live: "none", Owner: "Some Goal", OwnerLive: nil,
		})
		gomega.Expect(unknown).To(gomega.Equal("create"))
	})

	ginkgo.It("creates when nothing else matched", func() {
		branch, refused := ResolveBranch(BranchInput{Live: "none"})
		gomega.Expect(branch).To(gomega.Equal("create"))
		gomega.Expect(refused).To(gomega.BeEmpty())
	})
})
