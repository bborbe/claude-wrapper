// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// blockingSnapshotter hangs until its context ends, the shape of a wedged vault
// read. Prepare must bound it, or one subject would stall the whole loop.
type blockingSnapshotter struct{}

// Snapshot waits for the context and returns its error.
func (blockingSnapshotter) Snapshot(ctx context.Context, _ string) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}

// lineCollector is a thread-safe log sink for the runner specs.
type lineCollector struct {
	mutex sync.Mutex
	lines []string
}

// logf records one formatted line.
func (c *lineCollector) logf(format string, args ...any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

// count returns how many recorded lines contain the needle.
func (c *lineCollector) count(needle string) int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	found := 0
	for _, line := range c.lines {
		if strings.Contains(line, needle) {
			found++
		}
	}
	return found
}

var _ = ginkgo.Describe("writes a tick file from the wiring", func() {
	ginkgo.It("ticks a configured subject and writes its table file", func() {
		dir := ginkgo.GinkgoT().TempDir()
		cfg := Config{
			Vault:            filepath.Join("..", "..", "testdata", "sweepgate", "vault"),
			Subjects:         []string{"Parity Topic"},
			StateDir:         filepath.Join(dir, "state"),
			SettledDir:       filepath.Join(dir, "settled"),
			FeedDir:          filepath.Join(dir, "feed"),
			HoldsPath:        filepath.Join(dir, "holds.json"),
			PredispatchDir:   filepath.Join(dir, "predispatch"),
			WorkerManagerDir: filepath.Join(dir, "worker-manager"),
			RegistryDir:      filepath.Join(dir, "sessions"),
			HeartbeatDir:     filepath.Join(dir, "heartbeat"),
			ToolMarkerDir:    filepath.Join(dir, "tools"),
			TranscriptDir:    filepath.Join(dir, "projects"),
			StoreURL:         "http://127.0.0.1:1",
			Now:              fixedClock(),
			Interval:         50 * time.Millisecond,
			ReadTimeout:      2 * time.Second,
			Logf:             func(string, ...any) {},
		}
		runner, err := NewTickRunner(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- runner.Run(ctx) }()

		tickPath := filepath.Join(cfg.StateDir, "parity-topic.tick.txt")
		var info os.FileInfo
		gomega.Eventually(func() bool {
			stat, statErr := os.Stat(tickPath)
			if statErr != nil {
				return false
			}
			info = stat
			return true
		}, 2*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())

		ginkgo.GinkgoWriter.Printf("tick_file=%s mtime=%s\n", tickPath, info.ModTime())

		cancel()
		var runErr error
		select {
		case runErr = <-done:
		case <-time.After(2 * time.Second):
			ginkgo.Fail("runner did not stop after cancel")
		}
		gomega.Expect(errors.Is(runErr, context.Canceled)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("cancels a hanging read", func() {
	ginkgo.It("abandons the read and keeps ticking", func() {
		collector := &lineCollector{}
		cfg := Config{
			Vault:       "unused",
			Subject:     "Parity Topic",
			Source:      blockingSnapshotter{},
			ReadTimeout: 10 * time.Millisecond,
			Now:         fixedClock(),
			Logf:        collector.logf,
		}
		runner := &TickRunner{
			Configs:  []Config{cfg},
			Interval: 20 * time.Millisecond,
			Logf:     collector.logf,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- runner.Run(ctx) }()

		// The abandoned read must not wedge the loop: at least two passes have to
		// complete while the outer context is still live.
		gomega.Eventually(func() int {
			return collector.count("sweep-gate")
		}, 2*time.Second, 10*time.Millisecond).Should(gomega.BeNumerically(">=", 2))

		cancel()
		var runErr error
		select {
		case runErr = <-done:
		case <-time.After(2 * time.Second):
			ginkgo.Fail("runner did not stop after cancel")
		}
		ginkgo.GinkgoWriter.Printf("err=%v\n", runErr)
		gomega.Expect(errors.Is(runErr, context.Canceled)).To(gomega.BeTrue())
		gomega.Expect(collector.count("sweep-gate")).To(gomega.BeNumerically(">=", 2))
	})
})
