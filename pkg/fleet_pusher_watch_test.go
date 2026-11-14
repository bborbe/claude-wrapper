// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// recordingPoster records every card it is asked to post.
type recordingPoster struct {
	requests []AttentionPostRequest
	posted   map[string]string
}

func (p *recordingPoster) Post(_ context.Context, request AttentionPostRequest) (string, error) {
	if p.posted == nil {
		p.posted = map[string]string{}
	}
	p.requests = append(p.requests, request)
	itemID := "card-" + request.DedupKey
	p.posted[request.DedupKey] = itemID
	return itemID, nil
}

// postedCardLookup answers from the poster's own record, so a repeated cycle sees
// the card the first cycle created.
type postedCardLookup struct {
	poster *recordingPoster
}

func (l *postedCardLookup) ExistingCard(
	_ context.Context,
	_ string,
	dedupKey string,
) (string, error) {
	return l.poster.posted[dedupKey], nil
}

func watchHarness(
	heartbeatPath string,
	now time.Time,
	boot time.Time,
) (*FleetPusherWatch, *recordingPoster, *[]string) {
	poster := &recordingPoster{}
	lines := &[]string{}
	watch := &FleetPusherWatch{
		HeartbeatPath: heartbeatPath,
		Now:           fixedNow(now),
		MaxAge:        30 * time.Minute,
		BootTime: func(context.Context) (time.Time, error) {
			return boot, nil
		},
		Poster: poster,
		Cards:  &postedCardLookup{poster: poster},
		Logf: func(format string, args ...any) {
			*lines = append(*lines, fmt.Sprintf(format, args...))
		},
	}
	return watch, poster, lines
}

var _ = ginkgo.Describe("FleetPusherWatch", func() {
	var dir string
	var heartbeat string
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	oldBoot := now.Add(-48 * time.Hour)

	ginkgo.BeforeEach(func() {
		dir = ginkgo.GinkgoT().TempDir()
		heartbeat = filepath.Join(dir, "heartbeat")
	})

	ginkgo.It("posts the missing card when the heartbeat file does not exist", func() {
		watch, poster, lines := watchHarness(heartbeat, now, oldBoot)
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
		gomega.Expect(poster.requests[0].DedupKey).To(gomega.Equal(DedupHeartbeatMissing))
		gomega.Expect(*lines).To(gomega.ContainElement(
			"job=fleet-pusher-watch action=post key=" + DedupHeartbeatMissing +
				" card=card-" + DedupHeartbeatMissing,
		))
	})

	ginkgo.It("posts nothing on a second cycle for the same failure", func() {
		watch, poster, lines := watchHarness(heartbeat, now, oldBoot)
		watch.Cycle(context.Background())
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
		gomega.Expect(*lines).To(gomega.ContainElement(
			"job=fleet-pusher-watch action=skip key=" + DedupHeartbeatMissing +
				" card=card-" + DedupHeartbeatMissing,
		))
	})

	ginkgo.It("posts the unparseable card for a non-ISO stamp", func() {
		gomega.Expect(os.WriteFile(heartbeat, []byte("not-a-stamp"), 0o644)).To(gomega.Succeed())
		watch, poster, lines := watchHarness(heartbeat, now, oldBoot)
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
		gomega.Expect(poster.requests[0].DedupKey).To(gomega.Equal(DedupHeartbeatUnparseable))
		gomega.Expect(*lines).To(gomega.ContainElement(
			"job=fleet-pusher-watch action=post key=" + DedupHeartbeatUnparseable +
				" card=card-" + DedupHeartbeatUnparseable,
		))
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("posts the stale card for an old stamp", func() {
		stamp := now.Add(-time.Hour).Format("2006-01-02T15:04:05Z")
		gomega.Expect(os.WriteFile(heartbeat, []byte(stamp), 0o644)).To(gomega.Succeed())
		watch, poster, lines := watchHarness(heartbeat, now, oldBoot)
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
		gomega.Expect(poster.requests[0].DedupKey).To(gomega.Equal(DedupHeartbeatStale))
		gomega.Expect(*lines).To(gomega.ContainElement(
			"job=fleet-pusher-watch action=post key=" + DedupHeartbeatStale +
				" card=card-" + DedupHeartbeatStale,
		))
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.HaveLen(1))
	})

	ginkgo.It("stays quiet inside the startup grace", func() {
		watch, poster, lines := watchHarness(heartbeat, now, now.Add(-100*time.Second))
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.BeEmpty())
		gomega.Expect(*lines).To(gomega.ContainElement(
			"job=fleet-pusher-watch action=grace age=100s",
		))
	})

	ginkgo.It("reports ok for a fresh stamp and posts nothing", func() {
		stamp := now.Add(-10 * time.Second).Format("2006-01-02T15:04:05Z")
		gomega.Expect(os.WriteFile(heartbeat, []byte(stamp), 0o644)).To(gomega.Succeed())
		watch, poster, lines := watchHarness(heartbeat, now, oldBoot)
		watch.Cycle(context.Background())
		gomega.Expect(poster.requests).To(gomega.BeEmpty())
		gomega.Expect(*lines).To(gomega.ContainElement(
			"job=fleet-pusher-watch action=ok age=10s",
		))
	})
})
