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

// adoptReportSuite pins the accounting a restart owes an operator: a record the
// successor does not adopt is reported with its id (or directory) and a reason,
// so a dropped row is never indistinguishable from one that had already exited.
var _ = ginkgo.Describe("re-adoption accounting", func() {
	var (
		ctx   context.Context
		dir   string
		store SessionStore
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
		store = NewSessionStore(filepath.Join(dir, "sessions"))
	})

	newSupervisor := func() *supervisor {
		return &supervisor{
			now:   libtime.NewCurrentDateTime(),
			store: store,
		}
	}

	ginkgo.It("reports a record with no id by its directory and never touches the store", func() {
		// A record whose own id is empty is keyed only by its directory. Dir("")
		// is the sessions root, so an implementation that derives a path from the
		// id would remove every session — this asserts the root survives.
		root := filepath.Join(dir, "sessions")
		orphan := filepath.Join(root, "orphan-dir")
		gomega.Expect(os.MkdirAll(orphan, 0o700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(
			filepath.Join(orphan, sessionRecordName), []byte(`{"id":""}`), 0o600,
		)).To(gomega.Succeed())

		report, err := newSupervisor().Adopt(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Adopted).To(gomega.BeEmpty())
		gomega.Expect(report.Skipped).To(gomega.ConsistOf(AdoptSkip{
			ID:     "",
			Dir:    "orphan-dir",
			Reason: "record has no id",
		}))
		gomega.Expect(report.SkippedSessions()).To(gomega.ConsistOf("orphan-dir"))
		gomega.Expect(orphan).To(gomega.BeADirectory())
		gomega.Expect(root).To(gomega.BeADirectory())
	})

	ginkgo.It("reports a record whose start time does not match the live pid", func() {
		live := SessionRecord{
			ID:             "22222222-3333-4444-8555-666666666666",
			ChildPID:       os.Getpid(),
			Cwd:            dir,
			StartedAt:      time.Now().UTC(),
			ChildStartedAt: time.Unix(1, 0).UTC(),
		}
		files, err := store.Create(ctx, live)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_ = files.Stdin.Close()
		_ = files.EventsWriter.Close()

		report, err := newSupervisor().Adopt(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(report.Adopted).To(gomega.BeEmpty())
		gomega.Expect(report.Skipped).To(gomega.ConsistOf(AdoptSkip{
			ID:     live.ID,
			Dir:    live.ID,
			Reason: "record start time does not match the live pid",
		}))
		gomega.Expect(report.SkippedSessions()).To(gomega.ConsistOf(live.ID))
	})
})
