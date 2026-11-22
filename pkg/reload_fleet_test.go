// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// reloadRegistry is a SessionRegistry with a per-session Status.
type reloadRegistry struct {
	entries []RegistryEntry
	status  map[string]*string
	liveErr error
}

func (f *reloadRegistry) Live(context.Context) ([]RegistryEntry, error) {
	return f.entries, f.liveErr
}

func (f *reloadRegistry) Entries(context.Context) ([]RegistryEntry, error) {
	return f.entries, f.liveErr
}

func (f *reloadRegistry) Status(_ context.Context, id string) (*string, error) {
	return f.status[id], nil
}

func (f *reloadRegistry) Cwd(context.Context, string) (*string, error) { return nil, nil }

func (f *reloadRegistry) Named(context.Context, string) ([]string, error) { return nil, nil }

func (f *reloadRegistry) NamesForSession(context.Context, string) ([]string, error) {
	return nil, nil
}

// reloadSlash is a SlashRunner that returns a per-session error and counts calls.
type reloadSlash struct {
	errByID map[string]error
	calls   map[string]int
}

func (f *reloadSlash) Run(_ context.Context, id, _ string) error {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[id]++
	return f.errByID[id]
}

// reloadMarkers returns `before` on the first Newest call, then `after`.
type reloadMarkers struct {
	before, after       time.Time
	hasBefore, hasAfter bool
	calls               int
}

func (f *reloadMarkers) Newest(int) (time.Time, bool) {
	f.calls++
	if f.calls == 1 {
		return f.before, f.hasBefore
	}
	return f.after, f.hasAfter
}

// reloadRoster is a Roster with a fixed row set.
type reloadRoster struct {
	rows []AgentRow
}

func (f *reloadRoster) List(context.Context) ([]AgentRow, error) { return f.rows, nil }

func (f *reloadRoster) Get(context.Context, string) (*AgentRow, error) {
	return nil, nil
}

func statusPtr(s string) *string { return &s }

func reloadDeps(registry *reloadRegistry, roster *reloadRoster, slash *reloadSlash, markers *reloadMarkers) ReloadFleetDeps {
	return ReloadFleetDeps{
		Registry:      registry,
		Roster:        roster,
		Slash:         slash,
		Markers:       markers,
		SettleTimeout: 10 * time.Millisecond,
		PollInterval:  time.Millisecond,
	}
}

var _ = ginkgo.Describe("ReloadFleet", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("reports reloaded when delivery succeeds and the marker moves", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{{PID: 10, SessionID: "s1"}}}
		slash := &reloadSlash{}
		markers := &reloadMarkers{
			before: time.Unix(100, 0), after: time.Unix(200, 0),
			hasBefore: true, hasAfter: true,
		}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, slash, markers), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results).To(gomega.HaveLen(1))
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeReloaded))
	})

	ginkgo.It("reports refused when the composer never became ready", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{{PID: 10, SessionID: "s1"}}}
		slash := &reloadSlash{errByID: map[string]error{
			"s1": errors.New("pane 5 showed no empty composer within 8s, so nothing was typed"),
		}}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{hasBefore: true}), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeRefused))
	})

	ginkgo.It("reports busy for a mid-turn status without delivering", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}},
			status:  map[string]*string{"s1": statusPtr("busy")},
		}
		slash := &reloadSlash{}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeBusy))
		gomega.Expect(slash.calls["s1"]).To(gomega.BeZero(), "a busy session must never be typed into")
	})

	ginkgo.It("reports busy when the lock is held (running a sequence)", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{{PID: 10, SessionID: "s1"}}}
		slash := &reloadSlash{errByID: map[string]error{
			"s1": errors.New(`session "s1" is running a sequence`),
		}}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{hasBefore: true}), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeBusy))
	})

	ginkgo.It("reports no-pane for a shell session", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}},
			status:  map[string]*string{"s1": statusPtr("shell")},
		}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, &reloadSlash{}, &reloadMarkers{}), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeNoPane))
	})

	ginkgo.It("reports no-pane when delivery cannot resolve a pane", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{{PID: 10, SessionID: "s1"}}}
		slash := &reloadSlash{errByID: map[string]error{
			"s1": errors.New("agent s1 is not a local tab or headless worker of this daemon"),
		}}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{hasBefore: true}), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeNoPane))
	})

	ginkgo.It("reports unverified when there is no marker before delivery", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{{PID: 10, SessionID: "s1"}}}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, &reloadSlash{}, &reloadMarkers{}), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeUnverified))
	})

	ginkgo.It("reports unverified when the marker does not move", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{{PID: 10, SessionID: "s1"}}}
		markers := &reloadMarkers{
			before: time.Unix(100, 0), after: time.Unix(100, 0),
			hasBefore: true, hasAfter: true,
		}
		report, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, &reloadSlash{}, markers), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Results[0].Outcome).To(gomega.Equal(OutcomeUnverified))
	})

	ginkgo.It("delegates exactly once per enumerated session", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{
			{PID: 10, SessionID: "s1"}, {PID: 11, SessionID: "s2"},
		}}
		slash := &reloadSlash{}
		markers := &reloadMarkers{hasBefore: false}
		_, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, slash, markers), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(slash.calls["s1"]).To(gomega.Equal(1))
		gomega.Expect(slash.calls["s2"]).To(gomega.Equal(1))
	})

	ginkgo.It("prints the missing/extra diff when the two populations differ", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{
			{PID: 10, SessionID: "a"}, {PID: 11, SessionID: "b"},
		}}
		roster := &reloadRoster{rows: []AgentRow{
			{SessionID: "b"}, {SessionID: "c"},
		}}
		report, err := ReloadFleet(ctx, reloadDeps(registry, roster, &reloadSlash{}, &reloadMarkers{}), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Missing).To(gomega.ConsistOf("a"))
		gomega.Expect(report.Extra).To(gomega.ConsistOf("c"))
	})

	ginkgo.It("scopes the run to the named sessions", func() {
		registry := &reloadRegistry{entries: []RegistryEntry{
			{PID: 10, SessionID: "a"}, {PID: 11, SessionID: "b"},
		}}
		slash := &reloadSlash{}
		_, err := ReloadFleet(ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}), []string{"b"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(slash.calls["a"]).To(gomega.BeZero())
		gomega.Expect(slash.calls["b"]).To(gomega.Equal(1))
	})
})
