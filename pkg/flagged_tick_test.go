// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// recordingSpawner records every spawn request it is handed.
type recordingSpawner struct {
	mutex    sync.Mutex
	requests []TabSpawnRequest
	err      error
}

func (s *recordingSpawner) Spawn(
	_ context.Context, request TabSpawnRequest,
) (*TabSpawnResult, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.requests = append(s.requests, request)
	if s.err != nil {
		return nil, s.err
	}
	return &TabSpawnResult{Label: request.Label}, nil
}

func (s *recordingSpawner) snapshot() []TabSpawnRequest {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	out := make([]TabSpawnRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// recordingReopener records every row it is asked to reopen.
//
// The tick's own tests use this rather than a spawner because the decision pass
// is what they cover; the spawn REQUEST is asserted against the reopener below,
// which is where the empty-`prompt` defect lived.
type recordingReopener struct {
	mutex sync.Mutex
	rows  []FlaggedRow
	err   error
}

func (r *recordingReopener) Reopen(_ context.Context, row FlaggedRow) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.rows = append(r.rows, row)
	return r.err
}

func (r *recordingReopener) snapshot() []FlaggedRow {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	out := make([]FlaggedRow, len(r.rows))
	copy(out, r.rows)
	return out
}

// recordingSupervisor records the headless spawn it was handed.
type recordingSupervisor struct {
	mutex     sync.Mutex
	spawns    []SpawnOptions
	sends     []string
	stopped   []string
	spawnErr  error
	sendErr   error
	sessionID string
	// held is the child table Get answers from; an absent id is not held.
	held map[string]*Session
}

func (s *recordingSupervisor) Get(_ context.Context, id string) (*Session, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if session, ok := s.held[id]; ok {
		return session, nil
	}
	return nil, ErrSessionNotFound
}

func (s *recordingSupervisor) Spawn(
	_ context.Context, options SpawnOptions,
) (*Session, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.spawns = append(s.spawns, options)
	if s.spawnErr != nil {
		return nil, s.spawnErr
	}
	id := s.sessionID
	if id == "" {
		id = "spawned"
	}
	return &Session{ID: id}, nil
}

func (s *recordingSupervisor) Send(_ context.Context, id string, text string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.sends = append(s.sends, id+": "+text)
	return s.sendErr
}

func (s *recordingSupervisor) Stop(_ context.Context, id string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.stopped = append(s.stopped, id)
	return nil
}

func (s *recordingSupervisor) snapshot() []SpawnOptions {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	out := make([]SpawnOptions, len(s.spawns))
	copy(out, s.spawns)
	return out
}

func (s *recordingSupervisor) sent() []string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	out := make([]string, len(s.sends))
	copy(out, s.sends)
	return out
}

// staticVaults answers a fixed vault list.
type staticVaults struct {
	vaults []Vault
	err    error
}

func (v staticVaults) List(_ context.Context) ([]Vault, error) {
	return v.vaults, v.err
}

func testVaults() staticVaults {
	return staticVaults{vaults: []Vault{{
		Name:           "private-personal",
		Path:           "/tmp/vault-private-personal",
		WorkerLauncher: "cc-private",
	}}}
}

// recordingCaps records the arguments every cap call received.
type recordingCaps struct {
	mutex       sync.Mutex
	workerCalls []string
	rateCalls   int
	workerErr   error
	rateErr     error
}

func (c *recordingCaps) CheckWorker(_ context.Context, mode string, _ bool) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.workerCalls = append(c.workerCalls, mode)
	return c.workerErr
}

func (c *recordingCaps) CheckManager(
	context.Context, string, []string, bool,
) error {
	return nil
}

func (c *recordingCaps) ReserveStart(context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.rateCalls++
	return c.rateErr
}

func (c *recordingCaps) modes() []string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	out := make([]string, len(c.workerCalls))
	copy(out, c.workerCalls)
	return out
}

// staticSource returns a fixed row set.
type staticSource struct {
	rows []FlaggedRow
	err  error
}

func (s staticSource) Rows(context.Context) ([]FlaggedRow, error) {
	return s.rows, s.err
}

// blockingSource never returns until its context ends.
type blockingSource struct{}

func (blockingSource) Rows(ctx context.Context) ([]FlaggedRow, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// countingSource returns rows and counts its calls, so a test can prove the
// loop kept ticking after an abandoned read.
type countingSource struct {
	mutex sync.Mutex
	calls int
	block bool
}

func (s *countingSource) Rows(ctx context.Context) ([]FlaggedRow, error) {
	s.mutex.Lock()
	s.calls++
	block := s.block
	s.mutex.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, nil
}

func (s *countingSource) count() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.calls
}

// The guard must survive the attention-controller being down: the durable child
// table is on disk, so a ledger outage no longer disables the double-open guard.
var _ = ginkgo.Describe("flagged tick: recently-spawned guard without the ledger", func() {
	ginkgo.It("still suppresses a row whose child record is live", func() {
		ctx := context.Background()
		store := NewSessionStore(ginkgo.GinkgoT().TempDir())
		gomega.Expect(store.Create(ctx, SessionRecord{
			ID:        "child-1",
			ChildPID:  os.Getpid(),
			StartedAt: time.Now(),
		})).Error().NotTo(gomega.HaveOccurred())
		tick := &FlaggedTick{
			Ledger:   ledgerWith{err: stderrors.New("connection refused")},
			Sessions: store,
			Now:      fixedClock(time.Now()),
			Logf:     func(string, ...any) {},
		}

		recent := recentlySpawned(tick.ledgerRows(ctx))
		tick.mergeChildSpawnTimes(ctx, recent)
		got := recent

		gomega.Expect(got).To(gomega.HaveKey("child-1"))
	})

	ginkgo.It("ignores a child record whose pid is gone", func() {
		ctx := context.Background()
		store := NewSessionStore(ginkgo.GinkgoT().TempDir())
		gomega.Expect(store.Create(ctx, SessionRecord{
			ID:        "child-dead",
			ChildPID:  0,
			StartedAt: time.Now(),
		})).Error().NotTo(gomega.HaveOccurred())
		tick := &FlaggedTick{
			Ledger:   ledgerWith{err: stderrors.New("connection refused")},
			Sessions: store,
			Now:      fixedClock(time.Now()),
			Logf:     func(string, ...any) {},
		}

		recent := recentlySpawned(tick.ledgerRows(ctx))
		tick.mergeChildSpawnTimes(ctx, recent)
		got := recent

		gomega.Expect(got).NotTo(gomega.HaveKey("child-dead"))
	})
})

// SC2: a controller outage must not silence the tick. Fault injection — the
// ledger and heartbeat endpoints refuse, the count source is healthy — and the
// row must still open, with the line naming where the count came from.
var _ = ginkgo.Describe("flagged tick: caps during a controller outage", func() {
	ginkgo.It("opens the row when only the ledger is refused", func() {
		ctx := context.Background()
		caps := testCaps(
			ledgerWith{err: stderrors.New("connection refused")},
			&stubHeartbeats{err: stderrors.New("connection refused")},
			"",
			ginkgo.GinkgoT().TempDir(),
		)
		caps.registry = &capsRegistry{live: []RegistryEntry{{SessionID: "s1"}}}
		row := rowWith("agent", SpawnModeHeadless, "flagged-row")
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, caps)

		result := tick.tickFromRows(ctx, []FlaggedRow{row})

		gomega.Expect(result.Opened).To(gomega.HaveLen(1))
		gomega.Expect(result.Refused).To(gomega.BeEmpty())
		gomega.Expect(result.LiveCountSource).To(gomega.Equal("registry"))
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("live_count_source=registry"))
	})
})

// The tick path must leave the endpoint's bind in its own line. A criterion that
// requires the endpoint's bind, naming the session, has no other artifact to read
// on a tick-driven open — so an omitted field is the difference between a
// checkable fix and one only a hand-run POST could satisfy.
var _ = ginkgo.Describe("FlaggedTickResult.Line renders the endpoint's bind", func() {
	ginkgo.It("prints binds_rows when an opened row bound a page", func() {
		result := FlaggedTickResult{
			Opened: []string{"v/goal/G"},
			Binds:  []string{"v/goal/G=stamped:sid-1"},
		}
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("binds=1"))
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("binds_rows=v/goal/G=stamped:sid-1"))
	})

	ginkgo.It("omits binds when no opened row bound a page", func() {
		result := FlaggedTickResult{Opened: []string{"v/task/T"}}
		gomega.Expect(result.Line()).NotTo(gomega.ContainSubstring("binds="))
	})
})

// ledgerWith supplies spawn times to the recently-spawned guard.
type ledgerWith struct {
	rows []LedgerRow
	err  error
}

func (l ledgerWith) Rows(context.Context) ([]LedgerRow, error) { return l.rows, l.err }

func (l ledgerWith) Post(context.Context, string, LedgerRow) error { return nil }

func (l ledgerWith) Patch(context.Context, string, LedgerPatch) error { return nil }

// recordingLedger records the rows Post was handed.
type recordingLedger struct {
	mutex   sync.Mutex
	rows    []LedgerRow
	patches []LedgerPatch
	err     error
}

func (l *recordingLedger) Rows(context.Context) ([]LedgerRow, error) { return nil, l.err }

func (l *recordingLedger) Post(_ context.Context, _ string, row LedgerRow) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.rows = append(l.rows, row)
	return l.err
}

func (l *recordingLedger) Patch(_ context.Context, _ string, patch LedgerPatch) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.patches = append(l.patches, patch)
	return l.err
}

func (l *recordingLedger) patchSnapshot() []LedgerPatch {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	out := make([]LedgerPatch, len(l.patches))
	copy(out, l.patches)
	return out
}

func (l *recordingLedger) snapshot() []LedgerRow {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	out := make([]LedgerRow, len(l.rows))
	copy(out, l.rows)
	return out
}

// recordingRunner records the argv it was handed and returns fixed stdout.
type recordingRunner struct {
	argv []string
	out  []byte
	err  error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.argv = append([]string{name}, args...)
	return r.out, r.err
}

func fixedNow(at time.Time) libtime.CurrentDateTimeGetter {
	getter := libtime.NewCurrentDateTime()
	getter.SetNow(libtime.DateTime(at))
	return getter
}

func rowWith(role, mode, name string) FlaggedRow {
	return FlaggedRow{
		Kind:     "task",
		Vault:    "private-personal",
		Name:     name,
		Role:     role,
		Mode:     mode,
		Liveness: "ABSENT",
	}
}

func newTick(source FlaggedRowSource, reopener FlaggedReopener, caps SpawnCaps) *FlaggedTick {
	return &FlaggedTick{
		Source:   source,
		Reopener: reopener,
		Caps:     caps,
		Now:      fixedNow(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)),
		Logf:     func(string, ...any) {},
		// A spec must never read the host's real child table: `Home` scopes the state
		// directory to an empty temp dir and `Getenv` answers nothing, so the
		// per-session `record.json` half of the guard is empty unless a spec opts in
		// by setting `Sessions`.
		Home:   ginkgo.GinkgoT().TempDir(),
		Getenv: func(string) string { return "" },
	}
}

var _ = ginkgo.Describe("flagged reopen tick", func() {
	ginkgo.Describe("decides whether a flagged row is reopened", func() {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		recent := map[string]time.Time{
			"recent":                    now.Add(-5 * time.Minute),
			labelKey("opened-just-now"): now.Add(-5 * time.Minute),
		}

		ginkgo.DescribeTable("each guard",
			func(row FlaggedRow, wantOpened bool, wantReason string) {
				reason, refused, skipped := decision(row, recent, nil, nil, now, neverOpened(row), false)
				ginkgo.GinkgoWriter.Printf(
					"case=%s reason=%s opened=%t\n", row.Name, reason, !refused && !skipped,
				)
				gomega.Expect(!refused && !skipped).To(gomega.Equal(wantOpened))
				if !wantOpened {
					gomega.Expect(reason).To(gomega.Equal(wantReason))
				}
			},
			ginkgo.Entry("agent-set-flag", FlaggedRow{
				Name: "agent-set-flag", Refused: []string{"agent-set-flag"}, Liveness: "ABSENT",
			}, false, "agent-set-flag"),
			ginkgo.Entry("manager-row", FlaggedRow{
				Name: "manager-row", Refused: []string{"manager-row"}, Liveness: "ABSENT",
			}, false, "manager-row"),
			ginkgo.Entry("blocked", FlaggedRow{
				Name: "blocked", BlockedBy: []string{"[[X]]"}, Liveness: "ABSENT",
			}, false, reasonBlocked),
			ginkgo.Entry("live", FlaggedRow{
				Name: "live", Liveness: "LIVE",
			}, false, reasonNotAbsent),
			ginkgo.Entry("none without a branch", FlaggedRow{
				Name: "none", Liveness: "none",
			}, false, reasonNotAbsent),
			ginkgo.Entry("never opened — create", FlaggedRow{
				Name: "never-opened-create", Kind: "task", Liveness: "none", Branch: "create",
			}, true, ""),
			ginkgo.Entry("never opened — direct", FlaggedRow{
				Name: "never-opened-direct", Kind: "task", Liveness: "none", Branch: "direct",
			}, true, ""),
			ginkgo.Entry("none WITH a session id (--no-liveness shape) is never opened fresh",
				FlaggedRow{
					Name: "none-with-id", Kind: "task", SessionID: "abc", Liveness: "none",
					Branch: "create",
				}, false, reasonNotAbsent),
			ginkgo.Entry("never opened, but opened by label inside the window", FlaggedRow{
				Name: "opened-just-now", Kind: "task", Liveness: "none", Branch: "create",
			}, false, reasonRecentlySpawned),
			ginkgo.Entry("a goal on create is never opened fresh (its manager may be live)",
				FlaggedRow{
					Name: "goal-create", Kind: "goal", Liveness: "none", Branch: "create",
				}, false, reasonNotAbsent),
			ginkgo.Entry("unknown", FlaggedRow{
				Name: "unknown", Liveness: "UNKNOWN",
			}, false, reasonNotAbsent),
			ginkgo.Entry("jump", FlaggedRow{
				Name: "jump", Liveness: "ABSENT", Branch: "jump",
			}, false, reasonAlreadyLive),
			ginkgo.Entry("recently-spawned", FlaggedRow{
				Name: "recently-spawned", SessionID: "recent", Liveness: "ABSENT",
			}, false, reasonRecentlySpawned),
			ginkgo.Entry("eligible", FlaggedRow{
				Name: "eligible", Liveness: "ABSENT",
			}, true, ""),
		)
	})

	ginkgo.Describe("the recently-spawned guard", func() {
		now := time.Date(2026, 11, 16, 17, 47, 0, 0, time.UTC)
		spawned := now.Add(-10 * time.Minute).Format(time.RFC3339)

		// A terminal status proves the session ENDED, so the duplicate-open
		// hazard the window exists for is gone. Every other value — an absent
		// one included — proves nothing and must keep suppressing, or a row
		// whose worker is still registering would be opened twice.
		ginkgo.DescribeTable("suppresses only a spawn that has not closed",
			func(status string, wantSuppressed bool) {
				tick := &FlaggedTick{
					Ledger: ledgerWith{rows: []LedgerRow{{
						SessionID: "sid", Label: "a task", SpawnedAt: spawned, Status: status,
					}}},
					Logf: func(string, ...any) {},
				}
				recent := recentlySpawned(tick.ledgerRows(context.Background()))
				_, bySession := recent["sid"]
				_, byLabel := recent[labelKey("a task")]
				gomega.Expect(bySession).To(gomega.Equal(wantSuppressed))
				gomega.Expect(byLabel).To(gomega.Equal(wantSuppressed))
			},
			ginkgo.Entry("done — the session ended", "done", false),
			ginkgo.Entry("error — the session ended", "error", false),
			ginkgo.Entry("running — it may still be starting", "running", true),
			ginkgo.Entry("absent status proves nothing", "", true),
		)
	})

	ginkgo.Describe("bounds rows per tick", func() {
		ginkgo.It("opens exactly maxRowsPerTick of a larger eligible set", func() {
			rows := make([]FlaggedRow, 60)
			for i := range rows {
				rows[i] = rowWith("agent", "headless", "row")
				rows[i].SessionID = string(rune('a'+i%26)) + string(rune('0'+i/26))
			}
			reopener := &recordingReopener{}
			caps := &recordingCaps{}
			result, err := newTick(staticSource{rows: rows}, reopener, caps).Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf(
				"opened=%d refused=%d reopens=%d\n",
				len(result.Opened), len(result.Refused), len(reopener.snapshot()),
			)
			gomega.Expect(reopener.snapshot()).To(gomega.HaveLen(maxRowsPerTick))
			gomega.Expect(result.Opened).To(gomega.HaveLen(maxRowsPerTick))
			gomega.Expect(result.Refused).To(gomega.HaveLen(10))
		})
	})

	ginkgo.Describe("orders a human row before agent and manager rows", func() {
		ginkgo.It("spawns the human row first", func() {
			rows := []FlaggedRow{
				rowWith("agent", "headless", "agent-row"),
				rowWith("manager", "headless", "manager-row"),
				rowWith("human", "interactive", "human-row"),
			}
			reopener := &recordingReopener{}
			_, err := newTick(staticSource{rows: rows}, reopener, &recordingCaps{}).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			requests := reopener.snapshot()
			ginkgo.GinkgoWriter.Printf("first=%s\n", requests[0].Name)
			gomega.Expect(requests[0].Name).To(gomega.Equal("human-row"))
			gomega.Expect(requests).To(gomega.HaveLen(3))
		})
	})

	ginkgo.Describe("stops on a cap refusal", func() {
		ginkgo.It("spawns nothing and refuses every row when CheckWorker refuses", func() {
			rows := []FlaggedRow{
				rowWith("agent", "headless", "one"),
				rowWith("agent", "headless", "two"),
				rowWith("agent", "headless", "three"),
			}
			reopener := &recordingReopener{}
			caps := &recordingCaps{workerErr: stderrors.New("maxConcurrent reached")}
			result, err := newTick(staticSource{rows: rows}, reopener, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("reopens=%d refused=%d\n",
				len(reopener.snapshot()), len(result.Refused))
			gomega.Expect(reopener.snapshot()).To(gomega.BeEmpty())
			// Three, not one: the caps refusal ends the opening pass but every later
			// row is still accounted for as unconsidered. The old count of one was
			// the defect — two rows vanished out of all three buckets.
			gomega.Expect(result.Refused).To(gomega.HaveLen(3))
			gomega.Expect(result.Refused[0]).To(gomega.ContainSubstring(reasonCapsRefused))
			gomega.Expect(result.Refused[1]).To(gomega.ContainSubstring(reasonUnconsidered))
			gomega.Expect(result.Refused[2]).To(gomega.ContainSubstring(reasonUnconsidered))
		})

		ginkgo.It("spawns nothing when ReserveStart refuses", func() {
			rows := []FlaggedRow{rowWith("agent", "headless", "one")}
			reopener := &recordingReopener{}
			caps := &recordingCaps{rateErr: stderrors.New("maxStartsPerMinute reached")}
			result, err := newTick(staticSource{rows: rows}, reopener, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(reopener.snapshot()).To(gomega.BeEmpty())
			gomega.Expect(result.Refused).To(gomega.HaveLen(1))
		})
	})

	ginkgo.Describe("passes the row mode to the caps", func() {
		ginkgo.It("records each row's own mode", func() {
			rows := []FlaggedRow{
				rowWith("agent", "headless", "h"),
				rowWith("human", "interactive", "i"),
			}
			caps := &recordingCaps{}
			_, err := newTick(staticSource{rows: rows}, &recordingReopener{}, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			modes := caps.modes()
			ginkgo.GinkgoWriter.Printf("modes=%v\n", modes)
			gomega.Expect(modes).To(gomega.ConsistOf("headless", "interactive"))
			gomega.Expect(modes).NotTo(gomega.ContainElement(""))
		})

		// The cap is charged against the mode the row will OPEN in, not the stale
		// one on the record. An agent row stored `interactive` opens headless
		// (matrix Rule 2), so charging it to the interactive sub-cap refused it —
		// and halted the batch — whenever that cap sat at its limit.
		ginkgo.It("charges an agent row stored interactive to the headless cap", func() {
			rows := []FlaggedRow{rowWith("agent", "interactive", "stale")}
			caps := &recordingCaps{}
			_, err := newTick(staticSource{rows: rows}, &recordingReopener{}, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(caps.modes()).To(gomega.ConsistOf("headless"))
		})

		ginkgo.It("keeps a human row's stored mode", func() {
			rows := []FlaggedRow{rowWith("human", "interactive", "h")}
			caps := &recordingCaps{}
			_, err := newTick(staticSource{rows: rows}, &recordingReopener{}, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(caps.modes()).To(gomega.ConsistOf("interactive"))
		})
	})

	// A row that reaches the cap check has already passed the provenance gate, so
	// it counts as operator-named (operator ruling 2026-11-17). These tests run the
	// REAL cap enforcer over a scratch config, so the soft and hard limits are the
	// ones the daemon reads, not a fake that would pass whatever it was handed.
	ginkgo.Describe("asserts the row's operator provenance to the caps", func() {
		// liveHeadlessFleet is a fleet of n live headless workers, built the way the
		// cap enforcer counts one: a ledger row per session, each marked live by the
		// heartbeat store.
		liveHeadlessFleet := func(n int) (LedgerReader, HeartbeatReader) {
			rows := make([]LedgerRow, n)
			beats := make([]HeartbeatRow, n)
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("live-%d", i)
				rows[i] = LedgerRow{SessionID: id, Mode: SpawnModeHeadless, Role: RoleAgent}
				beats[i] = liveBeat(id)
			}
			return fakeLedgerRows{rows: rows}, &stubHeartbeats{rows: beats}
		}

		ginkgo.It("opens an operator-flagged row at the soft cap, below the hard cap", func() {
			// soft 2 / hard 3 make the headless axis 1 soft and 2 hard.
			ledger, heartbeats := liveHeadlessFleet(1)
			caps := testCaps(ledger, heartbeats, writeSpawnConfig(workerAxisConfig(2, 3)),
				ginkgo.GinkgoT().TempDir())
			reopener := &recordingReopener{}
			// Refused is empty: the predicate attributed this flag to an opening actor.
			result, err := newTick(
				staticSource{rows: []FlaggedRow{rowWith("agent", "headless", "flagged")}},
				reopener, caps,
			).Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("opened=%v refused=%v\n", result.Opened, result.Refused)
			gomega.Expect(reopener.snapshot()).To(gomega.HaveLen(1))
			gomega.Expect(result.Opened).To(gomega.HaveLen(1))
			gomega.Expect(result.Refused).To(gomega.BeEmpty())
		})

		ginkgo.It("refuses the same row at the hard cap, naming the hard axis", func() {
			ledger, heartbeats := liveHeadlessFleet(2)
			caps := testCaps(ledger, heartbeats, writeSpawnConfig(workerAxisConfig(2, 3)),
				ginkgo.GinkgoT().TempDir())
			reopener := &recordingReopener{}
			result, err := newTick(
				staticSource{rows: []FlaggedRow{rowWith("agent", "headless", "flagged")}},
				reopener, caps,
			).Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("refused=%v\n", result.Refused)
			gomega.Expect(reopener.snapshot()).To(gomega.BeEmpty())
			gomega.Expect(result.Opened).To(gomega.BeEmpty())
			gomega.Expect(result.Refused).To(gomega.HaveLen(1))
			gomega.Expect(result.Refused[0]).To(gomega.ContainSubstring(reasonCapsRefused))
			// The hard cap's own key, not the soft one: the refusal must say which
			// axis refused an operator-named row.
			gomega.Expect(result.Refused[0]).To(gomega.ContainSubstring("spawn.maxHeadlessHard"))
		})

		ginkgo.It("refuses a provenance-refused row before any cap check", func() {
			// The fleet is AT the hard cap, so a cap check that ran first would be the
			// reason recorded. The provenance reason proves the ordering.
			ledger, heartbeats := liveHeadlessFleet(2)
			caps := testCaps(ledger, heartbeats, writeSpawnConfig(workerAxisConfig(2, 3)),
				ginkgo.GinkgoT().TempDir())
			reopener := &recordingReopener{}
			row := rowWith("agent", "headless", "agent-set")
			row.Refused = []string{"agent-set-flag"}
			result, err := newTick(staticSource{rows: []FlaggedRow{row}}, reopener, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("refused=%v\n", result.Refused)
			gomega.Expect(reopener.snapshot()).To(gomega.BeEmpty())
			gomega.Expect(result.Refused).To(gomega.HaveLen(1))
			gomega.Expect(result.Refused[0]).To(gomega.ContainSubstring("agent-set-flag"))
			gomega.Expect(result.Refused[0]).NotTo(gomega.ContainSubstring(reasonCapsRefused))
		})

		ginkgo.It("still skips a LIVE row without asking the caps at all", func() {
			caps := &recordingCaps{}
			row := rowWith("agent", "headless", "live")
			row.Liveness = "LIVE"
			result, err := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("skipped=%v caps=%v\n", result.Skipped, caps.modes())
			gomega.Expect(result.Opened).To(gomega.BeEmpty())
			gomega.Expect(result.Skipped).To(gomega.HaveLen(1))
			gomega.Expect(result.Skipped[0]).To(gomega.ContainSubstring(reasonNotAbsent))
			gomega.Expect(caps.modes()).To(gomega.BeEmpty())
		})
	})

	ginkgo.Describe("cancels a slow predicate", func() {
		ginkgo.It("returns context.Canceled rather than hanging", func() {
			ctx, cancel := context.WithCancel(context.Background())
			tick := newTick(blockingSource{}, &recordingReopener{}, &recordingCaps{})
			tick.Interval = 10 * time.Millisecond
			done := make(chan error, 1)
			go func() { done <- tick.Run(ctx) }()
			time.Sleep(100 * time.Millisecond)
			cancel()
			var err error
			gomega.Eventually(done, time.Second).Should(gomega.Receive(&err))
			ginkgo.GinkgoWriter.Printf("err=%v\n", err)
			gomega.Expect(stderrors.Is(err, context.Canceled)).To(gomega.BeTrue())
		})
	})

	ginkgo.Describe("abandons a hanging predicate", func() {
		ginkgo.It("keeps ticking after an abandoned read", func() {
			source := &countingSource{block: true}
			tick := newTick(source, &recordingReopener{}, &recordingCaps{})
			tick.Interval = 10 * time.Millisecond
			tick.ReadTimeout = 5 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			go func() { _ = tick.Run(ctx) }()
			gomega.Eventually(source.count, time.Second).Should(gomega.BeNumerically(">=", 2))
			cancel()
		})
	})

	ginkgo.Describe("reads the predicate", func() {
		ginkgo.It("runs the documented argv and decodes every consumed field", func() {
			runner := &recordingRunner{out: []byte(`{"rows":[{
				"kind":"task","vault":"v","name":"n","role":"human","mode":"headless",
				"session_id":"s","liveness":"ABSENT","branch":"resume",
				"blocked_by":["[[B]]"],"refused":["agent-set-flag"]}]}`)}
			source := ExecFlaggedRowsSource{Script: "/tmp/fr.py", Runner: runner}
			rows, err := source.Rows(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("argv=%v rows=%d\n", runner.argv, len(rows))
			gomega.Expect(runner.argv).To(gomega.Equal([]string{"python3", "/tmp/fr.py", "--json", "--no-pane"}))
			gomega.Expect(rows).To(gomega.HaveLen(1))
			gomega.Expect(rows[0].Kind).To(gomega.Equal("task"))
			gomega.Expect(rows[0].Vault).To(gomega.Equal("v"))
			gomega.Expect(rows[0].Name).To(gomega.Equal("n"))
			gomega.Expect(rows[0].Role).To(gomega.Equal("human"))
			gomega.Expect(rows[0].Mode).To(gomega.Equal("headless"))
			gomega.Expect(rows[0].SessionID).To(gomega.Equal("s"))
			gomega.Expect(rows[0].Liveness).To(gomega.Equal("ABSENT"))
			gomega.Expect(rows[0].Branch).To(gomega.Equal("resume"))
			gomega.Expect(rows[0].BlockedBy).To(gomega.HaveLen(1))
			gomega.Expect(rows[0].Refused).To(gomega.HaveLen(1))
		})

		ginkgo.It("returns an error on a truncated payload", func() {
			runner := &recordingRunner{out: []byte(`{"rows":[{"kind":`)}
			source := ExecFlaggedRowsSource{Script: "/tmp/fr.py", Runner: runner}
			rows, err := source.Rows(context.Background())
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(rows).To(gomega.BeNil())
		})
	})

	ginkgo.Describe("reopens a row through the spawn path its mode names", func() {
		// ⚠️ Regression: the tick used to hand the tab spawner a request with no
		// `prompt`, which it refuses before any side effect. Every eligible row
		// therefore failed with `prompt is required: invalid spawn options` and
		// `opened` was zero for the tick's entire history. These cases assert the
		// request the spawner actually validates, not that a fake was called.
		newReopener := func(
			supervisor *recordingSupervisor, spawner *recordingSpawner, ledger *recordingLedger,
		) FlaggedReopener {
			projectsDir := ginkgo.GinkgoT().TempDir()
			// A stamped session resumes only when its transcript exists on disk
			// (see resumableSessionID). The fixture writes one under the vault's
			// slug so this case exercises the resume path, not the re-create path.
			slug := strings.NewReplacer("/", "-", ".", "-").
				Replace("/tmp/vault-private-personal")
			gomega.Expect(os.MkdirAll(filepath.Join(projectsDir, slug), 0o755)).
				To(gomega.Succeed())
			gomega.Expect(os.WriteFile(
				filepath.Join(projectsDir, slug, "abc-123.jsonl"), []byte("{}\n"), 0o600,
			)).To(gomega.Succeed())
			return &flaggedReopener{
				Supervisor:  supervisor,
				Spawner:     spawner,
				Vaults:      testVaults(),
				Ledger:      ledger,
				Now:         fixedNow(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)),
				ProjectsDir: projectsDir,
			}
		}

		ginkgo.It("resumes a headless row by its session id and sends the work turn", func() {
			supervisor := &recordingSupervisor{sessionID: "resumed"}
			spawner := &recordingSpawner{}
			ledger := &recordingLedger{}
			row := rowWith("agent", "headless", "Some Task")
			row.SessionID = "abc-123"
			gomega.Expect(newReopener(supervisor, spawner, ledger).Reopen(context.Background(), row)).
				To(gomega.BeNil())

			spawns := supervisor.snapshot()
			sent := supervisor.sent()
			ginkgo.GinkgoWriter.Printf("resume=%q sent=%v tabSpawns=%d ledger=%d\n",
				spawns[0].Resume, sent, len(spawner.snapshot()), len(ledger.snapshot()))
			gomega.Expect(spawns).To(gomega.HaveLen(1))
			gomega.Expect(spawns[0].Resume).To(gomega.Equal("abc-123"))
			gomega.Expect(spawner.snapshot()).To(gomega.BeEmpty())
			gomega.Expect(sent).To(gomega.HaveLen(1))
			gomega.Expect(sent[0]).To(gomega.ContainSubstring(`/vault-cli:work-on-task "Some Task"`))
			// ⚠️ A headless reopen that writes NOTHING is invisible to the caps AND
			// to the tick's own recently-spawned guard, so the next tick would open a
			// second copy of a row whose child is still starting. The write is a
			// PATCH now: a POST would replace the row and blank the spawner that made
			// the worker's parks answerable (measured 2026-11-26 — 320 of 2749
			// sessions in the wrapper log lost their spawner this way).
			gomega.Expect(ledger.snapshot()).To(gomega.BeEmpty())
			patches := ledger.patchSnapshot()
			gomega.Expect(patches).To(gomega.HaveLen(1))
			gomega.Expect(string(patches[0]["resumed_from"])).To(gomega.Equal(`"abc-123"`))
			gomega.Expect(string(patches[0]["role"])).To(gomega.Equal(`"agent"`))
			gomega.Expect(string(patches[0]["mode"])).To(gomega.Equal(`"headless"`))
			// The spawn edge and the original spawn time survive the resume.
			gomega.Expect(patches[0]).NotTo(gomega.HaveKey("spawner_session_id"))
			gomega.Expect(patches[0]).NotTo(gomega.HaveKey("spawned_at"))
		})

		ginkgo.It("creates a headless row that has no session id", func() {
			supervisor := &recordingSupervisor{}
			row := rowWith("agent", "headless", "Some Task")
			gomega.Expect(newReopener(supervisor, &recordingSpawner{}, &recordingLedger{}).
				Reopen(context.Background(), row)).To(gomega.BeNil())

			spawns := supervisor.snapshot()
			gomega.Expect(spawns).To(gomega.HaveLen(1))
			gomega.Expect(spawns[0].Resume).To(gomega.BeEmpty())
			gomega.Expect(supervisor.sent()).To(gomega.HaveLen(1))
		})

		ginkgo.It("hands a tab row a non-empty prompt", func() {
			supervisor := &recordingSupervisor{}
			spawner := &recordingSpawner{}
			row := rowWith("human", "interactive", "Direct Task")
			gomega.Expect(newReopener(supervisor, spawner, &recordingLedger{}).Reopen(context.Background(), row)).
				To(gomega.BeNil())

			requests := spawner.snapshot()
			ginkgo.GinkgoWriter.Printf("prompt=%q\n", requests[0].Prompt)
			gomega.Expect(requests).To(gomega.HaveLen(1))
			gomega.Expect(requests[0].Prompt).
				To(gomega.HavePrefix("FIRST STEP — readiness gate"))
			gomega.Expect(requests[0].Prompt).
				To(gomega.ContainSubstring(`/vault-cli:work-on-task "Direct Task"`))
			gomega.Expect(supervisor.snapshot()).To(gomega.BeEmpty())
		})

		ginkgo.It("stops a headless child whose opening turn could not be delivered", func() {
			supervisor := &recordingSupervisor{
				sessionID: "orphan",
				sendErr:   stderrors.New("stdin closed"),
			}
			row := rowWith("agent", "headless", "Some Task")
			err := newReopener(supervisor, &recordingSpawner{}, &recordingLedger{}).
				Reopen(context.Background(), row)
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(supervisor.stopped).To(gomega.ConsistOf("orphan"))
		})
	})

	ginkgo.Describe("skips a recently spawned row", func() {
		ginkgo.It("does not open a session spawned inside the window", func() {
			at := time.Date(2026, 11, 11, 11, 55, 0, 0, time.UTC)
			row := rowWith("agent", "headless", "fresh")
			row.SessionID = "fresh"
			tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, &recordingCaps{})
			tick.Ledger = ledgerWith{rows: []LedgerRow{
				{SessionID: "fresh", SpawnedAt: at.Format(time.RFC3339)},
			}}
			result, err := tick.Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("opened=%d skipped=%d\n",
				len(result.Opened), len(result.Skipped))
			gomega.Expect(result.Opened).To(gomega.BeEmpty())
			gomega.Expect(result.Skipped).To(gomega.HaveLen(1))
		})
	})
})

// fakeLedgerRows serves a fixed ledger row set to the recently-spawned guard.
type fakeLedgerRows struct{ rows []LedgerRow }

func (f fakeLedgerRows) Rows(context.Context) ([]LedgerRow, error) { return f.rows, nil }

func (fakeLedgerRows) Post(context.Context, string, LedgerRow) error { return nil }

func (fakeLedgerRows) Patch(context.Context, string, LedgerPatch) error { return nil }

var _ = ginkgo.Describe("flagged tick recently-spawned guard by label", func() {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	fresh := FlaggedRow{
		Name: "fresh", Kind: "task", Liveness: "none", Branch: "create",
		Role: "agent", Mode: "headless",
	}

	ginkgo.It("skips a never-opened row the ledger already spawned under its name", func() {
		tick := newTick(staticSource{rows: []FlaggedRow{fresh}}, &recordingReopener{}, &recordingCaps{})
		tick.Now = fixedNow(now)
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: "new-session", Label: "fresh",
			SpawnedAt: now.Add(-2 * time.Minute).Format(time.RFC3339),
		}}}
		gomega.Expect(recentlySpawned(tick.ledgerRows(context.Background()))).
			To(gomega.HaveKey(labelKey("fresh")))
		result, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.BeEmpty())
	})

	ginkgo.It("does not open a never-opened row twice when no ledger row was filed", func() {
		reopener := &recordingReopener{}
		tick := newTick(staticSource{rows: []FlaggedRow{fresh}}, reopener, &recordingCaps{})
		tick.Now = fixedNow(now)

		first, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(first.Opened).To(gomega.HaveLen(1))

		second, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(second.Opened).To(gomega.BeEmpty())
		gomega.Expect(reopener.snapshot()).To(gomega.HaveLen(1))
	})
})

// The tick line is the only evidence a 24 h liveness measurement has. These
// cases pin the two properties that make it usable: every row's reason is
// recoverable (not the first five), and every considered row is accounted for
// (a caps refusal does not drop the tail of the row set).
var _ = ginkgo.Describe("flagged tick log line", func() {
	// detailSegment returns the text between `name` and the next class marker.
	detailSegment := func(detail, name string) string {
		_, rest, ok := strings.Cut(detail, name)
		if !ok {
			return ""
		}
		segment, _, _ := strings.Cut(rest, " skipped=")
		return segment
	}

	// countEntries counts `key: reason` pairs in one detail segment.
	countEntries := func(segment string) int {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return 0
		}
		return len(strings.Split(segment, "; "))
	}

	ginkgo.It("names every refused and skipped row, not the first five", func() {
		rows := make([]FlaggedRow, 0, 13)
		for i := range 9 {
			rows = append(rows, FlaggedRow{
				Kind: "task", Vault: "v", Name: fmt.Sprintf("refused-%d", i),
				Liveness: "ABSENT", Refused: []string{"agent-set-flag"},
			})
		}
		for i := range 4 {
			rows = append(rows, FlaggedRow{
				Kind: "task", Vault: "v", Name: fmt.Sprintf("skipped-%d", i), Liveness: "LIVE",
			})
		}
		result, err := newTick(staticSource{rows: rows}, &recordingReopener{}, &recordingCaps{}).
			Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		summary, detail := result.Line(), result.Detail()
		ginkgo.GinkgoWriter.Printf("summary=%s\ndetail=%s\n", summary, detail)

		gomega.Expect(summary).To(gomega.ContainSubstring("refused=9"))
		gomega.Expect(summary).To(gomega.ContainSubstring("skipped=4"))
		gomega.Expect(summary).To(gomega.ContainSubstring("refused_by_reason=agent-set-flag:9"))
		gomega.Expect(summary).To(gomega.ContainSubstring("skipped_by_reason=not-absent:4"))

		refused := detailSegment(detail, "refused=")
		gomega.Expect(countEntries(refused)).To(gomega.Equal(9))
		for i := range 9 {
			gomega.Expect(detail).To(gomega.ContainSubstring(fmt.Sprintf("refused-%d", i)))
		}
		for i := range 4 {
			gomega.Expect(detail).To(gomega.ContainSubstring(fmt.Sprintf("skipped-%d", i)))
		}
	})

	ginkgo.It("names the three provenance classes in one tick line", func() {
		// SC2: an operator-set row is opened with no marker of its own, so the
		// refusal tokens alone cannot distinguish the three states. The counts can.
		result := FlaggedTickResult{
			Considered: 3,
			ProvenanceCounts: map[Provenance]int{
				ProvenanceOperator:   1,
				ProvenanceAgent:      1,
				ProvenanceUnrecorded: 1,
			},
		}
		gomega.Expect(result.Line()).
			To(gomega.ContainSubstring("provenance=operator:1; agent:1; unrecorded:1"))
		ginkgo.GinkgoWriter.Printf("provenance line=%s\n", result.Line())
	})

	ginkgo.It("counts provenance per class from the row set", func() {
		rows := []FlaggedRow{
			{Kind: "task", Vault: "v", Name: "op", Liveness: "ABSENT", Provenance: ProvenanceOperator},
			{Kind: "task", Vault: "v", Name: "ag", Liveness: "ABSENT",
				Provenance: ProvenanceAgent, Refused: []string{RefusalAgentSetFlag}},
			{Kind: "task", Vault: "v", Name: "un", Liveness: "ABSENT",
				Provenance: ProvenanceUnrecorded, Refused: []string{RefusalProvenanceUnrecorded}},
		}
		result, err := newTick(staticSource{rows: rows}, &recordingReopener{}, &recordingCaps{}).
			Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Line()).
			To(gomega.ContainSubstring("provenance=operator:1; agent:1; unrecorded:1"))
		ginkgo.GinkgoWriter.Printf("row-set line=%s\n", result.Line())
	})

	ginkgo.It("accounts for every row when a caps refusal ends the opening pass", func() {
		rows := []FlaggedRow{
			rowWith("agent", "headless", "one"),
			rowWith("agent", "headless", "two"),
			rowWith("agent", "headless", "three"),
		}
		caps := &recordingCaps{workerErr: stderrors.New("maxConcurrent reached")}
		result, err := newTick(staticSource{rows: rows}, &recordingReopener{}, caps).
			Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		summary := result.Line()
		ginkgo.GinkgoWriter.Printf("summary=%s\ndetail=%s\n", summary, result.Detail())

		gomega.Expect(summary).To(gomega.ContainSubstring("considered=3"))
		gomega.Expect(len(result.Opened) + len(result.Refused) + len(result.Skipped)).
			To(gomega.Equal(3))
		gomega.Expect(result.Detail()).To(gomega.ContainSubstring("two"))
		gomega.Expect(result.Detail()).To(gomega.ContainSubstring("three"))
	})

	ginkgo.It("opens the named row's shape and accounts for it", func() {
		row := FlaggedRow{
			Kind: "task", Vault: "private-personal",
			Name: "AgentUnknownAssignee Fires on Records Whose Assignee Is a Task Type",
			Role: "agent", Mode: "headless",
			SessionID: "abc-123", Liveness: "ABSENT", Branch: "resume",
		}
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, &recordingCaps{})
		tick.Ledger = ledgerWith{}
		result, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("summary=%s\n", result.Line())

		gomega.Expect(result.Opened).To(gomega.ConsistOf("abc-123"))
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("considered=1"))
	})

	ginkgo.Describe("survives a killed predicate read", func() {
		// The failure mode from 2026-11-16: one read killed by the 25-minute
		// predicateTimeout left the cache carrying the error, and every later tick
		// returned early on it — 18 consecutive dead ticks, four operator-flagged
		// rows opening 2 h 01 m late. A kill is an expected outcome of the bound;
		// it must cost freshness, not the tick.
		killed := stderrors.New("run python3 failed: signal: killed")

		ginkgo.It("keeps the last good row set when a read fails", func() {
			cache := &flaggedRowCache{}
			cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 0, time.Now())
			cache.store(nil, killed, 0, time.Now())

			rows, _, ok, stale := cache.snapshot()
			ginkgo.GinkgoWriter.Printf("rows=%d stale=%v ok=%t\n", len(rows), stale, ok)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(rows).To(gomega.HaveLen(1))
			gomega.Expect(stale).NotTo(gomega.BeNil())
		})

		ginkgo.It("still runs the tick after the read is killed", func() {
			reopener := &recordingReopener{}
			tick := newTick(nil, reopener, &recordingCaps{})
			var lines []string
			tick.Logf = func(format string, args ...any) {
				lines = append(lines, fmt.Sprintf(format, args...))
			}
			cache := &flaggedRowCache{}

			cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 0, time.Now())
			tick.tickAndLog(context.Background(), cache)

			cache.store(nil, killed, 0, time.Now())
			before := len(lines)
			tick.tickAndLog(context.Background(), cache)
			after := strings.Join(lines[before:], "\n")
			ginkgo.GinkgoWriter.Printf("lines-after-kill=%s\n", after)

			// A tick line that CONSIDERED the row, not merely one that was emitted:
			// with the sticky error the cache carries no rows, so the tick still
			// logs `opened=0 refused=0 skipped=0` and would satisfy a weaker
			// assertion while serving nothing.
			gomega.Expect(after).To(gomega.ContainSubstring("read failed"))
			gomega.Expect(after).To(gomega.ContainSubstring("considered=1"))
			gomega.Expect(after).NotTo(gomega.ContainSubstring("flagged-tick failed:"))
		})

		ginkgo.It("keeps logging skipped when the FIRST read fails", func() {
			tick := newTick(nil, &recordingReopener{}, &recordingCaps{})
			var lines []string
			tick.Logf = func(format string, args ...any) {
				lines = append(lines, fmt.Sprintf(format, args...))
			}
			cache := &flaggedRowCache{}
			cache.store(nil, killed, 0, time.Now())
			tick.tickAndLog(context.Background(), cache)

			// No good row set yet, so there is nothing to serve: `skipped` stays
			// honest rather than fabricating an empty set — but the read's own
			// error is still named, or a first read that always fails would hide
			// behind `skipped` forever.
			gomega.Expect(strings.Join(lines, "\n")).To(gomega.ContainSubstring("skipped"))
			gomega.Expect(strings.Join(lines, "\n")).To(gomega.ContainSubstring("read failed"))
			gomega.Expect(strings.Join(lines, "\n")).NotTo(gomega.ContainSubstring("opened="))
		})

		ginkgo.It("reports the read duration on the tick line", func() {
			tick := newTick(nil, &recordingReopener{}, &recordingCaps{})
			var lines []string
			tick.Logf = func(format string, args ...any) {
				lines = append(lines, fmt.Sprintf(format, args...))
			}
			cache := &flaggedRowCache{}
			cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 95*time.Second, time.Now())
			tick.tickAndLog(context.Background(), cache)

			joined := strings.Join(lines, "\n")
			ginkgo.GinkgoWriter.Printf("lines=%s\n", joined)
			// The read-cost measurement is a log grep, so the field has to be on the
			// tick line itself and carry the read's real cost — the tick's own
			// elapsed time would be a different number and would hide a slow read.
			gomega.Expect(joined).To(gomega.ContainSubstring("read_duration=1m35s"))
		})
	})
})

var _ = ginkgo.Describe("flagged tick resume delay", func() {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	// A RESUMED row: it has a session, its liveness read ABSENT, and the branch
	// sends it back through resume.
	row := FlaggedRow{
		Kind: "task", Vault: "private-personal", Name: "a flagged row",
		Role: "agent", Mode: "headless",
		SessionID: "abc-123", Liveness: "ABSENT", Branch: "resume",
	}

	ginkgo.It("measures the delay from the ledger's ended_at even on a TERMINAL row", func() {
		// The trap this guards: `recentlySpawned` skips terminal ledger rows, so a
		// reader that reused that filter would find no ended_at for any row being
		// resumed — every real row is terminal by definition, and the metric would
		// read as "never measurable" on the fleet it exists to measure.
		ended := now.Add(-8 * time.Minute).Format(time.RFC3339)
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, &recordingCaps{})
		tick.Now = fixedNow(now)
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: "abc-123", Status: "done",
			SpawnedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
			EndedAt:   &ended,
		}}}

		result, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("summary=%s\n", result.Line())

		gomega.Expect(result.Opened).To(gomega.ConsistOf("abc-123"))
		gomega.Expect(result.ResumeDelays).To(gomega.ConsistOf("abc-123=8m0s"))
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("resume_delay=abc-123=8m0s"))
	})

	ginkgo.It("omits the delay when the ledger carries no ended_at", func() {
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, &recordingCaps{})
		tick.Now = fixedNow(now)
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: "abc-123", Status: "done",
			SpawnedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
		}}}

		result, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(result.Opened).To(gomega.ConsistOf("abc-123"))
		// Absent, never zero: a zero would read as an instant resume.
		gomega.Expect(result.ResumeDelays).To(gomega.BeEmpty())
		gomega.Expect(result.Line()).NotTo(gomega.ContainSubstring("resume_delay="))
	})
})

// tickParkedCards is this file's fake OpenCardReader. The name is deliberately
// specific so it cannot collide with the card fake another test file defines,
// and both the open set and the error are settable between ticks.
type tickParkedCards struct {
	ids map[string]struct{}
	err error
}

func (c *tickParkedCards) OpenSessionIDs(context.Context) (map[string]struct{}, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.ids, nil
}

// parkedRow builds an ABSENT row with a UUID session id, the shape the park
// gate reads a closer for.
func parkedRow(sessionID, name string) FlaggedRow {
	return FlaggedRow{
		Kind: "task", Vault: "private-personal", Name: name,
		Role: "agent", Mode: "headless",
		SessionID: sessionID, Liveness: "ABSENT", Branch: "resume",
	}
}

// newTickWithParkedGate builds a tick wired to the REAL park gate over real temp
// sessions and parked directories, writing a real events.jsonl for every row's
// session id. Only the card reader and the `gh` runner are fakes.
func newTickWithParkedGate(
	rows []FlaggedRow,
	closers map[string]string,
	cards OpenCardReader,
	runner CommandRunner,
	clock libtime.CurrentDateTimeGetter,
) *FlaggedTick {
	sessionsDir := ginkgo.GinkgoT().TempDir()
	parkedDir := ginkgo.GinkgoT().TempDir()
	for _, row := range rows {
		if row.SessionID == "" {
			continue
		}
		writeSessionCloser(sessionsDir, row.SessionID, closers[row.SessionID])
	}
	tick := newTick(staticSource{rows: rows}, &recordingReopener{}, &recordingCaps{})
	tick.Now = clock
	tick.Parked = NewParkedGate(sessionsDir, parkedDir, cards, runner, clock)
	return tick
}

// newTickWithParkedGateAndCaps is newTickWithParkedGate with the tick's reopener
// and caps supplied by the caller, so one spec can make the tick refuse a row and
// then open the same row on a later tick against the same park gate.
func newTickWithParkedGateAndCaps(
	rows []FlaggedRow,
	closers map[string]string,
	cards OpenCardReader,
	runner CommandRunner,
	clock libtime.CurrentDateTimeGetter,
	reopener *recordingReopener,
	caps *recordingCaps,
) *FlaggedTick {
	sessionsDir := ginkgo.GinkgoT().TempDir()
	parkedDir := ginkgo.GinkgoT().TempDir()
	for _, row := range rows {
		if row.SessionID == "" {
			continue
		}
		writeSessionCloser(sessionsDir, row.SessionID, closers[row.SessionID])
	}
	tick := newTick(staticSource{rows: rows}, reopener, caps)
	tick.Now = clock
	tick.Parked = NewParkedGate(sessionsDir, parkedDir, cards, runner, clock)
	return tick
}

// keyedParkedHold is a stub ParkedHold that holds exactly one row key.
type keyedParkedHold struct {
	key string
}

func (h keyedParkedHold) Hold(_ context.Context, row FlaggedRow) (string, bool) {
	if row.Key() == h.key {
		return string(ParkedOnCard), true
	}
	return "", false
}

// Evaluate is Hold without the record: this stub writes nothing, so the two agree.
func (h keyedParkedHold) Evaluate(ctx context.Context, row FlaggedRow) (string, bool) {
	return h.Hold(ctx, row)
}

// Opened is the no-op half of the interface: this stub never produces a pending
// baseline, so there is nothing for a successful reopen to commit.
func (h keyedParkedHold) Opened(_ context.Context, _ FlaggedRow) {}

// runPRTicks drives one tick four times against one pull request observable:
// the first records the baseline, the second repeats the same reading, the third
// moves the head, and the fourth repeats the moved reading.
//
// ⚠️ The fourth tick is the regression guard. Committing the advanced baseline
// only after a successful open is what makes tick 4 hold; recording it inside
// Hold made tick 4 hold too, but a row refused by caps in between never retried.
func runPRTicks(tick *FlaggedTick, runner *recordingRunner) []FlaggedTickResult {
	ctx := context.Background()
	runner.out = []byte(`{"headRefOid":"aaa","reviewDecision":"REVIEW_REQUIRED","state":"OPEN"}`)
	first, _ := tick.Tick(ctx)
	second, _ := tick.Tick(ctx)
	runner.out = []byte(`{"headRefOid":"bbb","reviewDecision":"REVIEW_REQUIRED","state":"OPEN"}`)
	third, _ := tick.Tick(ctx)
	fourth, _ := tick.Tick(ctx)
	return []FlaggedTickResult{first, second, third, fourth}
}

var _ = ginkgo.Describe("flagged tick park gate", func() {
	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	ginkgo.It("holds a row parked on an unanswered card until the card is answered", func() {
		sessionID := "123e4567-e89b-12d3-a456-426614174001"
		row := parkedRow(sessionID, "card row")
		cards := &tickParkedCards{ids: map[string]struct{}{sessionID: {}}}
		tick := newTickWithParkedGate(
			[]FlaggedRow{row},
			map[string]string{sessionID: "⏰ Ends: card 3f9a1c7e answered"},
			cards, &recordingRunner{}, fixedNow(base),
		)

		first, err := tick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fmt.Printf("tick=1 %s\n", first.Line())
		gomega.Expect(first.Opened).To(gomega.BeEmpty())
		gomega.Expect(first.Line()).To(gomega.ContainSubstring("skipped_by_reason=parked-on-card:1"))

		cards.ids = map[string]struct{}{}
		second, err := tick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fmt.Printf("tick=2 %s\n", second.Line())
		gomega.Expect(second.Opened).To(gomega.ConsistOf(sessionID))
	})

	ginkgo.It("holds a row parked on a pull request until the pull request changes", func() {
		firstID := "123e4567-e89b-12d3-a456-426614174002"
		runner := &recordingRunner{}
		tick := newTickWithParkedGate(
			[]FlaggedRow{parkedRow(firstID, "pr row")},
			map[string]string{firstID: "⏰ Ends: bborbe/claude-wrapper#412 merges"},
			nil, runner, fixedNow(base),
		)
		results := runPRTicks(tick, runner)
		for n, result := range results {
			fmt.Printf("tick=%d %s\n", n+1, result.Line())
		}
		argv := strings.Join(runner.argv[1:], " ")
		fmt.Printf("argv=%s\n", argv)
		gomega.Expect(results[0].Opened).To(gomega.BeEmpty())
		gomega.Expect(results[1].Opened).To(gomega.BeEmpty())
		gomega.Expect(results[2].Opened).To(gomega.ConsistOf(firstID))
		// The moved reading repeats, so the row holds again: the advanced baseline
		// was committed by the successful open above.
		gomega.Expect(results[3].Opened).To(gomega.BeEmpty())
		gomega.Expect(argv).To(gomega.Equal(
			"pr view 412 --repo bborbe/claude-wrapper --json headRefOid,reviewDecision,state",
		))

		secondID := "123e4567-e89b-12d3-a456-426614174003"
		other := &recordingRunner{}
		otherTick := newTickWithParkedGate(
			[]FlaggedRow{parkedRow(secondID, "other pr row")},
			map[string]string{secondID: "⏰ Ends: bborbe/other-repo#9 merges"},
			nil, other, fixedNow(base),
		)
		otherResults := runPRTicks(otherTick, other)
		otherArgv := strings.Join(other.argv[1:], " ")
		fmt.Printf("other-argv=%s\n", otherArgv)
		gomega.Expect(otherResults[2].Opened).To(gomega.ConsistOf(secondID))
		gomega.Expect(otherArgv).To(gomega.Equal(
			"pr view 9 --repo bborbe/other-repo --json headRefOid,reviewDecision,state",
		))

		// A terminal state satisfies the wait on its own, whatever the head is.
		for _, state := range []string{"MERGED", "CLOSED"} {
			other.out = []byte(fmt.Sprintf(
				`{"headRefOid":"ccc-%s","reviewDecision":"","state":%q}`, state, state,
			))
			result, err := otherTick.Tick(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			fmt.Printf("state=%s opened=%t\n", state, len(result.Opened) == 1)
			gomega.Expect(result.Opened).To(gomega.HaveLen(1))
		}
	})

	ginkgo.It("retries a moved park the caps refused instead of committing it", func() {
		// The trap this guards: Hold runs BEFORE the caps and rate guards, so a
		// movement committed inside Hold would be written by a row that was then
		// refused — the old fingerprint gone, and a `parked-on-pr` row (no expiry)
		// held on the moved reading for good. The pending baseline makes the
		// refusal cost one tick, not the row.
		sessionID := "123e4567-e89b-12d3-a456-426614174004"
		runner := &recordingRunner{}
		reopener := &recordingReopener{}
		caps := &recordingCaps{}
		tick := newTickWithParkedGateAndCaps(
			[]FlaggedRow{parkedRow(sessionID, "caps-refused pr row")},
			map[string]string{sessionID: "⏰ Ends: bborbe/claude-wrapper#412 merges"},
			nil, runner, fixedNow(base), reopener, caps,
		)

		runner.out = []byte(`{"headRefOid":"aaa","reviewDecision":"REVIEW_REQUIRED","state":"OPEN"}`)
		first, err := tick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fmt.Printf("tick=1 %s\n", first.Line())
		gomega.Expect(first.Opened).To(gomega.BeEmpty())
		gomega.Expect(first.Skipped).To(gomega.HaveLen(1))
		gomega.Expect(reasonClass(first.Skipped[0])).To(gomega.Equal(string(ParkedOnPR)))
		gomega.Expect(reopener.snapshot()).To(gomega.BeEmpty())

		// The head moves, but the caps refuse the row: nothing opens, and the
		// advanced reading must stay pending rather than being committed.
		runner.out = []byte(`{"headRefOid":"bbb","reviewDecision":"REVIEW_REQUIRED","state":"OPEN"}`)
		caps.workerErr = stderrors.New("maxConcurrent reached")
		second, err := tick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fmt.Printf("tick=2 %s\n", second.Line())
		gomega.Expect(second.Opened).To(gomega.BeEmpty())
		gomega.Expect(second.Refused).To(gomega.HaveLen(1))
		gomega.Expect(reasonClass(second.Refused[0])).To(gomega.Equal(reasonCapsRefused))
		gomega.Expect(reopener.snapshot()).To(gomega.BeEmpty())

		// Caps clear: the SAME moved reading opens, which proves the old fingerprint
		// survived the refusal and the movement was re-detected on this tick.
		caps.workerErr = nil
		third, err := tick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fmt.Printf("tick=3 %s\n", third.Line())
		gomega.Expect(third.Opened).To(gomega.ConsistOf(sessionID))
		gomega.Expect(third.Refused).To(gomega.BeEmpty())
		gomega.Expect(reopener.snapshot()).To(gomega.HaveLen(1))
		gomega.Expect(reopener.snapshot()[0].Key()).To(gomega.Equal(sessionID))
	})

	ginkgo.It("resolves a host and a clock park", func() {
		live, err := net.Listen("tcp", "127.0.0.1:0")
		gomega.Expect(err).To(gomega.BeNil())
		defer func() { _ = live.Close() }()
		liveAddr, ok := live.Addr().(*net.TCPAddr)
		gomega.Expect(ok).To(gomega.BeTrue())

		dead, err := net.Listen("tcp", "127.0.0.1:0")
		gomega.Expect(err).To(gomega.BeNil())
		deadAddr, ok := dead.Addr().(*net.TCPAddr)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(dead.Close()).To(gomega.Succeed())

		cases := []struct {
			name    string
			closer  string
			open    bool
			skipped string
		}{
			{"unreachable-host", fmt.Sprintf("⏰ Ends: wait for 127.0.0.1:%d", deadAddr.Port),
				false, string(ParkedOnHost)},
			{"reachable-host", fmt.Sprintf("⏰ Ends: wait for 127.0.0.1:%d", liveAddr.Port),
				true, ""},
			{"future-clock", "⏰ Ends: " + base.Add(time.Hour).Format(time.RFC3339),
				false, string(ParkedUntil)},
			{"past-clock", "⏰ Ends: " + base.Add(-time.Hour).Format(time.RFC3339),
				true, ""},
		}
		for i, tc := range cases {
			sessionID := fmt.Sprintf("123e4567-e89b-12d3-a456-4266141740%02d", i)
			tick := newTickWithParkedGate(
				[]FlaggedRow{parkedRow(sessionID, tc.name)},
				map[string]string{sessionID: tc.closer},
				nil, &recordingRunner{}, fixedNow(base),
			)
			result, tickErr := tick.Tick(ctx)
			gomega.Expect(tickErr).NotTo(gomega.HaveOccurred())
			reason := ""
			if len(result.Skipped) > 0 {
				reason = reasonClass(result.Skipped[0])
			}
			fmt.Printf("case=%s opened=%t skipped=%s\n", tc.name, len(result.Opened) == 1, reason)
			gomega.Expect(len(result.Opened) == 1).To(gomega.Equal(tc.open))
			gomega.Expect(reason).To(gomega.Equal(tc.skipped))
		}
	})

	ginkgo.It("fails closed on an unreadable probe", func() {
		cardID := "123e4567-e89b-12d3-a456-426614174010"
		cardTick := newTickWithParkedGate(
			[]FlaggedRow{parkedRow(cardID, "card row")},
			map[string]string{cardID: "⏰ Ends: card 3f9a1c7e answered"},
			&tickParkedCards{err: stderrors.New("store unavailable")},
			&recordingRunner{}, fixedNow(base),
		)
		cardResult, err := cardTick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cardReopener, ok := cardTick.Reopener.(*recordingReopener)
		gomega.Expect(ok).To(gomega.BeTrue())
		cardReason := reasonClass(cardResult.Skipped[0])
		fmt.Printf("reopens=%d skipped=%s\n", len(cardReopener.snapshot()), cardReason)
		gomega.Expect(cardReopener.snapshot()).To(gomega.BeEmpty())
		gomega.Expect(cardReason).To(gomega.Equal(string(ParkedOnCard)))

		prID := "123e4567-e89b-12d3-a456-426614174011"
		prTick := newTickWithParkedGate(
			[]FlaggedRow{parkedRow(prID, "pr row")},
			map[string]string{prID: "⏰ Ends: bborbe/claude-wrapper#412 merges"},
			nil, &recordingRunner{err: stderrors.New("exit status 1")}, fixedNow(base),
		)
		prResult, err := prTick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		prReopener, ok := prTick.Reopener.(*recordingReopener)
		gomega.Expect(ok).To(gomega.BeTrue())
		prReason := reasonClass(prResult.Skipped[0])
		fmt.Printf("reopens=%d skipped=%s\n", len(prReopener.snapshot()), prReason)
		gomega.Expect(prReopener.snapshot()).To(gomega.BeEmpty())
		gomega.Expect(prReason).To(gomega.Equal(string(ParkedOnPR)))
	})

	ginkgo.It("skips a parked row without spending a reservation", func() {
		first := rowWith("agent", "headless", "parked-row")
		second := rowWith("agent", "headless", "eligible-row")
		reopener := &recordingReopener{}
		caps := &recordingCaps{}
		tick := newTick(staticSource{rows: []FlaggedRow{first, second}}, reopener, caps)
		tick.Parked = keyedParkedHold{key: first.Key()}

		result, err := tick.Tick(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fmt.Printf("%s\n", result.Line())
		gomega.Expect(result.Line()).
			To(gomega.ContainSubstring("opened=1 refused=0 skipped=1 considered=2"))
		opened := reopener.snapshot()
		gomega.Expect(opened).To(gomega.HaveLen(1))
		gomega.Expect(opened[0].Key()).To(gomega.Equal(second.Key()))
		gomega.Expect(caps.modes()).To(gomega.HaveLen(1))
	})
})

var _ = ginkgo.Describe("flagged tick resume-delay class split", func() {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	row := FlaggedRow{
		Kind: "task", Vault: "private-personal", Name: "a resumable row",
		Role: "agent", Mode: "headless",
		SessionID: "abc-123", Liveness: "ABSENT", Branch: "resume",
	}
	endedAt := now.Add(-30 * time.Minute).Format(time.RFC3339)

	newSplitTick := func() *FlaggedTick {
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, &recordingCaps{})
		tick.Now = fixedNow(now)
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: "abc-123", Status: "done",
			SpawnedAt: now.Add(-60 * time.Minute).Format(time.RFC3339),
			EndedAt:   &endedAt,
		}}}
		return tick
	}

	ginkgo.It("measures from ended_at when the row was never held back", func() {
		result, err := newSplitTick().Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("summary=%s\n", result.Line())

		gomega.Expect(result.ResumeDelays).To(gomega.ConsistOf("abc-123=30m0s"))
		gomega.Expect(result.ResumeDelaysWaited).To(gomega.BeEmpty())
	})

	ginkgo.It("measures from the last skip when the row was held back after its stop", func() {
		// The row stopped 30 min ago but the tick kept skipping it until 10 min
		// ago, so it only became able then. Counting from ended_at would report
		// 30 min of "resume latency" for a row that legitimately had nothing to
		// do — the defect that made the all-rows p90 unreadable against the two-minute bar.
		tick := newSplitTick()
		tick.rememberSkipped(row, now.Add(-10*time.Minute))

		result, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("summary=%s\n", result.Line())

		gomega.Expect(result.ResumeDelaysWaited).To(gomega.ConsistOf("abc-123=10m0s"))
		gomega.Expect(result.ResumeDelays).To(gomega.BeEmpty(), "must not pollute the could-continue population")
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("resume_delay_waited="))
	})
})

// sequenceSource serves a different row set per call, so a test can drive a real
// skip on one tick and a real open on the next.
type sequenceSource struct {
	mutex sync.Mutex
	calls int
	sets  [][]FlaggedRow
}

func (s *sequenceSource) Rows(context.Context) ([]FlaggedRow, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	i := s.calls
	if i >= len(s.sets) {
		i = len(s.sets) - 1
	}
	s.calls++
	return s.sets[i], nil
}

var _ = ginkgo.Describe("flagged tick resume-delay split via real ticks", func() {
	// This covers the PRODUCTION wiring: the rowSkipped branch calling
	// rememberSkipped. The specs above inject the skip directly, so they would
	// stay green if that call were deleted — which is the one edit that silently
	// turns the whole split back into a single mixed population.
	ginkgo.It("records the skip on a real tick and reports waited on the next", func() {
		now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		base := FlaggedRow{
			Kind: "task", Vault: "private-personal", Name: "a row that waits",
			Role: "agent", Mode: "headless",
			SessionID: "abc-123", Branch: "resume",
		}
		skippedRow := base
		skippedRow.Liveness = "LIVE"
		openedRow := base
		openedRow.Liveness = "ABSENT"

		endedAt := now.Add(-30 * time.Minute).Format(time.RFC3339)
		tick := newTick(
			&sequenceSource{sets: [][]FlaggedRow{{skippedRow}, {openedRow}}},
			&recordingReopener{}, &recordingCaps{},
		)
		clock := libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(now))
		tick.Now = clock
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: "abc-123", Status: "done",
			SpawnedAt: now.Add(-60 * time.Minute).Format(time.RFC3339),
			EndedAt:   &endedAt,
		}}}

		first, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(first.Opened).To(gomega.BeEmpty(), "the first tick must really skip")
		// The ledger row for this session is terminal, so on this branch the skip is
		// reported as not-absent-dead rather than not-absent. That relabel is the
		// whole point of this branch: the decision is unchanged, only the claim.
		gomega.Expect(first.Skipped).To(gomega.ContainElement("abc-123: " + reasonNotAbsentDead))

		// Ten minutes later the row becomes eligible. It was held back at 12:00,
		// so the real wait is those ten minutes — not the thirty since its stop.
		clock.SetNow(libtime.DateTime(now.Add(10 * time.Minute)))
		second, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("summary=%s\n", second.Line())

		gomega.Expect(second.Opened).To(gomega.ConsistOf("abc-123"))
		gomega.Expect(second.ResumeDelaysWaited).To(gomega.ConsistOf("abc-123=10m0s"))
		gomega.Expect(second.ResumeDelays).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("flagged tick not-absent labelling", func() {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	// A row the predicate believes is alive: it is not ABSENT, so it is skipped
	// either way. Only the REASON is under test.
	row := FlaggedRow{
		Kind: "task", Vault: "private-personal", Name: "a live-looking row",
		Role: "agent", Mode: "headless",
		SessionID: "abc-123", Liveness: "LIVE", Branch: "resume",
	}

	ginkgo.It("says not-absent-dead when the ledger proves the session ended", func() {
		// Measured 2026-11-18: 19 of 39 rows skipped as `not-absent` had no entry
		// in the wrapper's own roster, so the line asserted a live session that
		// was not there. The decision must not change - only the claim.
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, &recordingCaps{})
		tick.Now = fixedNow(now)
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: "abc-123", Status: "done",
			SpawnedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
		}}}

		result, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("summary=%s\n", result.Line())

		gomega.Expect(result.Skipped).To(gomega.ContainElement("abc-123: " + reasonNotAbsentDead))
		gomega.Expect(result.Opened).To(gomega.BeEmpty(), "the decision must not change")
	})

	ginkgo.It("still says not-absent when the ledger says the session is running", func() {
		tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingReopener{}, &recordingCaps{})
		tick.Now = fixedNow(now)
		tick.Ledger = fakeLedgerRows{rows: []LedgerRow{{
			SessionID: "abc-123", Status: "running",
			SpawnedAt: now.Add(-30 * time.Minute).Format(time.RFC3339),
		}}}

		result, err := tick.Tick(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(result.Skipped).To(gomega.ContainElement("abc-123: " + reasonNotAbsent))
		gomega.Expect(result.Skipped).NotTo(gomega.ContainElement("abc-123: " + reasonNotAbsentDead))
	})
})

var _ = ginkgo.Describe("endedSessions", func() {
	ginkgo.It("treats a session as ended only when its LAST row is terminal", func() {
		// A resumed session keeps its id and its later row re-opens the status to
		// `running`. Scanning for ANY terminal row would mark it dead and label
		// its skip not-absent-dead while it is live - the exact lie this function
		// exists to stop. Caught in review 2026-11-19.
		rows := []LedgerRow{
			{SessionID: "s1", Status: "done"},
			{SessionID: "s1", Status: "running"},
			{SessionID: "s2", Status: "running"},
			{SessionID: "s2", Status: "error"},
			{SessionID: "s3", Status: "running"},
		}

		got := endedSessions(rows)

		gomega.Expect(got).To(gomega.HaveKey("s2"))
		gomega.Expect(got).NotTo(gomega.HaveKey("s1"), "resumed session keeps its id; last row wins")
		gomega.Expect(got).NotTo(gomega.HaveKey("s3"))
	})
})

// The tick hands every row it has decided to open to OpenTick, so the tick and
// POST /api/1.0/open cannot route the same row two ways. These cases drive the
// real executor over the open fixtures: routing follows the page's role, never
// the FlaggedRow's stored mode.
var _ = ginkgo.Describe("flagged tick opens through the open decision", func() {
	var (
		ctx context.Context
		f   *openExecutorFixture
	)

	newOpenTick := func(rows []FlaggedRow) *FlaggedTick {
		tick := newTick(staticSource{rows: rows}, &recordingReopener{}, &recordingCaps{})
		tick.Open = f.executor
		tick.Chain = &fakeChainProbe{live: false}
		return tick
	}

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		f = newOpenExecutorFixture()
	})

	ginkgo.It("resumes an agent row headless by its recorded id, never as a tab", func() {
		id := openExecTestUUID(1)
		writeOpenPage(f.dir, openTaskTestFolder, "Agent Resume",
			"role: agent", "mode: interactive", "claude_session_id: "+id)
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Agent Resume",
			Role: RoleAgent, Mode: SpawnModeInteractive,
			SessionID: id, Liveness: "ABSENT", Branch: "resume",
		}

		result, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("line=%s headless=%d tabs=%d\n",
			result.Line(), len(f.headless.requests), len(f.tabOpener.requests))
		gomega.Expect(result.Opened).To(gomega.ConsistOf(row.Key()))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
		gomega.Expect(f.headless.requests[0].Resume).To(gomega.Equal(id))
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
		gomega.Expect(f.tabs.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("creates an agent row headless when it has no session id", func() {
		writeOpenPage(f.dir, openTaskTestFolder, "Agent Create", "role: agent")
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Agent Create",
			Role: RoleAgent, Mode: SpawnModeHeadless,
			Liveness: "none", Branch: "create",
		}

		result, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.ConsistOf(row.Key()))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
		gomega.Expect(f.headless.requests[0].Resume).To(gomega.BeEmpty())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
	})

	// SC1 of "A Foreign-Vault Daemon Claims Rows It Must Refuse": a foreign row
	// must be dropped at SELECTION. The row here is fully openable — a page, a
	// role, a create branch — so only the vault family can stop it, and deleting
	// the selection gate makes this spec open it (proved by mutation).
	ginkgo.It("skips a foreign-vault row at selection and never opens it", func() {
		writeOpenPage(f.dir, openTaskTestFolder, "Foreign Row", "role: agent")
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Foreign Row",
			Role: RoleAgent, Mode: SpawnModeHeadless,
			Liveness: "none", Branch: "create",
		}
		tick := newOpenTick([]FlaggedRow{row})
		tick.OwnVault = DaemonVault{Name: "private-personal"}

		result, err := tick.Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoWriter.Printf("line=%s\n", result.Line())
		gomega.Expect(result.Opened).To(gomega.BeEmpty())
		gomega.Expect(result.Skipped).
			To(gomega.ConsistOf(row.Key() + ": " + reasonForeignVault))
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("skipped_by_reason=foreign-vault:1"))
		gomega.Expect(result.Line()).NotTo(gomega.ContainSubstring("refused_by_reason=vault-mismatch"))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("opens a row from its own vault family", func() {
		writeOpenPage(f.dir, openTaskTestFolder, "Own Row", "role: agent")
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Own Row",
			Role: RoleAgent, Mode: SpawnModeHeadless,
			Liveness: "none", Branch: "create",
		}
		tick := newOpenTick([]FlaggedRow{row})
		tick.OwnVault = DaemonVault{Name: "v1-satellite"}

		result, err := tick.Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.ConsistOf(row.Key()))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("leaves the gate off when the daemon named no vault", func() {
		writeOpenPage(f.dir, openTaskTestFolder, "Ungated Row", "role: agent")
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Ungated Row",
			Role: RoleAgent, Mode: SpawnModeHeadless,
			Liveness: "none", Branch: "create",
		}

		result, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.ConsistOf(row.Key()))
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("carries the readiness first step on a task row", func() {
		writeOpenPage(f.dir, openTaskTestFolder, "Ready", "role: agent")
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Ready",
			Role: RoleAgent, Mode: SpawnModeHeadless,
			Liveness: "none", Branch: "create",
		}

		_, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.headless.requests).To(gomega.HaveLen(1))
		gomega.Expect(f.headless.requests[0].Prompt).
			To(gomega.HavePrefix("FIRST STEP — readiness gate"))
		gomega.Expect(f.headless.requests[0].Prompt).
			To(gomega.ContainSubstring(`/vault-cli:work-on-task "Ready"`))
	})

	ginkgo.It("opens a human row as a Direct tab", func() {
		writeOpenPage(f.dir, openTaskTestFolder, "Human Row", "role: human")
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Human Row",
			Role: RoleHuman, Mode: SpawnModeInteractive,
			Liveness: "none", Branch: "direct",
		}

		result, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.ConsistOf(row.Key()))
		gomega.Expect(f.tabOpener.requests).To(gomega.HaveLen(1))
		gomega.Expect(f.tabOpener.requests[0].Role).To(gomega.Equal(RoleHuman))
		gomega.Expect(f.tabOpener.requests[0].WindowID).To(gomega.Equal(12))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("opens a goal as a manager tab in Managers and spawns no worker", func() {
		tabs := &fakeTabClient{}
		opener := newOpenTabTestOpener(
			steppingClock(), tabs, f.registry, nil, &fakeLedger{}, openScripts(),
		)
		deps := f.deps()
		deps.TabOpener = opener
		f.executor = NewOpenExecutor(deps)
		writeOpenPage(f.dir, openGoalTestFolder, "Goal Row", "phase: todo")
		row := FlaggedRow{
			Kind: "goal", Vault: "v1", Name: "Goal Row",
			Role: RoleManager, Mode: SpawnModeInteractive,
			Liveness: "none", Branch: "create",
		}

		result, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.ConsistOf(row.Key()))
		gomega.Expect(tabs.spawned).To(gomega.HaveLen(1))
		gomega.Expect(tabs.spawned[0].TabName).To(gomega.Equal("Goal Row Manager"))
		gomega.Expect(tabs.spawned[0].WindowID).NotTo(gomega.BeNil())
		gomega.Expect(*tabs.spawned[0].WindowID).To(gomega.Equal(14))
		// No `/supervisor:open` worker: the manager tab is the only spawn.
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a human row when the Direct key is absent", func() {
		f.roleMap.entries = map[string]RoleMapEntry{
			"Managers": {Chip: new("orange"), WindowID: new(14)},
			"Hold":     {WindowID: new(2)},
		}
		writeOpenPage(f.dir, openTaskTestFolder, "No Direct", "role: human")
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "No Direct",
			Role: RoleHuman, Mode: SpawnModeInteractive,
			Liveness: "none", Branch: "direct",
		}

		result, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.BeEmpty())
		gomega.Expect(result.Refused).To(gomega.HaveLen(1))
		gomega.Expect(result.Refused[0]).To(gomega.ContainSubstring(OpenReasonRoleMapKeyMissing))
		gomega.Expect(result.Line()).
			To(gomega.ContainSubstring("refused_by_reason=role-map-key-missing:1"))
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("counts a jump row skipped and activates no pane", func() {
		id := openExecTestUUID(2)
		writeOpenPage(f.dir, openTaskTestFolder, "Live Human",
			"role: human", "claude_session_id: "+id)
		f.registry.live = []RegistryEntry{{PID: 9, SessionID: id, Name: "Live Human"}}
		f.joiner.panes = map[int]string{9: "p9"}
		row := FlaggedRow{
			Kind: "task", Vault: "v1", Name: "Live Human",
			Role: RoleHuman, Mode: SpawnModeInteractive,
			SessionID: id, Liveness: "ABSENT", Branch: "resume",
		}

		result, err := newOpenTick([]FlaggedRow{row}).Tick(ctx)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Opened).To(gomega.BeEmpty())
		gomega.Expect(result.Skipped).To(gomega.ConsistOf(row.Key() + ": " + reasonAlreadyLive))
		gomega.Expect(result.Line()).To(gomega.ContainSubstring("skipped_by_reason=already-live:1"))
		gomega.Expect(f.tabs.activated).To(gomega.BeEmpty())
		gomega.Expect(probeCalls(f.scripts)).To(gomega.Equal(0))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
	})
})
