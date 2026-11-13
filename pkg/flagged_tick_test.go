// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
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
		recent := map[string]time.Time{"recent": now.Add(-5 * time.Minute)}

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
			ginkgo.Entry("none", FlaggedRow{
				Name: "none", Liveness: "none",
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
			gomega.Expect(result.Refused).To(gomega.HaveLen(1))
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
			gomega.Expect(runner.argv).To(gomega.Equal([]string{"python3", "/tmp/fr.py", "--json"}))
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
			supervisor *recordingSupervisor, spawner *recordingSpawner,
		) FlaggedReopener {
			return NewFlaggedReopener(supervisor, spawner, testVaults())
		}

		ginkgo.It("resumes a headless row by its session id and sends the work turn", func() {
			supervisor := &recordingSupervisor{sessionID: "resumed"}
			spawner := &recordingSpawner{}
			row := rowWith("agent", "headless", "Some Task")
			row.SessionID = "abc-123"
			gomega.Expect(newReopener(supervisor, spawner).Reopen(context.Background(), row)).
				To(gomega.BeNil())

			spawns := supervisor.snapshot()
			sent := supervisor.sent()
			ginkgo.GinkgoWriter.Printf("resume=%q sent=%v tabSpawns=%d\n",
				spawns[0].Resume, sent, len(spawner.snapshot()))
			gomega.Expect(spawns).To(gomega.HaveLen(1))
			gomega.Expect(spawns[0].Resume).To(gomega.Equal("abc-123"))
			gomega.Expect(spawner.snapshot()).To(gomega.BeEmpty())
			gomega.Expect(sent).To(gomega.HaveLen(1))
			gomega.Expect(sent[0]).To(gomega.ContainSubstring(`/vault-cli:work-on-task "Some Task"`))
		})

		ginkgo.It("creates a headless row that has no session id", func() {
			supervisor := &recordingSupervisor{}
			row := rowWith("agent", "headless", "Some Task")
			gomega.Expect(newReopener(supervisor, &recordingSpawner{}).
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
			gomega.Expect(newReopener(supervisor, spawner).Reopen(context.Background(), row)).
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
			err := newReopener(supervisor, &recordingSpawner{}).
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
