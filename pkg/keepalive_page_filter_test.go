// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("FilterCandidates", func() {
	ginkgo.It("drops a page with no session id, whatever its status", func() {
		refs := FilterCandidates([]KeepalivePage{
			{Vault: "v", Name: "no-session", SessionID: "", Status: "in_progress"},
		}, KeepaliveKindTask)

		gomega.Expect(refs).To(gomega.BeEmpty())
	})

	// ⚠️ A task page must be in_progress: a completed page keeps its session id but
	// is not a live worker.
	ginkgo.It("keeps only in_progress task pages", func() {
		refs := FilterCandidates([]KeepalivePage{
			{Vault: "v", Name: "live", SessionID: "s1", Status: "in_progress"},
			{Vault: "v", Name: "done", SessionID: "s2", Status: "completed"},
			{Vault: "v", Name: "aborted", SessionID: "s3", Status: "aborted"},
			{Vault: "v", Name: "backlog", SessionID: "s4", Status: "backlog"},
		}, KeepaliveKindTask)

		gomega.Expect(refs).To(gomega.Equal([]KeepalivePageRef{{Vault: "v", Name: "live", SessionID: "s1"}}))
	})

	// A topic page has no status vocabulary — only the session requirement applies.
	ginkgo.It("keeps any topic page that carries a session, regardless of status", func() {
		refs := FilterCandidates([]KeepalivePage{
			{Vault: "v", Name: "topic-a", SessionID: "s1", Status: ""},
			{Vault: "v", Name: "topic-b", SessionID: "s2", Status: "anything"},
			{Vault: "v", Name: "topic-c", SessionID: "", Status: ""},
		}, KeepaliveKindTopic)

		gomega.Expect(refs).To(gomega.Equal([]KeepalivePageRef{
			{Vault: "v", Name: "topic-a", SessionID: "s1"}, {Vault: "v", Name: "topic-b", SessionID: "s2"},
		}))
	})
})
