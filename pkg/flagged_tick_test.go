// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"fmt"
	"net"
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
	mutex sync.Mutex
	rows  []LedgerRow
	err   error
}

func (l *recordingLedger) Rows(context.Context) ([]LedgerRow, error) { return nil, l.err }

func (l *recordingLedger) Post(_ context.Context, _ string, row LedgerRow) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.rows = append(l.rows, row)
	return l.err
}

func (l *recordingLedger) Patch(context.Context, string, LedgerPatch) error { return nil }

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
				reason, refused, skipped := decision(row, recent, now)
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
			return NewFlaggedReopener(
				supervisor,
				spawner,
				testVaults(),
				ledger,
				fixedNow(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)),
			)
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
			// ⚠️ A headless reopen that files no ledger row is invisible to the caps
			// AND to the tick's own recently-spawned guard, so the next tick would
			// open a second copy of a row whose child is still starting.
			rows := ledger.snapshot()
			gomega.Expect(rows).To(gomega.HaveLen(1))
			gomega.Expect(rows[0].SessionID).To(gomega.Equal("resumed"))
			gomega.Expect(rows[0].ResumedFrom).To(gomega.Equal("abc-123"))
			gomega.Expect(rows[0].Role).To(gomega.Equal("agent"))
			gomega.Expect(rows[0].Mode).To(gomega.Equal(SpawnModeHeadless))
			gomega.Expect(rows[0].SpawnedAt).To(gomega.Equal("2026-11-11T12:00:00Z"))
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
				To(gomega.Equal(`/vault-cli:work-on-task "Direct Task"`))
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
			cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 0)
			cache.store(nil, killed, 0)

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

			cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 0)
			tick.tickAndLog(context.Background(), cache)

			cache.store(nil, killed, 0)
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
			cache.store(nil, killed, 0)
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
			cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 95*time.Second)
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
