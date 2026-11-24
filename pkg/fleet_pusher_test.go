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

func (f *fakeFleetRegistry) Entries(context.Context) ([]RegistryEntry, error) {
	return f.entries, f.err
}

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
	// sendErr makes SendText fail, for the send-failure branch.
	sendErr error
}

func (f *fakeTabs) Spawn(context.Context, TabSpawnOptions) (string, error) {
	return "", errors.New("not used")
}

func (f *fakeTabs) Activate(context.Context, string) error { return nil }

func (f *fakeTabs) SendText(_ context.Context, pane string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
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

// fakeAnchor is an AnchorReader with a fixed answer per session.
type fakeAnchor struct {
	anchored map[string]bool
	err      error
}

func (f *fakeAnchor) Anchored(_ context.Context, id string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.anchored[id], nil
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
			tabs := &fakeTabs{before: "❯ \n", after: "✻ Thinking… (3s\n❯ \n"}
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
			tabs := &fakeTabs{before: "❯ \n", after: "✻ Thinking… (3s\n❯ \n"}
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

		// The send half's failure branch. The keystrokes reach the pty but the pane
		// never shows the wake as submitted; SendWhenReadyWith reports that as an
		// error, and the dispatcher must surface it as `failed:<err>` rather than
		// counting a message that never ran as delivered.
		ginkgo.It("reports failed when the submit is never confirmed on the screen", func() {
			tabs := &fakeTabs{before: "❯ \n", after: "❯ \n"}
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
			)
			pusher.Send = true
			// The two 8 s confirmation windows must expire without sleeping 16 s.
			pusher.Now = steppingClock()
			result := pusher.Tick(context.Background())
			row := result.Rows[0]
			gomega.Expect(row.Outcome).To(gomega.HavePrefix("failed:"))
			gomega.Expect(row.Outcome).To(gomega.ContainSubstring(
				"pane 11 never showed the message as submitted",
			))
			gomega.Expect(result.Delivered).To(gomega.Equal(0))
			gomega.Expect(result.Skipped).To(gomega.Equal(1))
		})

		// The send call itself can fail. Its reason must survive into the outcome
		// token: an assertion on `failed:` alone would also pass for a hard-coded
		// reason that discards the error.
		ginkgo.It("carries the send error's own text into the failed outcome", func() {
			sendErr := errors.New("wezterm send-text refused the pane")
			tabs := &fakeTabs{before: "❯ \n", sendErr: sendErr}
			pusher := pusherFor(
				&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
				&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
			)
			pusher.Send = true
			result := pusher.Tick(context.Background())
			row := result.Rows[0]
			gomega.Expect(row.Outcome).To(gomega.Equal("failed:" + sendErr.Error()))
			gomega.Expect(row.Outcome).To(gomega.ContainSubstring(sendErr.Error()))
			gomega.Expect(result.Delivered).To(gomega.Equal(0))
			gomega.Expect(result.Skipped).To(gomega.Equal(1))
		})

		// The guard's wake:suggestion verdict must be deliverable. A long-idle pane
		// always shows Claude Code's dim next-prompt suggestion, which the spawn
		// path's ComposerReady deliberately refuses; the wake path must accept it or
		// every suggestion-verdict wake times out. This pins the readiness seam.
		ginkgo.Describe("retired and unanchored sessions", func() {
			// SC1. The keeper refuses to restart a stood-down subject; the pusher must
			// refuse to wake it, or the retirement is only half real.
			ginkgo.It("skips a stood-down subject and names the stand-down", func() {
				tabs := &fakeTabs{before: "❯ \n", after: "✻ Thinking… (3s\n❯ \n"}
				pusher := pusherFor(
					&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
					&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
				)
				pusher.Subject = &fakeSubject{states: map[string]string{"s1": SubjectStateStopped}}
				pusher.Send = true
				result := pusher.Tick(context.Background())
				row := result.Rows[0]
				gomega.Expect(row.Outcome).To(gomega.Equal(VerdictSkipStopped))
				gomega.Expect(row.Line()).To(gomega.ContainSubstring("outcome=skip:stopped"))
				gomega.Expect(result.Delivered).To(gomega.Equal(0))
				gomega.Expect(tabs.sent).To(gomega.BeEmpty())
			})

			// The reason must survive an open card: a probe that looks for skip:stopped
			// would otherwise read skip:open-card and conclude the rule never fired.
			ginkgo.It("reports the stand-down ahead of an open card", func() {
				pusher := pusherFor(
					&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
					&fakeJoiner{panes: map[int]string{1: "11"}}, &fakeTabs{before: "❯ \n"},
					&fakeCards{ids: map[string]struct{}{"s1": {}}},
				)
				pusher.Subject = &fakeSubject{states: map[string]string{"s1": SubjectStateStopped}}
				gomega.Expect(pusher.Tick(context.Background()).Rows[0].Outcome).
					To(gomega.Equal(VerdictSkipStopped))
			})

			// SC4. The wake asks for "the next unchecked subtask on your anchored
			// task"; a session nothing anchors cannot act on it.
			ginkgo.It("skips a session nothing anchors", func() {
				tabs := &fakeTabs{before: "❯ \n", after: "✻ Thinking… (3s\n❯ \n"}
				pusher := pusherFor(
					&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
					&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
				)
				pusher.Anchors = &fakeAnchor{anchored: map[string]bool{"s1": false}}
				pusher.Send = true
				result := pusher.Tick(context.Background())
				gomega.Expect(result.Rows[0].Outcome).To(gomega.Equal(OutcomeSkipUnanchored))
				gomega.Expect(result.Delivered).To(gomega.Equal(0))
				gomega.Expect(tabs.sent).To(gomega.BeEmpty())
			})

			// DoD4's positive control: without it, a build that skipped every session
			// would satisfy SC1, SC2 and SC4 — all three are negative criteria.
			ginkgo.It("still delivers to an anchored session", func() {
				tabs := &fakeTabs{before: "❯ \n", after: "✻ Thinking… (3s\n❯ \n"}
				pusher := pusherFor(
					&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
					&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
				)
				pusher.Anchors = &fakeAnchor{anchored: map[string]bool{"s1": true}}
				pusher.Send = true
				result := pusher.Tick(context.Background())
				gomega.Expect(result.Rows[0].Outcome).To(gomega.Equal(OutcomeDelivered))
				gomega.Expect(tabs.sent["11"]).To(gomega.BeNumerically(">", 0))
			})

			// A vault outage must not stop the fleet: the anchor check fails OPEN.
			ginkgo.It("delivers when the anchor check cannot be read", func() {
				tabs := &fakeTabs{before: "❯ \n", after: "✻ Thinking… (3s\n❯ \n"}
				pusher := pusherFor(
					&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
					&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
				)
				pusher.Anchors = &fakeAnchor{err: errors.New("vault-api unavailable")}
				pusher.Send = true
				gomega.Expect(pusher.Tick(context.Background()).Rows[0].Outcome).
					To(gomega.Equal(OutcomeDelivered))
			})

			// A caller that wired no anchor reader must keep waking: the check is
			// opt-in, never an implicit skip-everything.
			ginkgo.It("delivers when no anchor reader is wired", func() {
				tabs := &fakeTabs{before: "❯ \n", after: "✻ Thinking… (3s\n❯ \n"}
				pusher := pusherFor(
					&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 1}}},
					&fakeJoiner{panes: map[int]string{1: "11"}}, tabs, &fakeCards{},
				)
				pusher.Send = true
				gomega.Expect(pusher.Tick(context.Background()).Rows[0].Outcome).
					To(gomega.Equal(OutcomeDelivered))
			})
		})

		ginkgo.It("delivers into a dim-placeholder composer the spawn path refuses", func() {
			placeholder := "❯ \x1b[0;2mTry \"how does this work?\"\x1b[0m\n"
			tabs := &fakeTabs{before: placeholder, after: "✻ Thinking… (3s\n❯ \n"}
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
