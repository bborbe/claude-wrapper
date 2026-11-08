// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Qualified ginkgo/gomega, not dot-imports: ginkgo exports a `Pending` decorator
// that collides with this package's own `Pending` type (see pkg_suite_test.go).
var _ = ginkgo.Describe("child event retention", func() {

	// The replay buffer is a bounded tail: a subscriber that arrives mid-turn sees
	// the recent events and nothing older. These specs pin that contract, which the
	// in-place compaction must preserve exactly.
	ginkgo.It("retains exactly the most recent maxRetainedEvents events", func() {
		c := &child{}
		const overflow = 10
		for i := 0; i < maxRetainedEvents+overflow; i++ {
			c.publish(i, Event{"seq": float64(i)})
		}

		gomega.Expect(c.events).To(gomega.HaveLen(maxRetainedEvents))
		gomega.Expect(c.events[0]["seq"]).To(gomega.Equal(float64(overflow)),
			"the oldest retained event must be the one that fell inside the window")
		// firstSeq is the absolute sequence of events[0]; a resume point is checked
		// against it, so a trim that leaves it behind makes a retained event look
		// like a gap and a dropped one look valid.
		gomega.Expect(c.firstSeq).To(gomega.Equal(overflow),
			"trimming must advance firstSeq by the number of events dropped")
		gomega.Expect(c.events[len(c.events)-1]["seq"]).To(gomega.Equal(float64(maxRetainedEvents+overflow-1)),
			"the newest event must always be retained")
	})

	ginkgo.It("keeps the retained events in the order they were published", func() {
		c := &child{}
		for i := 0; i < maxRetainedEvents+1; i++ {
			c.publish(i, Event{"seq": float64(i)})
		}

		for i := 1; i < len(c.events); i++ {
			gomega.Expect(c.events[i]["seq"]).To(gomega.BeNumerically(">", c.events[i-1]["seq"]),
				"event %d is out of order after compaction", i)
		}
	})

	// The defect this guards: reallocating the retained window per event made
	// publish the wrapper's dominant allocator once a child passed 4096 events.
	// A cap-sized copy per event is what the assertion below forbids.
	ginkgo.It("reuses the buffer instead of reallocating once at capacity", func() {
		c := &child{}
		for i := 0; i < maxRetainedEvents+1; i++ {
			c.publish(i, Event{"seq": float64(i)})
		}

		event := Event{"seq": float64(0)}
		allocs := testing.AllocsPerRun(100, func() {
			c.publish(maxRetainedEvents+1, event)
		})
		gomega.Expect(allocs).To(gomega.BeNumerically("<=", 1),
			"publish at capacity must not allocate a fresh %d-element slice per event", maxRetainedEvents)
	})
})

// BenchmarkPublishAtCapacity reports the per-event allocation cost once the
// replay buffer is full — the state a long-running wrapper spends all its time
// in. Run with `go test ./pkg -bench PublishAtCapacity -benchmem -run '^$'`.
func BenchmarkPublishAtCapacity(b *testing.B) {
	c := &child{}
	for i := 0; i < maxRetainedEvents+1; i++ {
		c.publish(i, Event{"seq": i})
	}
	event := Event{"seq": 0}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.publish(maxRetainedEvents+1, event)
	}
}
