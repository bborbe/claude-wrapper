// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("pending records", func() {
	ctx := context.Background()

	ginkgo.Describe("PendingRecordDir", func() {
		ginkgo.It("round-trips a record through a fresh store (survives a reload)", func() {
			dir := ginkgo.GinkgoT().TempDir()
			gomega.Expect(NewPendingRecordDir(dir).Add(ctx, PendingRecord{
				SessionID: "s1", Vault: "v", Task: "t", Kind: "worker",
			})).To(gomega.Succeed())

			// A NEW store over the same dir stands in for the daemon reload.
			records, err := NewPendingRecordDir(dir).List(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(records).To(gomega.Equal([]PendingRecord{{SessionID: "s1", Vault: "v", Task: "t", Kind: "worker"}}))
		})

		ginkgo.It("is idempotent on re-add", func() {
			store := NewPendingRecordDir(ginkgo.GinkgoT().TempDir())
			gomega.Expect(store.Add(ctx, PendingRecord{SessionID: "s1", Kind: "manager"})).To(gomega.Succeed())
			gomega.Expect(store.Add(ctx, PendingRecord{SessionID: "s1", Kind: "manager"})).To(gomega.Succeed())

			records, err := store.List(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(records).To(gomega.HaveLen(1))
		})

		ginkgo.It("returns no records for a directory that does not exist yet", func() {
			records, err := NewPendingRecordDir(ginkgo.GinkgoT().TempDir() + "/absent").List(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(records).To(gomega.BeEmpty())
		})

		ginkgo.It("removes a record, and removing a missing one is not an error", func() {
			store := NewPendingRecordDir(ginkgo.GinkgoT().TempDir())
			gomega.Expect(store.Add(ctx, PendingRecord{SessionID: "s1"})).To(gomega.Succeed())

			gomega.Expect(store.Remove(ctx, "s1")).To(gomega.Succeed())
			gomega.Expect(store.Remove(ctx, "s1")).To(gomega.Succeed())

			records, err := store.List(ctx)
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(records).To(gomega.BeEmpty())
		})

		ginkgo.It("refuses an empty session id", func() {
			gomega.Expect(NewPendingRecordDir(ginkgo.GinkgoT().TempDir()).Add(ctx, PendingRecord{})).
				To(gomega.HaveOccurred())
		})

		// A hostile id must not write outside the store directory.
		ginkgo.It("does not let a path separator escape the directory", func() {
			dir := ginkgo.GinkgoT().TempDir()
			store := NewPendingRecordDir(dir)

			gomega.Expect(store.Add(ctx, PendingRecord{SessionID: "../escape"})).To(gomega.Succeed())

			entries, err := os.ReadDir(dir)
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(entries).To(gomega.HaveLen(1))
			gomega.Expect(entries[0].Name()).NotTo(gomega.ContainSubstring("/"))
		})
	})

	ginkgo.Describe("DrainPendingRecords", func() {
		clock := func() libtime.CurrentDateTimeGetter { return libtime.NewCurrentDateTime() }

		ginkgo.It("records live entries, leaves non-live, and removes the recorded", func() {
			store := NewPendingRecordDir(ginkgo.GinkgoT().TempDir())
			gomega.Expect(store.Add(ctx, PendingRecord{SessionID: "live-1", Kind: "manager"})).To(gomega.Succeed())
			gomega.Expect(store.Add(ctx, PendingRecord{SessionID: "starting-1", Kind: "manager"})).To(gomega.Succeed())

			recorded := &[]string{}
			count, _, err := DrainPendingRecords(
				ctx, store,
				func(_ context.Context, sessionID string) (bool, error) { return sessionID == "live-1", nil },
				func(_ context.Context, p PendingRecord) error {
					*recorded = append(*recorded, p.SessionID)
					return nil
				},
				0, clock(),
			)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(count).To(gomega.Equal(1))
			gomega.Expect(*recorded).To(gomega.Equal([]string{"live-1"}))

			// The live one is gone; the starting one stays for a later tick.
			remaining, listErr := store.List(ctx)
			gomega.Expect(listErr).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(remaining).To(gomega.Equal([]PendingRecord{{SessionID: "starting-1", Kind: "manager"}}))
		})

		// A record failure must leave the entry on disk so the next tick retries.
		ginkgo.It("keeps the entry when the record action fails", func() {
			store := NewPendingRecordDir(ginkgo.GinkgoT().TempDir())
			gomega.Expect(store.Add(ctx, PendingRecord{SessionID: "live-1"})).To(gomega.Succeed())

			_, _, err := DrainPendingRecords(
				ctx, store,
				func(context.Context, string) (bool, error) { return true, nil },
				func(context.Context, PendingRecord) error { return errors.New(ctx, "record refused") },
				0, clock(),
			)

			gomega.Expect(err).To(gomega.HaveOccurred())
			remaining, listErr := store.List(ctx)
			gomega.Expect(listErr).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(remaining).To(gomega.HaveLen(1))
		})

		// ⚠️ An entry whose id never read live within maxAge is DROPPED, not retried
		// forever — a resume that never came up must not sit on disk.
		ginkgo.It("drops an entry older than the max age as stale", func() {
			store := NewPendingRecordDir(ginkgo.GinkgoT().TempDir())
			gomega.Expect(store.Add(ctx, PendingRecord{
				SessionID: "old-1", Kind: "manager", CreatedAt: time.Now().Add(-2 * time.Hour),
			})).To(gomega.Succeed())

			recorded := &[]string{}
			count, stale, err := DrainPendingRecords(
				ctx, store,
				func(context.Context, string) (bool, error) { return true, nil },
				func(_ context.Context, p PendingRecord) error { *recorded = append(*recorded, p.SessionID); return nil },
				time.Hour, clock(),
			)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(count).To(gomega.Equal(0))
			gomega.Expect(stale).To(gomega.Equal(1))
			gomega.Expect(*recorded).To(gomega.BeEmpty())
			remaining, _ := store.List(ctx)
			gomega.Expect(remaining).To(gomega.BeEmpty())
		})

		ginkgo.DescribeTable("refuses a drain wired without a collaborator",
			func(store PendingRecordStore,
				live func(context.Context, string) (bool, error),
				record func(context.Context, PendingRecord) error) {
				_, _, err := DrainPendingRecords(ctx, store, live, record, 0, clock())
				gomega.Expect(err).To(gomega.HaveOccurred())
			},
			ginkgo.Entry("no store", nil,
				func(context.Context, string) (bool, error) { return true, nil },
				func(context.Context, PendingRecord) error { return nil }),
			ginkgo.Entry("no live check", NewPendingRecordDir("/tmp/x"), nil,
				func(context.Context, PendingRecord) error { return nil }),
			ginkgo.Entry("no record action", NewPendingRecordDir("/tmp/x"),
				func(context.Context, string) (bool, error) { return true, nil }, nil),
		)
	})
})
