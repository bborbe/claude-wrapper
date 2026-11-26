// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fixedRowSource answers every read with the same row set.
type fixedRowSource struct {
	rows []FlaggedRow
}

func (s fixedRowSource) Rows(context.Context) ([]FlaggedRow, error) { return s.rows, nil }

// sendTimeoutReopener fails the named row the way a spawn whose opening stdin
// write never returns now fails: with an error wrapping ErrSendTimeout once the
// write deadline passes. Every other row opens.
type sendTimeoutReopener struct {
	stuck string
}

func (r sendTimeoutReopener) Reopen(ctx context.Context, row FlaggedRow) error {
	if row.Name != r.stuck {
		return nil
	}
	return errors.Wrapf(ctx, ErrSendTimeout, "session %q did not read its stdin", row.Name)
}

// lockedLines collects tick log lines from the tick goroutine.
type lockedLines struct {
	mutex sync.Mutex
	lines []string
}

func (l *lockedLines) logf(format string, args ...any) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *lockedLines) matching(substr string) []string {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	var out []string
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			out = append(out, line)
		}
	}
	return out
}

// Measured 2026-11-26: one headless spawn blocked forever writing its opening
// turn to the child's stdin, and because the tick runs the open inline no further
// `flagged-tick opened=` line was logged for 67 minutes. A bounded write turns the
// hang into a refusal, so the tick must keep summarising after it.
var _ = ginkgo.Describe("flagged tick survives a spawn whose first write times out", func() {
	ginkgo.It("logs further summary lines and refuses the row naming the send timeout", func() {
		lines := &lockedLines{}
		tick := newTick(
			fixedRowSource{rows: []FlaggedRow{rowWith("agent", "headless", "stuck-row")}},
			sendTimeoutReopener{stuck: "stuck-row"},
			&recordingCaps{},
		)
		tick.Interval = 10 * time.Millisecond
		tick.CacheDir = ginkgo.GinkgoT().TempDir()
		tick.Logf = lines.logf
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = tick.Run(ctx) }()

		// The first refusal is the blocked spawn; the tick must summarise at least
		// two further ticks after it.
		gomega.Eventually(func() int {
			return len(lines.matching("flagged-tick opened="))
		}, 2*time.Second, 5*time.Millisecond).Should(gomega.BeNumerically(">=", 3))

		refusals := lines.matching("send-timeout")
		ginkgo.GinkgoWriter.Printf("refusals=%q\n", refusals)
		gomega.Expect(refusals).NotTo(gomega.BeEmpty())
		gomega.Expect(strings.Join(refusals, "\n")).To(gomega.ContainSubstring("stuck-row"))
	})
})
