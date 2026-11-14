// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"
	"sync"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fakeFleetRegistry is a SessionRegistry whose only meaningful method is Live.
type fakeFleetRegistry struct {
	entries []RegistryEntry
	err     error
}

func (f *fakeFleetRegistry) Live(context.Context) ([]RegistryEntry, error) { return f.entries, f.err }

func (f *fakeFleetRegistry) Status(context.Context, string) (*string, error) {
	return nil, nil
}

func (f *fakeFleetRegistry) Cwd(context.Context, string) (*string, error) { return nil, nil }

func (f *fakeFleetRegistry) Named(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *fakeFleetRegistry) NamesForSession(context.Context, string) ([]string, error) {
	return nil, nil
}

// fakeJoiner is a PaneJoiner with a fixed map.
type fakeJoiner struct {
	panes map[int]string
	err   error
}

func (f *fakeJoiner) PanesForPIDs(context.Context, []int) (map[int]string, error) {
	return f.panes, f.err
}

// fakeTabs is a TabClient that answers a pre-send screen, then a post-send one.
type fakeTabs struct {
	mu     sync.Mutex
	before string
	after  string
	sent   map[string]int
}

func (f *fakeTabs) Spawn(context.Context, TabSpawnOptions) (string, error) {
	return "", errors.New("not used")
}

func (f *fakeTabs) Activate(context.Context, string) error { return nil }

func (f *fakeTabs) SendText(_ context.Context, pane string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sent == nil {
		f.sent = map[string]int{}
	}
	f.sent[pane]++
	return nil
}

func (f *fakeTabs) Text(_ context.Context, pane string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sent[pane] > 0 {
		return f.after, nil
	}
	return f.before, nil
}

// fakeCards is an OpenCardReader with a fixed set.
type fakeCards struct {
	ids map[string]struct{}
	err error
}

func (f *fakeCards) OpenSessionIDs(context.Context) (map[string]struct{}, error) {
	return f.ids, f.err
}

// fakeSubject is a SubjectStateReader with a fixed map.
type fakeSubject struct {
	states map[string]string
}

func (f *fakeSubject) State(_ context.Context, id string) (string, bool, error) {
	state, ok := f.states[id]
	return state, ok, nil
}

func pusherFor(
	registry SessionRegistry,
	joiner PaneJoiner,
	tabs TabClient,
	cards OpenCardReader,
) *FleetPusher {
	return &FleetPusher{
		Registry: registry,
		Joiner:   joiner,
		Tabs:     tabs,
		Cards:    cards,
		Subject:  &fakeSubject{},
		Now:      libtime.NewCurrentDateTime(),
		Message:  "wake now",
	}
}

var _ = ginkgo.Describe("FleetPusher", func() {
	ginkgo.Describe("Tick line", func() {
		ginkgo.It("emits the counter line unconditionally, zeros included", func() {
			pusher := pusherFor(&fakeFleetRegistry{}, &fakeJoiner{}, &fakeTabs{}, &fakeCards{})
			result := pusher.Tick(context.Background())
			gomega.Expect(result.Line()).To(gomega.Equal(
				"job=fleet-pusher action=tick sessions=0 delivered=0 skipped=0",
			))
		})

		ginkgo.It("names a registry failure instead of reporting an idle fleet", func() {
			pusher := pusherFor(&fakeFleetRegistry{err: errors.New("boom")}, &fakeJoiner{}, &fakeTabs{}, &fakeCards{})
			result := pusher.Tick(context.Background())
			gomega.Expect(result.Line()).To(gomega.HavePrefix(
				"job=fleet-pusher action=tick sessions=0 delivered=0 skipped=0 error=registry",
			))
		})
	})

	ginkgo.Describe("skip classes", func() {
		ginkgo.It("refuses the session running the tick", func() {
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s-self", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}},
				&fakeTabs{before: "❯ \n"}, &fakeCards{},
			)
			pusher.SelfSession = "s-self"
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(OutcomeSkipSelf))
		})

		ginkgo.It("refuses a session named Fleet Manager", func() {
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1, Name: FleetManagerName}}},
				&fakeJoiner{panes: map[int]string{1: "11"}},
				&fakeTabs{before: "❯ \n"}, &fakeCards{},
			)
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(OutcomeSkipFleetManager))
		})

		ginkgo.It("refuses another Fleet Pusher", func() {
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1, Name: "Fleet Pusher 2"}}},
				&fakeJoiner{panes: map[int]string{1: "11"}},
				&fakeTabs{before: "❯ \n"}, &fakeCards{},
			)
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(OutcomeSkipPusher))
		})

		ginkgo.It("refuses a scoped session the registry does not list as live", func() {
			pusher := pusherFor(&fakeFleetRegistry{}, &fakeJoiner{}, &fakeTabs{}, &fakeCards{})
			pusher.Sessions = []string{"s-ghost"}
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(OutcomeSkipNotLive))
		})

		ginkgo.It("refuses a session whose pid joins no pane", func() {
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{}},
				&fakeTabs{before: "❯ \n"}, &fakeCards{},
			)
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(OutcomeSkipNoPane))
		})

		ginkgo.It("refuses a session holding an open card", func() {
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}},
				&fakeTabs{before: "❯ \n"},
				&fakeCards{ids: map[string]struct{}{"s1": {}}},
			)
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(OutcomeSkipOpenCard))
		})

		ginkgo.It("fails closed on every session when the board cannot be read", func() {
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}},
				&fakeTabs{before: "❯ \n"},
				&fakeCards{err: errors.New("store down")},
			)
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(OutcomeSkipOpenCard))
		})
	})

	ginkgo.Describe("guard and send", func() {
		ginkgo.It("carries a guard skip as the outcome", func() {
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}},
				&fakeTabs{before: "✻ Thinking… (3s\n"}, &fakeCards{},
			)
			row := pusher.Tick(context.Background()).Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal(VerdictSkipSpinner))
		})

		ginkgo.It("is a dry run by default, typing nothing", func() {
			tabs := &fakeTabs{before: "❯ \n", after: "❯ wake now\n❯ \n"}
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
			)
			result := pusher.Tick(context.Background())
			gomega.Expect(result.Rows[0].Outcome).To(gomega.Equal(OutcomeDryRun))
			gomega.Expect(result.Delivered).To(gomega.Equal(0))
			gomega.Expect(result.Skipped).To(gomega.Equal(1))
			gomega.Expect(tabs.sent).To(gomega.BeEmpty())
		})

		ginkgo.It("delivers when --send is set and the submit is confirmed", func() {
			tabs := &fakeTabs{before: "❯ \n", after: "❯ wake now\n❯ \n"}
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
			)
			pusher.Send = true
			result := pusher.Tick(context.Background())
			gomega.Expect(result.Rows[0].Outcome).To(gomega.Equal(OutcomeDelivered))
			gomega.Expect(result.Delivered).To(gomega.Equal(1))
			gomega.Expect(result.Skipped).To(gomega.Equal(0))
			gomega.Expect(tabs.sent["11"]).To(gomega.BeNumerically(">", 0))
		})

		// The guard's wake:suggestion verdict must be deliverable. A long-idle pane
		// always shows Claude Code's dim next-prompt suggestion, which the spawn
		// path's ComposerReady deliberately refuses; the wake path must accept it or
		// every suggestion-verdict wake times out. This pins the readiness seam.
		ginkgo.It("delivers into a dim-placeholder composer the spawn path refuses", func() {
			placeholder := "❯ \x1b[0;2mTry \"how does this work?\"\x1b[0m\n"
			tabs := &fakeTabs{before: placeholder, after: "❯ wake now\n❯ \n"}
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
			)
			pusher.Send = true
			result := pusher.Tick(context.Background())
			gomega.Expect(result.Rows[0].Outcome).To(gomega.Equal(OutcomeDelivered))
			gomega.Expect(tabs.sent["11"]).To(gomega.BeNumerically(">", 0))
			gomega.Expect(ComposerReady(placeholder)).To(gomega.BeFalse())
			gomega.Expect(PaneIsReady(placeholder)).To(gomega.BeTrue())
		})
	})
})
