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

func newTick(source FlaggedRowSource, spawner TabSpawner, caps SpawnCaps) *FlaggedTick {
	return &FlaggedTick{
		Source:  source,
		Spawner: spawner,
		Caps:    caps,
		Now:     fixedNow(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)),
		Logf:    func(string, ...any) {},
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
			}, false, reasonAlreadyLive),
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
			spawner := &recordingSpawner{}
			caps := &recordingCaps{}
			result, err := newTick(staticSource{rows: rows}, spawner, caps).Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf(
				"opened=%d refused=%d spawns=%d\n",
				len(result.Opened), len(result.Refused), len(spawner.snapshot()),
			)
			gomega.Expect(spawner.snapshot()).To(gomega.HaveLen(maxRowsPerTick))
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
			spawner := &recordingSpawner{}
			_, err := newTick(staticSource{rows: rows}, spawner, &recordingCaps{}).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			requests := spawner.snapshot()
			ginkgo.GinkgoWriter.Printf("first=%s\n", requests[0].Label)
			gomega.Expect(requests[0].Label).To(gomega.Equal("human-row"))
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
			spawner := &recordingSpawner{}
			caps := &recordingCaps{workerErr: stderrors.New("maxConcurrent reached")}
			result, err := newTick(staticSource{rows: rows}, spawner, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			ginkgo.GinkgoWriter.Printf("spawns=%d refused=%d\n",
				len(spawner.snapshot()), len(result.Refused))
			gomega.Expect(spawner.snapshot()).To(gomega.BeEmpty())
			gomega.Expect(result.Refused).To(gomega.HaveLen(1))
		})

		ginkgo.It("spawns nothing when ReserveStart refuses", func() {
			rows := []FlaggedRow{rowWith("agent", "headless", "one")}
			spawner := &recordingSpawner{}
			caps := &recordingCaps{rateErr: stderrors.New("maxStartsPerMinute reached")}
			result, err := newTick(staticSource{rows: rows}, spawner, caps).
				Tick(context.Background())
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(spawner.snapshot()).To(gomega.BeEmpty())
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
			_, err := newTick(staticSource{rows: rows}, &recordingSpawner{}, caps).
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
			tick := newTick(blockingSource{}, &recordingSpawner{}, &recordingCaps{})
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
			tick := newTick(source, &recordingSpawner{}, &recordingCaps{})
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

	ginkgo.Describe("skips a recently spawned row", func() {
		ginkgo.It("does not open a session spawned inside the window", func() {
			at := time.Date(2026, 11, 11, 11, 55, 0, 0, time.UTC)
			row := rowWith("agent", "headless", "fresh")
			row.SessionID = "fresh"
			tick := newTick(staticSource{rows: []FlaggedRow{row}}, &recordingSpawner{}, &recordingCaps{})
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
