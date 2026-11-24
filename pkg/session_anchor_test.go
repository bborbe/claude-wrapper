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
			"goal": {{ID: "g1", SessionID: "s1"}},
		}}}
		anchored, err := reader.Anchored(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(anchored).To(gomega.BeTrue())
	})

	ginkgo.It("reports a session no page names as unanchored", func() {
		reader := VaultAPIAnchorReader{Client: fakeVaultAPI{all: map[string][]VaultAPIRow{
			"task": {{ID: "t1", SessionID: "other"}},
		}}}
		anchored, err := reader.Anchored(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(anchored).To(gomega.BeFalse())
	})

	// A partial answer must never read as a complete one: the caller fails open,
	// and an error is what tells it the question was not answered.
	ginkgo.It("returns the read error rather than a partial verdict", func() {
		reader := VaultAPIAnchorReader{Client: fakeVaultAPI{err: errors.New("boom")}}
		_, err := reader.Anchored(context.Background(), "s1")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("answers anchored when no client is wired", func() {
		anchored, err := VaultAPIAnchorReader{}.Anchored(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(anchored).To(gomega.BeTrue())
	})
})
