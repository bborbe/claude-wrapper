// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Openable", func() {
	// PR 1 shape test. The full F1–F5 fixture table lives in PR 2: F3 (expects
	// `open`) and F5 (expects reason `indeterminate`) encode the classifier-input
	// swap that PR 2 introduces, so they cannot pass on PR 1's branch. Here we pin
	// only what the current tick deterministically yields.
	ginkgo.It("reports the tick's decision per row", func() {
		now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
		rows := []FlaggedRow{
			{Kind: "task", Vault: "private-personal", Name: "f1-never-opened", Branch: "create", Liveness: "none", Phase: "planning", FlagSetBy: "operator"},
			{Kind: "task", Vault: "private-personal", Name: "f2-fresh", Branch: "resume", Liveness: "LIVE", SessionID: "11111111-1111-1111-1111-111111111111", Phase: "execution", FlagSetBy: "operator"},
			{Kind: "task", Vault: "private-personal", Name: "f4-blocked", Branch: "create", Liveness: "none", BlockedBy: []string{"[[x]]"}, FlagSetBy: "operator"},
		}
		tick := newTick(staticSource{rows: rows}, nil, nil)
		tick.Now = fixedNow(now)

		got, err := tick.Openable(context.Background())
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(got).To(gomega.HaveLen(3))

		byName := map[string]OpenableEntry{}
		for _, entry := range got {
			byName[entry.Name] = entry
			gomega.Expect(entry.Decision).To(
				gomega.BeElementOf(OpenableDecisionOpen, OpenableDecisionHeld))
			if entry.Decision == OpenableDecisionHeld {
				gomega.Expect(entry.Reason).ToNot(gomega.BeEmpty())
			}
		}
		gomega.Expect(byName["f1-never-opened"].Decision).To(gomega.Equal(OpenableDecisionOpen))
		gomega.Expect(byName["f2-fresh"].Decision).To(gomega.Equal(OpenableDecisionHeld))
		gomega.Expect(byName["f2-fresh"].Reason).To(gomega.Equal(reasonNotAbsent))
		gomega.Expect(byName["f4-blocked"].Decision).To(gomega.Equal(OpenableDecisionHeld))
		gomega.Expect(byName["f4-blocked"].Reason).To(gomega.Equal(reasonBlocked))
	})
})
