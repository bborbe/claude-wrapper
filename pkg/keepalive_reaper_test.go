// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// reaperSupervisor is a Supervisor that records Stop calls and returns a fixed
// session list. The embedded interface satisfies the rest of the contract.
type reaperSupervisor struct {
	Supervisor
	sessions []Session
	stopped  []string
}

func (s *reaperSupervisor) List(context.Context) ([]Session, error) { return s.sessions, nil }

func (s *reaperSupervisor) Stop(_ context.Context, id string) error {
	s.stopped = append(s.stopped, id)
	return nil
}

var _ = ginkgo.Describe("ReaperJob", func() {
	ctx := context.Background()

	ginkgo.It("renders one job=reaper line per result, closed and kept", func() {
		lines := &[]string{}
		job := &ReaperJob{
			Reapers: []Reaper{stubReaper{kind: "manager-reap", results: []ReaperResult{
				{Kind: "manager-reap", SessionID: "s1", Closed: true},
				{Kind: "manager-reap", SessionID: "s2", Reason: "live"},
			}}},
			Logf: func(line string) { *lines = append(*lines, line) },
		}

		job.TickAndLog(ctx)

		gomega.Expect(*lines).To(gomega.Equal([]string{
			"job=reaper action=manager-reap result=closed id=s1",
			"job=reaper action=manager-reap result=kept id=s2 reason=live",
		}))
	})
})

// stubReaper returns a fixed set of results.
type stubReaper struct {
	kind    string
	results []ReaperResult
}

func (r stubReaper) Kind() string { return r.kind }

func (r stubReaper) Reap(context.Context) ([]ReaperResult, error) { return r.results, nil }

var _ = ginkgo.Describe("livenessReaper", func() {
	ctx := context.Background()

	verdicts := func(vs ...LivenessVerdict) func(context.Context) ([]LivenessVerdict, error) {
		return func(context.Context) ([]LivenessVerdict, error) { return vs, nil }
	}
	roles := func(m map[string]string) RoleFor {
		return func(_ context.Context, id string) (string, error) { return m[id], nil }
	}

	ginkgo.It("manager-reap closes a not-live child-table manager and skips a live one", func() {
		sup := &reaperSupervisor{}
		reaper := NewManagerReapReaper(
			verdicts(
				LivenessVerdict{SessionID: "dead-mgr", Liveness: LivenessNotLive, Source: "child-table"},
				LivenessVerdict{SessionID: "live-mgr", Liveness: LivenessLive, Source: "registry"},
			),
			roles(map[string]string{"dead-mgr": "manager", "live-mgr": "manager"}),
			sup,
		)

		results, err := reaper.Reap(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(sup.stopped).To(gomega.Equal([]string{"dead-mgr"}))
		gomega.Expect(results).To(gomega.Equal([]ReaperResult{
			{Kind: "manager-reap", SessionID: "dead-mgr", Closed: true},
		}))
	})

	ginkgo.It("manager-reap never closes a worker role, drive-reap never closes a manager", func() {
		sup := &reaperSupervisor{}
		v := verdicts(LivenessVerdict{SessionID: "dead-worker", Liveness: LivenessNotLive, Source: "child-table"})
		rolesFor := roles(map[string]string{"dead-worker": "worker"})

		mgr, _ := NewManagerReapReaper(v, rolesFor, sup).Reap(ctx)
		drv, _ := NewDriveReapReaper(v, rolesFor, sup).Reap(ctx)

		gomega.Expect(mgr).To(gomega.BeEmpty())
		gomega.Expect(drv).To(gomega.HaveLen(1))
		gomega.Expect(drv[0].Kind).To(gomega.Equal("drive-reap"))
		// ⚠️ The KIND alone is not the act: a reaper that returns a
		// `{Kind: "drive-reap", Closed: true}` result without calling Stop
		// satisfies every line assertion above and closes nothing. Assert the
		// CALL, and assert it is drive-reap's own — the manager reap ran first
		// on the same supervisor and stopped nothing, so this id can only come
		// from the drive reap.
		gomega.Expect(sup.stopped).To(gomega.Equal([]string{"dead-worker"}))
	})

	// A not-live session from a source OTHER than child-table is not a close
	// candidate — the daemon may simply not reach it.
	ginkgo.It("skips a not-live session whose source is not child-table", func() {
		sup := &reaperSupervisor{}
		reaper := NewManagerReapReaper(
			verdicts(LivenessVerdict{SessionID: "x", Liveness: LivenessNotLive, Source: "registry"}),
			roles(map[string]string{"x": "manager"}),
			sup,
		)

		results, _ := reaper.Reap(ctx)

		gomega.Expect(results).To(gomega.BeEmpty())
		gomega.Expect(sup.stopped).To(gomega.BeEmpty())
	})
})

// stubIdleTicker returns a fixed IdleReaperResult.
type stubIdleTicker struct{ closed []string }

func (s stubIdleTicker) Tick(context.Context) (IdleReaperResult, error) {
	return IdleReaperResult{Closed: s.closed}, nil
}

var _ = ginkgo.Describe("IdleCloseReaper", func() {
	ctx := context.Background()

	// The fifth per-kind fixture SC3(a) requires. It stubs the idle tick rather
	// than driving the idle decision, because the decision needs a fully
	// populated Session fake (an earlier attempt panicked at idle_reaper.go:175).
	ginkgo.It("re-labels each closed id as an idle-close result", func() {
		reaper := &IdleCloseReaper{Job: stubIdleTicker{closed: []string{"s1", "s2"}}}

		results, err := reaper.Reap(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(results).To(gomega.Equal([]ReaperResult{
			{Kind: "idle-close", SessionID: "s1", Closed: true},
			{Kind: "idle-close", SessionID: "s2", Closed: true},
		}))
	})

	ginkgo.It("reports nothing when the idle tick closes nothing", func() {
		reaper := &IdleCloseReaper{Job: stubIdleTicker{}}

		results, _ := reaper.Reap(ctx)

		gomega.Expect(results).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("StoodDownCloseReaper", func() {
	ctx := context.Background()

	ginkgo.It("closes each id the stood-down join returns", func() {
		sup := &reaperSupervisor{}
		reaper := &StoodDownCloseReaper{
			Supervisor: sup,
			Candidates: func(context.Context) ([]StoodDownCandidate, error) {
				return []StoodDownCandidate{{Slug: "goal--g", Base: "g"}}, nil
			},
			SessionFor: func(context.Context, string) (string, error) { return "s1", nil },
			Live:       func(context.Context, string) (bool, error) { return true, nil },
		}

		results, err := reaper.Reap(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(sup.stopped).To(gomega.Equal([]string{"s1"}))
		gomega.Expect(results).To(gomega.Equal([]ReaperResult{
			{Kind: "stood-down-close", SessionID: "s1", Closed: true},
		}))
	})
})

// stubResolver returns a fixed AnchoredTask for every session.
type stubResolver struct{ task AnchoredTask }

func (r stubResolver) Resolve(context.Context, string) (AnchoredTask, error) { return r.task, nil }

var _ = ginkgo.Describe("TerminalTaskReaper", func() {
	ctx := context.Background()

	ginkgo.It("closes a session whose anchored task is completed", func() {
		sup := &reaperSupervisor{sessions: []Session{{ID: "s1"}}}
		reaper := &TerminalTaskReaper{
			Supervisor: sup,
			Resolver:   stubResolver{task: AnchoredTask{Status: "completed", Matches: 1}},
		}

		results, err := reaper.Reap(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(sup.stopped).To(gomega.Equal([]string{"s1"}))
		gomega.Expect(results).To(gomega.Equal([]ReaperResult{
			{Kind: "terminal-task", SessionID: "s1", Closed: true},
		}))
	})

	// A non-terminal task is not notable — no line, no close.
	ginkgo.It("leaves a session whose task is in progress alone", func() {
		sup := &reaperSupervisor{sessions: []Session{{ID: "s1"}}}
		reaper := &TerminalTaskReaper{
			Supervisor: sup,
			Resolver:   stubResolver{task: AnchoredTask{Status: "in_progress", Matches: 1}},
		}

		results, _ := reaper.Reap(ctx)

		gomega.Expect(results).To(gomega.BeEmpty())
		gomega.Expect(sup.stopped).To(gomega.BeEmpty())
	})
})
