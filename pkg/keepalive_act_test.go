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

var _ = ginkgo.Describe("NewKeepaliveAct", func() {
	ctx := context.Background()

	type sent struct{ sessionID, text string }

	act := func(sendErr error) (func(context.Context, string, string) error, *[]sent) {
		sentTo := &[]sent{}
		return NewKeepaliveAct(
			func(_ context.Context, sessionID, text string) error {
				*sentTo = append(*sentTo, sent{sessionID: sessionID, text: text})
				return sendErr
			},
			func(sessionID string) string { return "resume " + sessionID },
			func(sessionID string) string { return "restart " + sessionID },
		), sentTo
	}

	// ⚠️ The two commands are not interchangeable: a restart re-drives a live
	// session, a resume brings back a dead one. Sending the wrong one opens a
	// second session or types into nothing.
	ginkgo.It("types the resume command for a resume verdict", func() {
		run, sentTo := act(nil)

		err := run(ctx, "resume", "s1")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*sentTo).To(gomega.Equal([]sent{{sessionID: "s1", text: "resume s1"}}))
	})

	ginkgo.It("types the restart command for a restart verdict", func() {
		run, sentTo := act(nil)

		err := run(ctx, "restart", "s1")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(*sentTo).To(gomega.Equal([]sent{{sessionID: "s1", text: "restart s1"}}))
	})

	// A word this act does not know is drift, and guessing would type the wrong
	// command into a real session.
	ginkgo.It("refuses an unknown verdict without typing anything", func() {
		run, sentTo := act(nil)

		err := run(ctx, "leave", "s1")

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*sentTo).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses an empty session id without typing anything", func() {
		run, sentTo := act(nil)

		err := run(ctx, "resume", "")

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(*sentTo).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a send failure", func() {
		run, _ := act(errors.New(ctx, "session gone"))

		err := run(ctx, "resume", "s1")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses an act wired without a sender", func() {
		run := NewKeepaliveAct(nil, nil, nil)

		err := run(ctx, "resume", "s1")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
