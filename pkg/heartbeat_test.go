// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// recordingHeartbeatClient records every row it is handed.
type recordingHeartbeatClient struct {
	mutex sync.Mutex
	rows  []HeartbeatRow
	seen  chan struct{}
}

func (c *recordingHeartbeatClient) Post(_ context.Context, row HeartbeatRow) error {
	c.mutex.Lock()
	c.rows = append(c.rows, row)
	c.mutex.Unlock()
	select {
	case c.seen <- struct{}{}:
	default:
	}
	return nil
}

func (c *recordingHeartbeatClient) snapshot() []HeartbeatRow {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	out := make([]HeartbeatRow, len(c.rows))
	copy(out, c.rows)
	return out
}

// ledgerStatusOf names the case's ledger state for the printed evidence line.
func ledgerStatusOf(rows []LedgerRow) string {
	if len(rows) == 0 {
		return "none"
	}
	return rows[len(rows)-1].Status
}

var _ = ginkgo.Describe("HeartbeatPoster", func() {
	ginkgo.It("posts for a live session, skips a dead pid, and stops on cancel", func() {
		dir := ginkgo.GinkgoT().TempDir()
		writeRegistryEntry(dir, registryFile{
			PID: os.Getpid(), SessionID: "live-session", Status: "idle", Cwd: "/tmp",
		})
		writeRegistryEntry(dir, registryFile{
			PID: 999999, SessionID: "dead-session", Status: "idle", Cwd: "/tmp",
		})

		client := &recordingHeartbeatClient{seen: make(chan struct{}, 8)}
		poster := NewHeartbeatPoster(NewSessionRegistry(dir), client, nil, time.Hour)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- poster.Run(ctx) }()

		// The first round is immediate, so the post arrives without waiting an
		// interval.
		gomega.Eventually(func() []HeartbeatRow {
			return client.snapshot()
		}, time.Second, 5*time.Millisecond).Should(gomega.HaveLen(1))

		rows := client.snapshot()
		gomega.Expect(rows[0].SessionID).To(gomega.Equal("live-session"))
		gomega.Expect(rows[0].Location).To(gomega.Equal("local"))
		gomega.Expect(rows[0].State).To(gomega.Equal("idle"))
		// The store treats only this source as proof of a self-posting producer.
		gomega.Expect(rows[0].Source).To(gomega.Equal("mcp-timer"))

		cancel()
		gomega.Eventually(done, time.Second).Should(gomega.Receive(gomega.BeNil()))
	})

	ginkgo.It("returns nil rather than an error when the registry cannot be read", func() {
		client := &recordingHeartbeatClient{seen: make(chan struct{}, 1)}
		poster := NewHeartbeatPoster(
			NewSessionRegistry(ginkgo.GinkgoT().TempDir()+"/does-not-exist"),
			client,
			nil,
			time.Hour,
		)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- poster.Run(ctx) }()

		cancel()
		gomega.Eventually(done, time.Second).Should(gomega.Receive(gomega.BeNil()))
		gomega.Expect(client.snapshot()).To(gomega.BeEmpty())
	})

	ginkgo.It("suppresses only a terminal ledger row", func() {
		// One registry of live sessions, one ledger row per case. The assertion is the
		// POSTED SET, never a count: a count would pass for an implementation that
		// posted the wrong sessions and skipped the right number.
		cases := []struct {
			name   string
			rows   []LedgerRow
			posted bool
		}{
			{"done", []LedgerRow{{SessionID: "s-done", Status: "done"}}, false},
			{"error", []LedgerRow{{SessionID: "s-error", Status: "error"}}, false},
			{"running", []LedgerRow{{SessionID: "s-running", Status: "running"}}, true},
			{"empty", []LedgerRow{{SessionID: "s-empty", Status: ""}}, true},
			{"cancelled", []LedgerRow{{SessionID: "s-cancelled", Status: "cancelled"}}, true},
			{"no-row", nil, true},
			// A resumed session keeps its id, so its new `running` row must override the
			// stale terminal one that precedes it.
			{"duplicate", []LedgerRow{
				{SessionID: "s-duplicate", Status: "done"},
				{SessionID: "s-duplicate", Status: "running"},
			}, true},
		}

		// Every case runs and PRINTS before anything is asserted. A fail-fast
		// assertion would stop at the first wrong answer and leave the remaining cases
		// unobserved, so the evidence line would report one case where the rule has
		// seven — and an implementation that inverted only one direction would look
		// like an implementation that inverted none.
		type outcome struct {
			name     string
			expected bool
			actual   bool
			reads    int
		}
		outcomes := make([]outcome, 0, len(cases))
		for _, tc := range cases {
			sessionID := "s-" + tc.name
			client := &recordingHeartbeatClient{}
			ledger := &fakeLedger{rows: tc.rows}
			registry := &fakeRegistry{live: []RegistryEntry{{SessionID: sessionID, PID: 1}}}
			poster := NewHeartbeatPoster(registry, client, ledger, time.Hour)
			concrete, ok := poster.(*heartbeatPoster)
			gomega.Expect(ok).To(gomega.BeTrue())
			concrete.round(context.Background())

			posted := map[string]bool{}
			for _, row := range client.snapshot() {
				posted[row.SessionID] = true
			}
			ginkgo.GinkgoWriter.Printf(
				"case=%s ledger=%s posted=%t\n", tc.name, ledgerStatusOf(tc.rows), posted[sessionID],
			)
			outcomes = append(outcomes, outcome{tc.name, tc.posted, posted[sessionID], ledger.calls})
		}

		for _, o := range outcomes {
			gomega.Expect(o.actual).To(gomega.Equal(o.expected), "case %s", o.name)
			// One read per round, never one per session.
			gomega.Expect(o.reads).To(gomega.Equal(1), "case %s", o.name)
		}
	})

	ginkgo.It("posts for every live session when the ledger cannot be read", func() {
		client := &recordingHeartbeatClient{}
		registry := &fakeRegistry{live: []RegistryEntry{
			{SessionID: "a", PID: 1},
			{SessionID: "b", PID: 2},
			{SessionID: "c", PID: 3},
		}}
		ledger := &fakeLedger{err: stderrors.New("store down")}
		poster := NewHeartbeatPoster(registry, client, ledger, time.Hour)
		concrete, ok := poster.(*heartbeatPoster)
		gomega.Expect(ok).To(gomega.BeTrue())
		concrete.round(context.Background())

		posted := map[string]bool{}
		for _, row := range client.snapshot() {
			posted[row.SessionID] = true
		}
		ginkgo.GinkgoWriter.Printf("case=ledger-unreadable posted=%t\n", len(posted) == 3)
		gomega.Expect(posted).To(gomega.HaveLen(3))
	})
})
