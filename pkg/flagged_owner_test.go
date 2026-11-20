// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// ownerCtx builds an OwnerContext from `kind/name` → answer maps, so each spec
// states only the answers it cares about.
func ownerCtx(
	topicOf map[string]string,
	live map[string]*bool,
	liveness map[string]*bool,
	fleet *bool,
) OwnerContext {
	return OwnerContext{
		TopicOf: topicOf,
		SubjectLive: func(kind, name string) *bool {
			return live[kind+"/"+name]
		},
		SubjectLiveness: func(kind, name string) *bool {
			return liveness[kind+"/"+name]
		},
		FleetLive: fleet,
	}
}

var _ = ginkgo.Describe("ResolveOwner", func() {
	yes := true
	no := false

	ginkgo.It("keeps a topic as its own owner when its manager is live", func() {
		owner := ResolveOwner("topic", "Fleet Ops", "", ownerCtx(nil, map[string]*bool{"topic/Fleet Ops": &yes}, nil, nil))
		gomega.Expect(owner).To(gomega.Equal(Owner{Kind: "topic", Name: "Fleet Ops"}))
	})

	ginkgo.It("falls to the fleet sentinel only on a PROVEN dead topic", func() {
		// nil is "could not answer" and must NOT demote — that is the
		// duplicate-spawn hazard: a failed registry read would otherwise prove that
		// no manager is live.
		dead := ResolveOwner("topic", "Fleet Ops", "", ownerCtx(nil, map[string]*bool{"topic/Fleet Ops": &no}, nil, nil))
		gomega.Expect(dead.IsFleet()).To(gomega.BeTrue())

		unanswerable := ResolveOwner("topic", "Fleet Ops", "", ownerCtx(nil, map[string]*bool{"topic/Fleet Ops": nil}, nil, nil))
		gomega.Expect(unanswerable).To(gomega.Equal(Owner{Kind: "topic", Name: "Fleet Ops"}))
	})

	ginkgo.It("keeps a goal as its own owner when its manager is live", func() {
		owner := ResolveOwner("goal", "Ship It", "", ownerCtx(nil, map[string]*bool{"goal/Ship It": &yes}, nil, nil))
		gomega.Expect(owner).To(gomega.Equal(Owner{Kind: "goal", Name: "Ship It"}))
	})

	ginkgo.It("lifts an unmanaged goal to its declaring topic", func() {
		// Tier 4 exists so the topic manager can START that goal's manager.
		owner := ResolveOwner("goal", "Ship It", "",
			ownerCtx(map[string]string{"Ship It": "Fleet Ops"},
				map[string]*bool{"goal/Ship It": &no, "topic/Fleet Ops": &yes}, nil, nil))
		gomega.Expect(owner).To(gomega.Equal(Owner{Kind: "topic", Name: "Fleet Ops"}))
	})

	ginkgo.It("gives an unmanaged goal with no topic to the fleet", func() {
		owner := ResolveOwner("goal", "Ship It", "", ownerCtx(nil, map[string]*bool{"goal/Ship It": &no}, nil, nil))
		gomega.Expect(owner.IsFleet()).To(gomega.BeTrue())
	})

	ginkgo.It("resolves a task through managed_by before its goals", func() {
		block := "managed_by: [[Explicit Goal]]\ngoals: [[Other Goal]]\n"
		owner := ResolveOwner("task", "Do It", block,
			ownerCtx(nil, map[string]*bool{"goal/Explicit Goal": &yes, "goal/Other Goal": &yes}, nil, nil))
		gomega.Expect(owner).To(gomega.Equal(Owner{Kind: "goal", Name: "Explicit Goal"}))
	})

	ginkgo.It("unwraps an aliased or anchored managed_by the same way goals are unwrapped", func() {
		// Left on the value, the alias would name no page and send the row to the
		// fleet for a reason nobody could see.
		for _, value := range []string{"[[Explicit Goal|the goal]]", "[[Explicit Goal#Section]]"} {
			block := "managed_by: \"" + value + "\"\n"
			owner := ResolveOwner("task", "Do It", block,
				ownerCtx(nil, map[string]*bool{"goal/Explicit Goal": &yes}, nil, nil))
			gomega.Expect(owner).To(gomega.Equal(Owner{Kind: "goal", Name: "Explicit Goal"}), "value=%s", value)
		}
	})

	ginkgo.It("walks a task's goals in order and takes the first live one", func() {
		block := "goals:\n  - \"[[Dead Goal]]\"\n  - \"[[Live Goal]]\"\n"
		owner := ResolveOwner("task", "Do It", block,
			ownerCtx(nil, map[string]*bool{"goal/Dead Goal": &no, "goal/Live Goal": &yes}, nil, nil))
		gomega.Expect(owner).To(gomega.Equal(Owner{Kind: "goal", Name: "Live Goal"}))
	})

	ginkgo.It("lifts a goal-less task to its own topic", func() {
		// A task named directly in a topic's ## Goals has no `goals:` of its own.
		owner := ResolveOwner("task", "Do It", "",
			ownerCtx(map[string]string{"Do It": "Fleet Ops"},
				map[string]*bool{"topic/Fleet Ops": &yes}, nil, nil))
		gomega.Expect(owner).To(gomega.Equal(Owner{Kind: "topic", Name: "Fleet Ops"}))
	})

	ginkgo.It("gives a task with no live owner anywhere to the fleet", func() {
		block := "goals: [[Dead Goal]]\n"
		owner := ResolveOwner("task", "Do It", block,
			ownerCtx(nil, map[string]*bool{"goal/Dead Goal": &no}, nil, nil))
		gomega.Expect(owner.IsFleet()).To(gomega.BeTrue())
	})

	ginkgo.It("never treats a task's topic as a rung for a WORKER", func() {
		// The topic rung only fires when the topic is the task's OWN declarer; a
		// goal's topic is never consulted for a worker.
		block := "goals: [[Dead Goal]]\n"
		owner := ResolveOwner("task", "Do It", block,
			ownerCtx(map[string]string{"Dead Goal": "Fleet Ops"},
				map[string]*bool{"goal/Dead Goal": &no, "topic/Fleet Ops": &yes}, nil, nil))
		gomega.Expect(owner.IsFleet()).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("OwnerLiveness", func() {
	yes := true
	no := false

	ginkgo.It("reads a resolved subject's own liveness and a PROVEN live chain", func() {
		ownerLive, chainLive := OwnerLiveness(
			Owner{Kind: "goal", Name: "Ship It"}, "task", "Do It",
			ownerCtx(nil, map[string]*bool{"goal/Ship It": &yes},
				map[string]*bool{"goal/Ship It": &yes}, &yes))
		gomega.Expect(ownerLive).To(gomega.Equal(&yes))
		gomega.Expect(chainLive).To(gomega.Equal(&yes))
	})

	ginkgo.It("does not claim a live chain for an owner kept only because the probe could not answer", func() {
		// ResolveOwner keeps a subject when SubjectLive is nil, so reporting a
		// proven-true chain here would assert a liveness nobody measured. nil does
		// not fire open-manager either, so the safe direction is unchanged.
		ownerLive, chainLive := OwnerLiveness(
			Owner{Kind: "goal", Name: "Ship It"}, "task", "Do It",
			ownerCtx(nil, map[string]*bool{"goal/Ship It": nil}, nil, &yes))
		gomega.Expect(ownerLive).To(gomega.BeNil())
		gomega.Expect(chainLive).To(gomega.BeNil())
	})

	ginkgo.It("reads the fleet sentinel's liveness and the subject's chain", func() {
		ownerLive, chainLive := OwnerLiveness(
			FleetOwnerRef, "goal", "Ship It",
			ownerCtx(nil, map[string]*bool{"goal/Ship It": &yes}, nil, &no))
		gomega.Expect(ownerLive).To(gomega.Equal(&no))
		gomega.Expect(chainLive).To(gomega.Equal(&yes))
	})

	ginkgo.It("falls back to the declaring topic when the goal's own chain is dead", func() {
		_, chainLive := OwnerLiveness(
			FleetOwnerRef, "goal", "Ship It",
			ownerCtx(map[string]string{"Ship It": "Fleet Ops"},
				map[string]*bool{"goal/Ship It": &no, "topic/Fleet Ops": &yes}, nil, &yes))
		gomega.Expect(chainLive).To(gomega.Equal(&yes))
	})

	ginkgo.It("reports a dead chain for an unmanaged goal with no topic", func() {
		_, chainLive := OwnerLiveness(
			FleetOwnerRef, "goal", "Ship It",
			ownerCtx(nil, map[string]*bool{"goal/Ship It": &no}, nil, &yes))
		gomega.Expect(chainLive).To(gomega.Equal(&no))
	})

	ginkgo.It("leaves an unanswerable chain nil so open-manager cannot fire", func() {
		_, chainLive := OwnerLiveness(
			FleetOwnerRef, "goal", "Ship It",
			ownerCtx(nil, map[string]*bool{"goal/Ship It": nil}, nil, &yes))
		gomega.Expect(chainLive).To(gomega.BeNil())
	})

	ginkgo.It("leaves a task's chain unresolved — the sentinel owns it", func() {
		ownerLive, chainLive := OwnerLiveness(
			FleetOwnerRef, "task", "Do It",
			ownerCtx(nil, nil, nil, &yes))
		gomega.Expect(ownerLive).To(gomega.Equal(&yes))
		gomega.Expect(chainLive).To(gomega.BeNil())
	})
})
