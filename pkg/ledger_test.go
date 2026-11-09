// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("LedgerRow", func() {
	ginkgo.It("decodes the ledger status", func() {
		// The status is what the heartbeat poster keys its skip on, and this repo's
		// client silently discarded it before the field existed — so the decode is
		// pinned here rather than assumed from the store's payload.
		//
		// ⚠️ The absence of the key must decode to the empty string, NOT to a
		// terminal value: a row written before the field existed proves nothing
		// about the session, and "unknown" is the only safe reading.
		var row LedgerRow
		err := json.Unmarshal(
			[]byte(`{"session_id":"a60db816","status":"done","spawned_at":"2026-11-08T12:25:12.802Z"}`),
			&row,
		)
		gomega.Expect(err).To(gomega.Succeed())
		ginkgo.GinkgoWriter.Printf("status=%s\n", row.Status)
		gomega.Expect(row.Status).To(gomega.Equal("done"))
		gomega.Expect(row.SessionID).To(gomega.Equal("a60db816"))

		var absent LedgerRow
		gomega.Expect(json.Unmarshal([]byte(`{"session_id":"x"}`), &absent)).To(gomega.Succeed())
		gomega.Expect(absent.Status).To(gomega.BeEmpty())
	})
})
