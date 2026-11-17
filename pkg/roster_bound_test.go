// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// countingRegistry counts every read the roster performs against it.
//
// It implements BOTH the per-row pair (Status, Cwd) and the bulk Entries, and
// counts all three as directory reads, because that is what each one is: the
// registry has no index, so every question re-reads the whole directory. The
// test therefore reads the same way against the pre-fix implementation, which
// asked Status once and Cwd twice per row, as against the fixed one, which asks
// Entries once per sweep.
type countingRegistry struct {
	entries        []RegistryEntry
	directoryReads int
	mu             sync.Mutex
}

func (c *countingRegistry) count() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.directoryReads++
}

func (c *countingRegistry) Status(_ context.Context, sessionID string) (*string, error) {
	c.count()
	for _, entry := range c.entries {
		if entry.SessionID == sessionID {
			status := entry.Status
			return &status, nil
		}
	}
	return nil, nil
}

func (c *countingRegistry) Cwd(_ context.Context, sessionID string) (*string, error) {
	c.count()
	for _, entry := range c.entries {
		if entry.SessionID == sessionID {
			cwd := entry.Cwd
			return &cwd, nil
		}
	}
	return nil, nil
}

func (c *countingRegistry) Live(context.Context) ([]RegistryEntry, error) {
	c.count()
	return c.entries, nil
}

func (c *countingRegistry) Named(context.Context, string) ([]string, error) {
	c.count()
	return nil, nil
}

func (c *countingRegistry) NamesForSession(context.Context, string) ([]string, error) {
	c.count()
	return nil, nil
}

func (c *countingRegistry) Entries(context.Context) ([]RegistryEntry, error) {
	c.count()
	return c.entries, nil
}

// countingTranscripts counts transcript window reads across both shapes.
//
// One count per METHOD CALL, which is one window read: the pre-fix roster called
// the two old methods per row (two reads), the fixed one calls State (one).
type countingTranscripts struct {
	reads int
	mu    sync.Mutex
}

func (c *countingTranscripts) count() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
}

func (c *countingTranscripts) LastAssistantText(context.Context, string) (*string, error) {
	c.count()
	return nil, nil
}

func (c *countingTranscripts) CurrentToolCall(context.Context, string) (*ToolCall, error) {
	c.count()
	return nil, nil
}

func (c *countingTranscripts) State(
	context.Context,
	string,
	*string,
) (*TranscriptState, error) {
	c.count()
	return &TranscriptState{}, nil
}

var _ = ginkgo.Describe("Roster enrichment bounds", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("reads the registry once and each transcript once, whatever the fleet size", func() {
		// The bound is a COUNT, not a duration: a duration assertion is a flaky
		// test on a loaded machine, while the read count is the mechanism that made
		// the sweep slow. 12 rows is enough to separate one from twelve.
		const rows = 12
		sessions := make([]Session, 0, rows)
		entries := make([]RegistryEntry, 0, rows)
		for i := 0; i < rows; i++ {
			id := fmt.Sprintf("session-%02d", i)
			sessions = append(sessions, Session{ID: id, Cwd: "/work/dir", StartedAt: time.Now()})
			entries = append(entries, RegistryEntry{
				PID: os.Getpid(), SessionID: id, Status: "busy", Cwd: "/work/dir",
			})
		}
		registry := &countingRegistry{entries: entries}
		transcripts := &countingTranscripts{}

		got, err := NewRoster(
			&rosterSupervisor{sessions: sessions}, nil, nil, transcripts, registry,
		).List(ctx)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(got).To(gomega.HaveLen(rows))

		// ONE registry read for the whole sweep. The pre-fix form asked Status once
		// and Cwd twice per row — 36 reads here, 13.1 s of a 20.0 s measured sweep.
		gomega.Expect(registry.directoryReads).To(gomega.Equal(1))
		// ONE transcript window per row, not two: the same 256 KiB tail was opened,
		// seeked and JSON-decoded twice for every session.
		gomega.Expect(transcripts.reads).To(gomega.Equal(rows))

		// The snapshot still answers the field it replaced.
		gomega.Expect(got[0].SessionStatus).NotTo(gomega.BeNil())
		gomega.Expect(*got[0].SessionStatus).To(gomega.Equal("busy"))
		gomega.Expect(got[0].AwaitingInput).NotTo(gomega.BeNil())
		gomega.Expect(*got[0].AwaitingInput).To(gomega.BeFalse())
	})

	ginkgo.It("abandons a row whose read outlives the row bound instead of wedging the sweep", func() {
		sessions := []Session{{ID: "slow-session", Cwd: "/work/dir", StartedAt: time.Now()}}
		registry := &countingRegistry{entries: []RegistryEntry{
			{PID: os.Getpid(), SessionID: "slow-session", Status: "busy", Cwd: "/work/dir"},
		}}

		started := time.Now()
		got, err := NewRoster(
			&rosterSupervisor{sessions: sessions}, nil, nil, &blockingTranscripts{}, registry,
		).List(ctx)
		elapsed := time.Since(started)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(got).To(gomega.HaveLen(1))
		// The row is served with its static fields and null read-time ones.
		gomega.Expect(got[0].SessionStatus).NotTo(gomega.BeNil())
		gomega.Expect(got[0].LastMessage).To(gomega.BeNil())
		gomega.Expect(got[0].CurrentToolCall).To(gomega.BeNil())
		// Bounded by the row timeout, not by the read.
		gomega.Expect(elapsed).To(gomega.BeNumerically("<", rosterEnrichRowTimeout+time.Second))
	})
})

// blockingTranscripts never answers until its context ends, which is the
// pathological row the deadline exists for.
type blockingTranscripts struct{}

func (b *blockingTranscripts) LastAssistantText(context.Context, string) (*string, error) {
	return nil, nil
}

func (b *blockingTranscripts) CurrentToolCall(context.Context, string) (*ToolCall, error) {
	return nil, nil
}

func (b *blockingTranscripts) State(
	ctx context.Context,
	_ string,
	_ *string,
) (*TranscriptState, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
