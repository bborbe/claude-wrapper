// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os/exec"
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
			c.publish(Event{"seq": float64(i)})
		}

		gomega.Expect(c.events).To(gomega.HaveLen(maxRetainedEvents))
		gomega.Expect(c.events[0]["seq"]).To(gomega.Equal(float64(overflow)),
			"the oldest retained event must be the one that fell inside the window")
		gomega.Expect(c.events[len(c.events)-1]["seq"]).To(gomega.Equal(float64(maxRetainedEvents+overflow-1)),
			"the newest event must always be retained")
	})

	ginkgo.It("keeps the retained events in the order they were published", func() {
		c := &child{}
		for i := 0; i < maxRetainedEvents+1; i++ {
			c.publish(Event{"seq": float64(i)})
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
			c.publish(Event{"seq": float64(i)})
		}

		event := Event{"seq": float64(0)}
		allocs := testing.AllocsPerRun(100, func() {
			c.publish(event)
		})
		gomega.Expect(allocs).To(gomega.BeNumerically("<=", 1),
			"publish at capacity must not allocate a fresh %d-element slice per event", maxRetainedEvents)
	})

	// Retention, not churn, sets the wrapper's footprint: a dead child keeps up to
	// maxRetainedEvents decoded maps for the rest of the process lifetime because
	// exited records are never removed from s.sessions. The record must survive —
	// only the event history goes.
	ginkgo.It("releases the replay buffer when the child exits", func() {
		cmd := exec.Command("true")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())

		c := &child{cmd: cmd, subscribers: map[int]chan Event{}}
		for i := 0; i < 32; i++ {
			c.publish(Event{"seq": float64(i)})
		}
		gomega.Expect(c.events).To(gomega.HaveLen(32))

		c.reap()

		gomega.Expect(c.session.State).To(gomega.Equal(StateExited),
			"the record itself must survive for listing")
		gomega.Expect(c.events).To(gomega.BeEmpty(),
			"an exited child must not keep its decoded event maps for the process lifetime")
	})

	// reap and consume run concurrently; an event read from stdout after reap
	// released the buffer must not rebuild it.
	ginkgo.It("does not re-retain events once the child has exited", func() {
		c := &child{}
		c.session.State = StateExited

		c.publish(Event{"seq": float64(0)})

		gomega.Expect(c.events).To(gomega.BeEmpty(),
			"a late event must not rebuild the released buffer")
	})
})

// BenchmarkPublishAtCapacity reports the per-event allocation cost once the
// replay buffer is full — the state a long-running wrapper spends all its time
// in. Run with `go test ./pkg -bench PublishAtCapacity -benchmem -run '^$'`.
func BenchmarkPublishAtCapacity(b *testing.B) {
	c := &child{}
	for i := 0; i < maxRetainedEvents+1; i++ {
		c.publish(Event{"seq": i})
	}
	event := Event{"seq": 0}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.publish(event)
	}
}
