// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fakeChainProbe answers ChainLive with a fixed verdict, and records whether it
// was consulted — a task row must never reach the probe.
type fakeChainProbe struct {
	live  bool
	err   error
	calls int
}

func (f *fakeChainProbe) ChainLive(context.Context, FlaggedRow) (bool, error) {
	f.calls++
	return f.live, f.err
}

var _ = ginkgo.Describe("neverOpenedManager", func() {
	ginkgo.DescribeTable("admits only a goal or topic with no session on the create branch",
		func(row FlaggedRow, want bool) {
			gomega.Expect(neverOpenedManager(row)).To(gomega.Equal(want))
		},
		ginkgo.Entry("goal on create with no session", FlaggedRow{
			Kind: "goal", Liveness: "none", Branch: "create",
		}, true),
		ginkgo.Entry("topic on create with no session", FlaggedRow{
			Kind: "topic", Liveness: "none", Branch: "create",
		}, true),
		ginkgo.Entry("a task is not a manager row", FlaggedRow{
			Kind: "task", Liveness: "none", Branch: "create",
		}, false),
		ginkgo.Entry("a row that already has a session", FlaggedRow{
			Kind: "goal", Liveness: "none", Branch: "create", SessionID: "s1",
		}, false),
		// ⚠️ This entry used to expect FALSE, on the belief that another path opened
		// an `open-manager` row. That path was the fleet round's open leg, removed
		// 2026-11-20. `ResolveBranch` never returns `create` for a goal or topic
		// whose chain is proven dead, so excluding this branch left the wrapper
		// opening no goal or topic at all — measured 2026-11-21: two flagged rows,
		// `branch: open-manager`, `liveness: none`, `session_id: null`, and the tick
		// logged `opened=0 skipped=... not-absent:15` on every cycle.
		ginkgo.Entry("a proven-dead chain reads open-manager, and that IS the never-opened case",
			FlaggedRow{
				Kind: "goal", Liveness: "none", Branch: "open-manager",
			}, true),
		ginkgo.Entry("open-manager with a session is not never-opened", FlaggedRow{
			Kind: "goal", Liveness: "none", Branch: "open-manager", SessionID: "s1",
		}, false),
		ginkgo.Entry("a row the predicate could classify is not never-opened", FlaggedRow{
			Kind: "goal", Liveness: "ABSENT", Branch: "create",
		}, false),
	)
})

var _ = ginkgo.Describe("rowFreshness for a manager row", func() {
	ctx := context.Background()
	goal := FlaggedRow{Kind: "goal", Name: "Some Goal", Liveness: "none", Branch: "create"}

	ginkgo.It("opens when the chain probe reads dead (no live manager)", func() {
		tick := &FlaggedTick{Chain: &fakeChainProbe{live: false}}

		fresh, reason, skip := tick.rowFreshness(ctx, goal)

		gomega.Expect(fresh).To(gomega.BeTrue())
		gomega.Expect(skip).To(gomega.BeFalse())
		gomega.Expect(reason).To(gomega.BeEmpty())
	})

	ginkgo.It("skips as already-live when a live manager claims the goal", func() {
		tick := &FlaggedTick{Chain: &fakeChainProbe{live: true}}

		fresh, reason, skip := tick.rowFreshness(ctx, goal)

		gomega.Expect(fresh).To(gomega.BeFalse())
		gomega.Expect(skip).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.Equal(reasonAlreadyLive))
	})

	ginkgo.It("skips as chain-unreadable when the probe cannot read its store", func() {
		tick := &FlaggedTick{Chain: &fakeChainProbe{err: stderrors.New("registry down")}}

		fresh, reason, skip := tick.rowFreshness(ctx, goal)

		gomega.Expect(fresh).To(gomega.BeFalse())
		gomega.Expect(skip).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.Equal(reasonChainUnreadable))
	})

	ginkgo.It("skips as chain-unreadable when no probe is wired", func() {
		tick := &FlaggedTick{}

		fresh, reason, skip := tick.rowFreshness(ctx, goal)

		gomega.Expect(fresh).To(gomega.BeFalse())
		gomega.Expect(skip).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.Equal(reasonChainUnreadable))
	})

	ginkgo.It("does not consult the probe for a task row", func() {
		chain := &fakeChainProbe{live: true}
		tick := &FlaggedTick{Chain: chain}

		fresh, _, skip := tick.rowFreshness(ctx, FlaggedRow{
			Kind: "task", Name: "Some Task", Liveness: "none", Branch: "create",
		})

		gomega.Expect(fresh).To(gomega.BeTrue())
		gomega.Expect(skip).To(gomega.BeFalse())
		gomega.Expect(chain.calls).To(gomega.Equal(0))
	})
})

var _ = ginkgo.Describe("registry chain probe", func() {
	ctx := context.Background()
	row := FlaggedRow{Kind: "goal", Name: "Some Goal"}

	ginkgo.It("reports live when a live entry carries the row's tab title", func() {
		probe := NewRegistryChainProbe(&fakeFleetRegistry{
			entries: []RegistryEntry{{Name: TabNameFor("Some Goal"), SessionID: "s1"}},
		})

		live, err := probe.ChainLive(ctx, row)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.BeTrue())
	})

	ginkgo.It("reports live when the tab title carries a collision suffix", func() {
		probe := NewRegistryChainProbe(&fakeFleetRegistry{
			entries: []RegistryEntry{{Name: TabNameFor("Some Goal") + " (2)", SessionID: "s1"}},
		})

		live, err := probe.ChainLive(ctx, row)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.BeTrue())
	})

	ginkgo.It("reports live when the open endpoint named the tab `<name> Manager`", func() {
		probe := NewRegistryChainProbe(&fakeFleetRegistry{
			entries: []RegistryEntry{{Name: "Some Goal Manager", SessionID: "s1"}},
		})

		live, err := probe.ChainLive(ctx, row)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.BeTrue())
	})

	ginkgo.It("reports not-live when no live entry matches", func() {
		probe := NewRegistryChainProbe(&fakeFleetRegistry{
			entries: []RegistryEntry{{Name: TabNameFor("Another Row"), SessionID: "s2"}},
		})

		live, err := probe.ChainLive(ctx, row)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.BeFalse())
	})

	ginkgo.It("returns the registry error rather than a false negative", func() {
		probe := NewRegistryChainProbe(&fakeFleetRegistry{err: stderrors.New("registry down")})

		_, err := probe.ChainLive(ctx, row)

		gomega.Expect(err).ToNot(gomega.BeNil())
	})
})
