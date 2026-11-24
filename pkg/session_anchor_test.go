// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("VaultAPIAnchorReader", func() {
	// A goal that anchors a manager is usually not itself eligible to open, so the
	// filtered rows would answer "nothing anchors this" — ListAll is load-bearing.
	ginkgo.It("finds a session anchored to a goal that is not itself eligible", func() {
		reader := VaultAPIAnchorReader{Client: fakeVaultAPI{all: map[string][]VaultAPIRow{
			"goal": {{ID: "g1", SessionID: "s1", Status: "in_progress"}},
		}}}
		anchored, err := reader.AnchoredSessions(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(anchored).To(gomega.HaveKey("s1"))
	})

	ginkgo.It("omits a session no page names", func() {
		reader := VaultAPIAnchorReader{Client: fakeVaultAPI{all: map[string][]VaultAPIRow{
			"task": {{ID: "t1", SessionID: "other", Status: "in_progress"}},
		}}}
		anchored, err := reader.AnchoredSessions(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(anchored).NotTo(gomega.HaveKey("s1"))
	})

	// A terminal page still names the session but offers it no next subtask, so the
	// wake would be as unactionable as one to an unanchored session.
	ginkgo.It("omits a session whose only anchor is terminal", func() {
		reader := VaultAPIAnchorReader{Client: fakeVaultAPI{all: map[string][]VaultAPIRow{
			"task": {{ID: "t1", SessionID: "s1", Status: "completed"}},
		}}}
		anchored, err := reader.AnchoredSessions(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(anchored).NotTo(gomega.HaveKey("s1"))
	})

	// The empty-success path must not read as "nothing anchors anything": that would
	// dark-fleet every session with no error to notice.
	ginkgo.It("refuses an index that served no rows at all", func() {
		reader := VaultAPIAnchorReader{Client: fakeVaultAPI{}}
		_, err := reader.AnchoredSessions(context.Background())
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	// A partial answer must never read as a complete one: the caller fails open,
	// and an error is what tells it the question was not answered.
	ginkgo.It("returns the read error rather than a partial set", func() {
		reader := VaultAPIAnchorReader{Client: fakeVaultAPI{err: errors.New("boom")}}
		_, err := reader.AnchoredSessions(context.Background())
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses when no client is wired", func() {
		_, err := VaultAPIAnchorReader{}.AnchoredSessions(context.Background())
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
