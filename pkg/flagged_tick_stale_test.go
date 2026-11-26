// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The summary line is what an operator greps for, so the degradation has to be
// on it — a separate `read failed` line above it is not enough.
var _ = ginkgo.Describe("rows_stale on the summary line", func() {
	ginkgo.It("names the outage on the summary line a tick is known by", func() {
		tick := newTick(nil, &recordingReopener{}, &recordingCaps{})
		var lines []string
		tick.Logf = func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
		}
		cache := &flaggedRowCache{}

		cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 0, time.Now())
		tick.tickAndLog(context.Background(), cache)

		cache.store(nil,
			errors.Wrapf(context.Background(), ErrVaultAPIUnavailable, "read flagged rows"),
			0, time.Now())
		before := len(lines)
		tick.tickAndLog(context.Background(), cache)
		stale := strings.Join(lines[before:], "\n")
		ginkgo.GinkgoWriter.Printf("lines-after-outage=%s\n", stale)

		gomega.Expect(stale).To(gomega.ContainSubstring("rows_stale=vault-api-unavailable"))
		gomega.Expect(stale).To(gomega.ContainSubstring("considered=1"))
	})

	ginkgo.It("omits rows_stale from a tick whose read succeeded", func() {
		tick := newTick(nil, &recordingReopener{}, &recordingCaps{})
		var lines []string
		tick.Logf = func(format string, args ...any) {
			lines = append(lines, fmt.Sprintf(format, args...))
		}
		cache := &flaggedRowCache{}
		cache.store([]FlaggedRow{rowWith("agent", "headless", "good-row")}, nil, 0, time.Now())
		tick.tickAndLog(context.Background(), cache)
		healthy := strings.Join(lines, "\n")
		ginkgo.GinkgoWriter.Printf("lines-healthy=%s\n", healthy)

		// The ABSENCE is the positive reading that this tick served a fresh set;
		// a field always present could not tell the two apart.
		gomega.Expect(healthy).NotTo(gomega.ContainSubstring("rows_stale="))
		gomega.Expect(healthy).To(gomega.ContainSubstring("considered=1"))
	})
})
