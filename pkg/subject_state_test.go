// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"time"

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

// writeMarker writes an empty stand-down marker into the state dir.
func writeMarker(dir, name string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte("2026-11-23T20:51:26+01:00"), 0o600)
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
		gomega.Expect(writeMarker(state, "topic--trading-strategy-factory.stopped")).
			To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeTrue())
		gomega.Expect(got).To(gomega.Equal(SubjectStateStopped))
	})

	// Pre-split records live under the bare base name; the reference `resolve()`
	// honours them, so a subject that never re-armed is still recognised.
	ginkgo.It("honours a legacy pre-split stand-down marker", func() {
		gomega.Expect(writeRecord(records, "s1", "Driftline", "topic")).To(gomega.Succeed())
		gomega.Expect(writeMarker(state, "driftline.stopped")).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, _, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(got).To(gomega.Equal(SubjectStateStopped))
	})

	// ⚠️ One tier only. A topic and a goal may share a name, so a marker on
	// another tier must NOT skip this subject — that would be the original defect
	// mirrored, and permanent on the wake path.
	ginkgo.It("ignores a stand-down recorded on another tier", func() {
		gomega.Expect(writeRecord(records, "s1", "Shared Name", "topic")).To(gomega.Succeed())
		gomega.Expect(writeMarker(state, "goal--shared-name.stopped")).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeTrue())
		gomega.Expect(got).To(gomega.BeEmpty())
	})

	ginkgo.It("reports a future sleep marker as SLEEPING", func() {
		gomega.Expect(writeRecord(records, "s1", "Sleeper", "goal")).To(gomega.Succeed())
		until := time.Now().Add(time.Hour).Format(time.RFC3339)
		gomega.Expect(os.WriteFile(
			filepath.Join(state, "goal--sleeper.sleeping"), []byte(until), 0o600,
		)).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, _, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(got).To(gomega.Equal(SubjectStateSleeping))
	})

	ginkgo.It("reports an elapsed sleep marker as awake", func() {
		gomega.Expect(writeRecord(records, "s1", "Sleeper", "goal")).To(gomega.Succeed())
		until := time.Now().Add(-time.Hour).Format(time.RFC3339)
		gomega.Expect(os.WriteFile(
			filepath.Join(state, "goal--sleeper.sleeping"), []byte(until), 0o600,
		)).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, _, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(got).To(gomega.BeEmpty())
	})

	ginkgo.It("reports no state for a subject with no marker", func() {
		gomega.Expect(writeRecord(records, "s1", "Some Subject", "goal")).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		got, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeTrue())
		gomega.Expect(got).To(gomega.BeEmpty())
	})

	ginkgo.It("resolves nothing for a record with no subject or branch", func() {
		gomega.Expect(writeRecord(records, "s1", "", "goal")).To(gomega.Succeed())

		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		_, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeFalse())
	})

	ginkgo.It("resolves nothing for a session with no record", func() {
		reader := NewSubjectStateReader(records, state, libtime.NewCurrentDateTime())
		_, resolved, err := reader.State(context.Background(), "absent")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeFalse())
	})
})
