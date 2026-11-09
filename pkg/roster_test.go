// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// rosterSupervisor is a Supervisor whose only used half is List.
//
// The unused methods panic rather than return zero values: a roster that started
// calling Spawn would otherwise read as a passing test that spawned nothing.
type rosterSupervisor struct {
	fakeDurable
	sessions []Session
	err      error
}

func (s *rosterSupervisor) Spawn(context.Context, SpawnOptions) (*Session, error) {
	panic("Spawn is not used by the roster")
}

func (s *rosterSupervisor) List(context.Context) ([]Session, error) {
	return s.sessions, s.err
}

func (s *rosterSupervisor) Get(context.Context, string) (*Session, error) {
	panic("Get is not used by the roster")
}

func (s *rosterSupervisor) Send(context.Context, string, string) error {
	panic("Send is not used by the roster")
}

func (s *rosterSupervisor) SetPermissionMode(context.Context, string, string) (string, error) {
	panic("SetPermissionMode is not used by the roster")
}

func (s *rosterSupervisor) Answer(context.Context, string, string, Answer) error {
	panic("Answer is not used by the roster")
}

func (s *rosterSupervisor) RegisterQuestion(context.Context, string, Question) error {
	panic("RegisterQuestion is not used by the roster")
}

func (s *rosterSupervisor) ForgetQuestion(context.Context, string, string) error {
	panic("ForgetQuestion is not used by the roster")
}

func (s *rosterSupervisor) PollAnswer(context.Context, string) (PendingAnswer, error) {
	panic("PollAnswer is not used by the roster")
}

func (s *rosterSupervisor) Stop(context.Context, string) error {
	panic("Stop is not used by the roster")
}

func (s *rosterSupervisor) Subscribe(context.Context, string) ([]Event, <-chan Event, func(), error) {
	panic("Subscribe is not used by the roster")
}

// rosterLedger is a LedgerReader returning fixed rows.
type rosterLedger struct {
	rows []LedgerRow
	err  error
	// posted records every row handed to Post, so a test can read what a spawn
	// actually filed rather than what it was supposed to file.
	posted []LedgerRow
}

func (l *rosterLedger) Rows(context.Context) ([]LedgerRow, error) {
	return l.rows, l.err
}

func (l *rosterLedger) Post(_ context.Context, _ string, row LedgerRow) error {
	l.posted = append(l.posted, row)
	return nil
}

// Patch is a no-op: the roster reads and spawns, it never writes a verdict.
func (l *rosterLedger) Patch(_ context.Context, _ string, _ LedgerPatch) error { return nil }

// rosterLiveness is a Liveness returning a fixed set.
type rosterLiveness struct {
	live map[string]struct{}
	err  error
}

func (l *rosterLiveness) Live(context.Context) (map[string]struct{}, error) {
	return l.live, l.err
}

// stubHeartbeats is a HeartbeatReader returning fixed rows.
type stubHeartbeats struct {
	rows []HeartbeatRow
	err  error
}

func (s *stubHeartbeats) Rows(context.Context) ([]HeartbeatRow, error) {
	return s.rows, s.err
}

var _ = ginkgo.Describe("RegistryLiveness", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("counts a session the registry cannot see but the heartbeat store can", func() {
		// The headless case: an in-process SDK query has no pid of its own, so it
		// has no registry entry at all. A registry-only union drops exactly the
		// worker a manager most needs to see.
		registryDir := ginkgo.GinkgoT().TempDir()
		live, err := NewRegistryLiveness(
			NewSessionRegistry(registryDir),
			&stubHeartbeats{rows: []HeartbeatRow{
				{SessionID: "headless-worker", Live: true},
			}},
		).Live(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.HaveKey("headless-worker"))
	})

	ginkgo.It("counts a session the heartbeat store has never heard of but the registry lists", func() {
		registryDir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(registryDir, registryFile{
			PID: os.Getpid(), SessionID: "registry-only", Status: "busy",
		})
		live, err := NewRegistryLiveness(
			NewSessionRegistry(registryDir),
			&stubHeartbeats{rows: nil},
		).Live(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.HaveKey("registry-only"))
	})

	ginkgo.It("excludes a stale heartbeat row and a registry entry whose pid is gone", func() {
		registryDir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(registryDir, registryFile{
			PID: 999999, SessionID: "crashed-session", Status: "busy",
		})
		live, err := NewRegistryLiveness(
			NewSessionRegistry(registryDir),
			&stubHeartbeats{rows: []HeartbeatRow{
				{SessionID: "stale-session", Live: false},
			}},
		).Live(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(live).To(gomega.BeEmpty())
	})

	ginkgo.It("returns nil when both channels are unreadable, so the caller degrades", func() {
		live, err := NewRegistryLiveness(
			NewSessionRegistry(ginkgo.GinkgoT().TempDir()+"/missing"),
			&stubHeartbeats{err: context.DeadlineExceeded},
		).Live(ctx)
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(live).To(gomega.BeNil())
	})
})

var _ = ginkgo.Describe("Roster", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("includes a live foreign row under a row-derived id and drops a dead one", func() {
		supervisor := &rosterSupervisor{sessions: []Session{
			{ID: "own-session", Cwd: "/tmp", StartedAt: time.Now()},
		}}
		ledger := &rosterLedger{rows: []LedgerRow{
			{
				SessionID:        "abcdef1234567890",
				AgentID:          "agent_1",
				Label:            "foreign-live",
				Cwd:              "/tmp",
				SpawnerSessionID: "spawner-1",
			},
			{
				SessionID: "dead1234567890",
				AgentID:   "agent_2",
				Label:     "foreign-dead",
			},
		}}
		liveness := &rosterLiveness{live: map[string]struct{}{"abcdef1234567890": {}}}

		roster := NewRoster(supervisor, ledger, liveness, nil, nil)
		rows, err := roster.List(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(2))

		gomega.Expect(rows[0].SessionID).To(gomega.Equal("own-session"))
		gomega.Expect(rows[0].Rehydrated).To(gomega.BeFalse())

		gomega.Expect(rows[1].SessionID).To(gomega.Equal("abcdef1234567890"))
		// The id is a function of the ROW ALONE, so it is stable across calls.
		gomega.Expect(rows[1].AgentID).To(gomega.Equal("agent_1~abcdef12"))
		gomega.Expect(rows[1].Rehydrated).To(gomega.BeTrue())
		gomega.Expect(rows[1].ParentSession).NotTo(gomega.BeNil())
		gomega.Expect(*rows[1].ParentSession).To(gomega.Equal("spawner-1"))
	})

	ginkgo.It("adopts a ledger row that only the heartbeat store reports live", func() {
		// The fixture that matters: a headless worker spawned by a node
		// supervisor. It has NO registry entry, so a registry-only liveness would
		// drop it — and SC2's cross-session test would pass on a tab worker by
		// accident while failing on the real one.
		registryDir := ginkgo.GinkgoT().TempDir()
		liveness := NewRegistryLiveness(
			NewSessionRegistry(registryDir),
			&stubHeartbeats{rows: []HeartbeatRow{
				{SessionID: "abcdef1234567890", Live: true},
				{SessionID: "stale1234567890", Live: false},
			}},
		)
		ledger := &rosterLedger{rows: []LedgerRow{
			{SessionID: "abcdef1234567890", AgentID: "agent_7", Label: "headless-worker"},
			{SessionID: "stale1234567890", AgentID: "agent_8", Label: "stale-worker"},
		}}

		rows, err := NewRoster(&rosterSupervisor{}, ledger, liveness, nil, nil).List(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].SessionID).To(gomega.Equal("abcdef1234567890"))
		gomega.Expect(rows[0].Rehydrated).To(gomega.BeTrue())
	})

	ginkgo.It("keeps its own row when the ledger names the same session", func() {
		supervisor := &rosterSupervisor{sessions: []Session{
			{ID: "shared", Cwd: "/tmp", StartedAt: time.Now()},
		}}
		ledger := &rosterLedger{rows: []LedgerRow{
			{SessionID: "shared", AgentID: "agent_9", Label: "hollow"},
		}}
		liveness := &rosterLiveness{live: map[string]struct{}{"shared": {}}}

		rows, err := NewRoster(supervisor, ledger, liveness, nil, nil).List(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Rehydrated).To(gomega.BeFalse())
		gomega.Expect(rows[0].AgentID).To(gomega.Equal("shared"))
	})

	ginkgo.It("returns its own rows when the ledger cannot be read", func() {
		supervisor := &rosterSupervisor{sessions: []Session{
			{ID: "own-session", Cwd: "/tmp", StartedAt: time.Now()},
		}}
		ledger := &rosterLedger{err: context.DeadlineExceeded}
		liveness := &rosterLiveness{live: map[string]struct{}{}}

		rows, err := NewRoster(supervisor, ledger, liveness, nil, nil).List(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].SessionID).To(gomega.Equal("own-session"))
	})

	ginkgo.It("reports unknown agent for an id no row carries", func() {
		roster := NewRoster(&rosterSupervisor{}, nil, nil, nil, nil)
		_, err := roster.Get(ctx, "nobody")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("unknown agent"))
	})

	ginkgo.It("derives awaiting_input from the registry status and leaves it null when unlisted", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{PID: 1, SessionID: "waiting-session", Status: "waiting", Cwd: "/tmp"})
		registry := NewSessionRegistry(dir)

		supervisor := &rosterSupervisor{sessions: []Session{
			{ID: "waiting-session", Cwd: "/tmp", StartedAt: time.Now()},
			{ID: "unlisted-session", Cwd: "/tmp", StartedAt: time.Now()},
		}}
		rows, err := NewRoster(supervisor, nil, nil, nil, registry).List(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(2))

		gomega.Expect(rows[0].AwaitingInput).NotTo(gomega.BeNil())
		gomega.Expect(*rows[0].AwaitingInput).To(gomega.BeTrue())

		// Unlisted is a different fact from not-waiting, so it is null and never
		// false.
		gomega.Expect(rows[1].SessionStatus).To(gomega.BeNil())
		gomega.Expect(rows[1].AwaitingInput).To(gomega.BeNil())
	})
})
