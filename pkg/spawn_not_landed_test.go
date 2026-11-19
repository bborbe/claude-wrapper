// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("spawn-not-landed", func() {
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	row := FlaggedRow{Kind: "task", Name: "Some Task"}

	newTick := func(childPID int) *FlaggedTick {
		store := NewSessionStore(ginkgo.GinkgoT().TempDir())
		_, err := store.Create(ctx, SessionRecord{
			ID:       "s1",
			ChildPID: childPID,
			Options:  SpawnOptions{Label: row.Name},
		})
		gomega.Expect(err).To(gomega.BeNil())
		return &FlaggedTick{
			Sessions: store,
			Now:      fixedNow(now),
			opened:   map[string]time.Time{labelKey(row.Name): now.Add(-20 * time.Minute)},
		}
	}

	ginkgo.It("is due when the spawned child exited with no session id", func() {
		// A pid that does not exist reads as an exited child.
		tick := newTick(999999)

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeTrue())
	})

	ginkgo.It("is not due when the child is still alive", func() {
		tick := newTick(os.Getpid())

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeFalse())
	})

	ginkgo.It("is not due before the 10-min window closes", func() {
		tick := newTick(999999)
		tick.opened[labelKey(row.Name)] = now.Add(-2 * time.Minute)

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeFalse())
	})

	ginkgo.It("is not due once the retry has been issued", func() {
		tick := newTick(999999)
		tick.rememberSpawnNotLanded(row)

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeFalse())
	})

	ginkgo.It("is not due for a row that already has a session id", func() {
		tick := newTick(999999)
		row := FlaggedRow{Kind: "task", Name: "Some Task", SessionID: "sid-1"}

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeFalse())
	})

	ginkgo.It("is not due when one of two same-label records is still alive", func() {
		tick := newTick(999999)
		// A second record under the same label, with a live pid.
		_, err := tick.Sessions.Create(ctx, SessionRecord{
			ID:       "s2",
			ChildPID: os.Getpid(),
			Options:  SpawnOptions{Label: row.Name},
		})
		gomega.Expect(err).To(gomega.BeNil())

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeFalse())
	})

	ginkgo.It("is not due when no record carries the label", func() {
		tick := &FlaggedTick{
			Sessions: NewSessionStore(ginkgo.GinkgoT().TempDir()),
			Now:      fixedNow(now),
			opened:   map[string]time.Time{labelKey(row.Name): now.Add(-20 * time.Minute)},
		}

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeFalse())
	})

	ginkgo.It("is not due when the child-table read fails", func() {
		tick := &FlaggedTick{
			Sessions: errSessionStore{},
			Now:      fixedNow(now),
			opened:   map[string]time.Time{labelKey(row.Name): now.Add(-20 * time.Minute)},
		}

		gomega.Expect(tick.spawnNotLandedDue(ctx, row)).To(gomega.BeFalse())
	})
})

// errSessionStore fails every List, so spawnNotLandedDue's error path is covered.
type errSessionStore struct{ SessionStore }

func (errSessionStore) List(context.Context) ([]SessionRecord, error) {
	return nil, stderrors.New("store down")
}
