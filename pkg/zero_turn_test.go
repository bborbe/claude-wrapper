// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// failingWriter makes send's write fail, so the retry's failure path is
// exercised without a real child process.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func (failingWriter) Close() error { return nil }

var _ = ginkgo.Describe("zero-turn retry", func() {
	// The exit record's `turns=` field is only a discriminator if reap copies the
	// LIVE counter. c.session.AssistantTurns is assigned nowhere but snapshot()'s
	// clone, so passing c.session raw writes turns=0 for every child — a healthy
	// close and a stranded worker then read identically, which is the observability
	// the field exists to add.
	ginkgo.It("records the live assistant-turn count at reap", func() {
		dir := ginkgo.GinkgoT().TempDir()
		c := &child{
			session:      Session{ID: "sid-turns", Mode: SpawnModeHeadless},
			exitRecorder: &SessionExitFileRecorder{Dir: dir},
		}
		c.zeroTurns.turns = 4

		c.reap(nil)

		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-turns.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("turns=4"))
	})

	// The other half of the same discriminator: a turn that produced nothing must
	// record zero, not inherit a stale count from an earlier turn.
	ginkgo.It("records turns=0 for a zero-turn close", func() {
		dir := ginkgo.GinkgoT().TempDir()
		c := &child{
			session:      Session{ID: "sid-zero", Mode: SpawnModeHeadless},
			exitRecorder: &SessionExitFileRecorder{Dir: dir},
		}
		c.zeroTurns.turns = 0

		c.reap(nil)

		raw, err := os.ReadFile(filepath.Join(dir, "session-sid-zero.log"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("turns=0"))
	})

	// The reservation the race fix rests on. Between the budget spend and the
	// re-send's own pendingTurns++ the child reads idle, so a duplicate turn end
	// in that window would kill it and lose the one attempt the budget allows.
	// keepReasonLocked must report the gap as an open turn.
	ginkgo.It("keeps the child while a zero-turn retry is in flight", func() {
		c := &child{}
		c.retrying = true

		reason, keep := c.keepReasonLocked()

		gomega.Expect(keep).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.Equal(reasonRetryPending))
	})

	// Once the write has landed, the reservation is gone and the child is kept by
	// pendingTurns instead — an idle child with neither must be stoppable.
	ginkgo.It("stops keeping the child once the reservation is cleared", func() {
		c := &child{}
		c.retrying = false

		_, keep := c.keepReasonLocked()

		gomega.Expect(keep).To(gomega.BeFalse())
	})

	// A duplicate turn end must not spend a second budget slot while the first
	// re-send is still in flight.
	ginkgo.It("refuses a duplicate retry while one is already reserved", func() {
		c := &child{zeroTurns: zeroTurnGuard{text: "hello"}}
		c.retrying = true

		gomega.Expect(c.retryZeroTurn(context.Background())).To(gomega.BeFalse())
		gomega.Expect(c.zeroTurns.retries).To(gomega.Equal(0),
			"a duplicate must not charge the budget")
	})

	// A failed re-send must release the reservation, or a write error parks the
	// worker forever behind a keep reason nothing will clear.
	ginkgo.It("clears the reservation when the re-send fails", func() {
		c := &child{
			session: Session{ID: "sid-fail", Mode: SpawnModeHeadless},
			stdin:   failingWriter{},
			zeroTurns: zeroTurnGuard{
				text: "hello",
			},
		}

		gomega.Expect(c.retryZeroTurn(context.Background())).To(gomega.BeFalse())
		gomega.Expect(c.retrying).To(gomega.BeFalse(),
			"a failed re-send must not leave the child permanently kept")
		gomega.Expect(c.pendingTurns).To(gomega.Equal(0),
			"the rolled-back turn must not read as still open")
	})
})
