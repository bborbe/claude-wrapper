// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
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
		poster := NewHeartbeatPoster(NewSessionRegistry(dir), client, time.Hour)

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
			time.Hour,
		)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- poster.Run(ctx) }()

		cancel()
		gomega.Eventually(done, time.Second).Should(gomega.Receive(gomega.BeNil()))
		gomega.Expect(client.snapshot()).To(gomega.BeEmpty())
	})
})
