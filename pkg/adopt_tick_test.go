// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("stranded workers", func() {
	rows := []LedgerRow{
		{SessionID: "m1", Role: "manager"},
		{SessionID: "w1", Role: "agent", SpawnerSessionID: "m1"},
		{SessionID: "w2", Role: "agent", SpawnerSessionID: "m-live"},
		{SessionID: "m-live", Role: "manager"},
		{SessionID: "w3", Role: "agent", SpawnerSessionID: "not-a-manager"},
		{SessionID: "w4", Role: "agent", SpawnerSessionID: ""},
	}

	ginkgo.It("includes a live worker whose manager exited", func() {
		live := map[string]bool{"w1": true, "m-live": true}

		out := strandedWorkers(rows, live)

		gomega.Expect(out["m1"]).To(gomega.Equal([]string{"w1"}))
	})

	ginkgo.It("excludes a worker whose parent is still live", func() {
		live := map[string]bool{"w2": true, "m-live": true}

		out := strandedWorkers(rows, live)

		gomega.Expect(out).To(gomega.BeEmpty())
	})

	ginkgo.It("excludes a worker that also exited", func() {
		live := map[string]bool{}

		out := strandedWorkers(rows, live)

		gomega.Expect(out).To(gomega.BeEmpty())
	})

	ginkgo.It("excludes a worker whose parent is not a manager", func() {
		live := map[string]bool{"w3": true}

		out := strandedWorkers(rows, live)

		gomega.Expect(out).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("already owned", func() {
	ginkgo.It("is owned when a live manager holds it", func() {
		gomega.Expect(alreadyOwned("m1", map[string]bool{"m1": true})).To(gomega.BeTrue())
	})

	ginkgo.It("is not owned when the holder is gone", func() {
		gomega.Expect(alreadyOwned("m1", map[string]bool{})).To(gomega.BeFalse())
	})

	ginkgo.It("is not owned when unclaimed", func() {
		gomega.Expect(alreadyOwned("", map[string]bool{"m1": true})).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("resolve adopter", func() {
	subjectOf := func(sessionID string) string {
		switch sessionID {
		case "m1", "m2":
			return "subj-a"
		case "m3":
			return "subj-b"
		default:
			return ""
		}
	}

	ginkgo.It("adopts through the single live same-subject successor", func() {
		live := map[string]bool{"m2": true}

		adopter, how := resolveAdopter("m1", live, "self", subjectOf)

		gomega.Expect(adopter).To(gomega.Equal("m2"))
		gomega.Expect(how).To(gomega.Equal("successor"))
	})

	ginkgo.It("falls back to self when no successor matches", func() {
		live := map[string]bool{"m3": true}

		adopter, how := resolveAdopter("m1", live, "self", subjectOf)

		gomega.Expect(adopter).To(gomega.Equal("self"))
		gomega.Expect(how).To(gomega.Equal("fallback"))
	})

	ginkgo.It("falls back to self when two successors match", func() {
		live := map[string]bool{"m2": true, "m2b": true}
		two := func(id string) string {
			if id == "m1" || id == "m2" || id == "m2b" {
				return "subj-a"
			}
			return ""
		}

		adopter, how := resolveAdopter("m1", live, "self", two)

		gomega.Expect(adopter).To(gomega.Equal("self"))
		gomega.Expect(how).To(gomega.Equal("fallback"))
	})
})

// fakeClaims records ownership claims.
type fakeClaims struct {
	holder  map[string]string
	claimed []string
}

func (f *fakeClaims) Holder(_ context.Context, worker string) (string, error) {
	return f.holder[worker], nil
}

func (f *fakeClaims) Claim(_ context.Context, worker, manager, _ string) error {
	f.claimed = append(f.claimed, worker+"->"+manager)
	return nil
}

// fakeSubjects serves a fixed subject and stand-down map.
type fakeSubjects struct {
	subject map[string]string
	stopped map[string]string
}

func (f *fakeSubjects) Subject(_ context.Context, id string) (string, error) {
	return f.subject[id], nil
}

func (f *fakeSubjects) Stopped(_ context.Context, subject string) (string, bool, error) {
	marker, ok := f.stopped[subject]
	return marker, ok, nil
}

var _ = ginkgo.Describe("adopt tick run", func() {
	ctx := context.Background()
	rows := []LedgerRow{
		{SessionID: "m1", Role: "manager"},
		{SessionID: "w1", Role: "agent", SpawnerSessionID: "m1"},
	}

	newTick := func(claims *fakeClaims, subjects *fakeSubjects) *AdoptTick {
		return &AdoptTick{
			Ledger:   fakeLedgerRows{rows: rows},
			Registry: &fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "w1"}}},
			Claims:   claims,
			Subjects: subjects,
			Self:     "self",
		}
	}

	ginkgo.It("adopts a stranded worker through self", func() {
		claims := &fakeClaims{holder: map[string]string{}}

		result, err := newTick(claims, &fakeSubjects{}).Run(ctx)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.Adopted).To(gomega.Equal([]string{"w1"}))
		gomega.Expect(claims.claimed).To(gomega.Equal([]string{"w1->self"}))
	})

	ginkgo.It("does not re-adopt a worker the daemon itself already holds", func() {
		// The adopter is live by construction. Without that, the daemon's own
		// claim reads as a dead holder on the next round and every stranded
		// worker is re-adopted on every tick, rewriting the store each time.
		claims := &fakeClaims{holder: map[string]string{"w1": "self"}}

		result, err := newTick(claims, &fakeSubjects{}).Run(ctx)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.Adopted).To(gomega.BeEmpty())
		gomega.Expect(claims.claimed).To(gomega.BeEmpty())
	})

	ginkgo.It("skips a stood-down subject", func() {
		claims := &fakeClaims{holder: map[string]string{}}
		subjects := &fakeSubjects{
			subject: map[string]string{"m1": "subj-a"},
			stopped: map[string]string{"subj-a": "goal--subj-a.stopped"},
		}

		result, err := newTick(claims, subjects).Run(ctx)

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.Adopted).To(gomega.BeEmpty())
		gomega.Expect(result.Skipped).To(gomega.HaveLen(1))
		gomega.Expect(claims.claimed).To(gomega.BeEmpty())
	})
})
