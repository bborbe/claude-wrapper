// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// writeRecord writes a worker-manager record for sessionID.
func writeRecord(dir, sessionID, subject, branch string) error {
	return os.WriteFile(
		filepath.Join(dir, sessionID+".json"),
		[]byte(`{"subject":"`+subject+`","branch":"`+branch+`"}`),
		0o600,
	)
}

var _ = ginkgo.Describe("SubjectStateReader", func() {
	var records, state string

	ginkgo.BeforeEach(func() {
		records = ginkgo.GinkgoT().TempDir()
		state = ginkgo.GinkgoT().TempDir()
	})

	// SC1's read half: a stand-down marker is the state the pusher was blind to.
	ginkgo.It("reports a subject with a stand-down marker as STOPPED", func() {
		gomega.Expect(writeRecord(records, "s1", "Trading Strategy Factory", "topic")).
			To(gomega.Succeed())
		gomega.Expect(os.WriteFile(
			filepath.Join(state, "topic--trading-strategy-factory.stopped"),
			[]byte("2026-11-23T20:51:26+01:00"), 0o600,
		)).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeTrue())
		gomega.Expect(got).To(gomega.Equal(SubjectStateStopped))
	})

	// The marker is probed on every tier, because a manager can be retired as a
	// goal, a topic, a fleet subject or a task.
	ginkgo.It("finds a stand-down recorded on a different tier", func() {
		gomega.Expect(writeRecord(records, "s1", "Some Subject", "goal")).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(
			filepath.Join(state, "task--some-subject.stopped"), []byte("x"), 0o600,
		)).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, _, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(got).To(gomega.Equal(SubjectStateStopped))
	})

	ginkgo.It("reports no state for a subject with no marker", func() {
		gomega.Expect(writeRecord(records, "s1", "Some Subject", "goal")).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeTrue())
		gomega.Expect(got).To(gomega.BeEmpty())
	})

	ginkgo.It("resolves nothing for a session with no record", func() {
		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		_, resolved, err := reader.State(context.Background(), "absent")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeFalse())
	})
})
