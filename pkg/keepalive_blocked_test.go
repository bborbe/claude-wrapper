// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("NewVaultAPIBlockers", func() {
	ctx := context.Background()

	ginkgo.It("returns the unresolved blocker of a gated task", func() {
		client := fakeVaultAPI{all: map[string][]VaultAPIRow{
			"task": {
				{Title: "Gated Row", Vault: "private-personal", Status: "in_progress", BlockedBy: []string{"[[Blocker A]]"}},
				{Title: "Blocker A", Vault: "private-personal", Status: "in_progress"},
			},
		}}

		blockers, err := NewVaultAPIBlockers(client)(ctx, "Gated Row")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(blockers).To(gomega.Equal([]string{"Blocker A"}))
	})

	// ⚠️ The resolved-blocker control: a blocker the index reports `completed`
	// must NOT keep the row gated, or a naive `len(blocked_by) > 0` skip would
	// gate every row forever once its blocker had closed.
	ginkgo.It("returns nothing when every blocker is completed", func() {
		client := fakeVaultAPI{all: map[string][]VaultAPIRow{
			"task": {
				{Title: "Freed Row", Vault: "private-personal", Status: "in_progress", BlockedBy: []string{"[[Blocker A]]"}},
				{Title: "Blocker A", Vault: "private-personal", Status: "completed"},
			},
		}}

		blockers, err := NewVaultAPIBlockers(client)(ctx, "Freed Row")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(blockers).To(gomega.BeEmpty())
	})

	ginkgo.It("returns nothing for a task the index does not carry", func() {
		client := fakeVaultAPI{all: map[string][]VaultAPIRow{
			"task": {{Title: "Some Other Row", Vault: "private-personal", Status: "in_progress"}},
		}}

		blockers, err := NewVaultAPIBlockers(client)(ctx, "Unknown Row")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(blockers).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a list failure rather than reporting an unblocked row", func() {
		client := fakeVaultAPI{err: errors.New(ctx, "vault-api unavailable")}

		_, err := NewVaultAPIBlockers(client)(ctx, "Gated Row")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses a reader wired without a client", func() {
		_, err := NewVaultAPIBlockers(nil)(ctx, "Gated Row")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
