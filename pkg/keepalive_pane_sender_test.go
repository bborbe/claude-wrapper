// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("NewKeepalivePaneSender", func() {
	ctx := context.Background()

	// A ready composer before the send, and the echoed message + a fresh prompt
	// after — the two screens SendWhenReady's readiness and confirmation polls read.
	readyTabs := func() *fakeTabs {
		return &fakeTabs{before: "❯ \n", after: "/supervisor:restart s1\n❯ \n"}
	}

	ginkgo.It("sends the restart command into the resolved pane", func() {
		tabs := readyTabs()

		err := NewKeepalivePaneSender(
			tabs,
			func(string) (string, bool) { return "pane-7", true },
			libtime.NewCurrentDateTime(),
		)(ctx, "s1", "/supervisor:restart s1")

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(tabs.sent).To(gomega.Equal(map[string]int{"pane-7": 1}))
	})

	// ⚠️ No pane → the sentinel the procedure maps to `skipped reason=no-pane`, not a
	// send that typed into nothing.
	ginkgo.It("returns ErrKeepaliveNoPane when the session has no pane, without sending", func() {
		tabs := readyTabs()

		err := NewKeepalivePaneSender(
			tabs,
			func(string) (string, bool) { return "", false },
			libtime.NewCurrentDateTime(),
		)(ctx, "s1", "/supervisor:restart s1")

		gomega.Expect(err).To(gomega.Equal(ErrKeepaliveNoPane))
		gomega.Expect(tabs.sent).To(gomega.BeEmpty())
	})

	ginkgo.It("surfaces a send failure", func() {
		tabs := readyTabs()
		tabs.sendErr = errors.New(ctx, "pane gone")

		err := NewKeepalivePaneSender(
			tabs,
			func(string) (string, bool) { return "pane-7", true },
			libtime.NewCurrentDateTime(),
		)(ctx, "s1", "/supervisor:restart s1")

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.DescribeTable("refuses a sender wired without a collaborator",
		func(tabs TabClient,
			paneFor func(string) (string, bool),
			now libtime.CurrentDateTimeGetter) {
			err := NewKeepalivePaneSender(tabs, paneFor, now)(ctx, "s1", "cmd")
			gomega.Expect(err).To(gomega.HaveOccurred())
		},
		ginkgo.Entry("no tab client", nil,
			func(string) (string, bool) { return "p", true }, libtime.NewCurrentDateTime()),
		ginkgo.Entry("no pane resolver", readyTabs(), nil, libtime.NewCurrentDateTime()),
		ginkgo.Entry("no clock", readyTabs(),
			func(string) (string, bool) { return "p", true }, nil),
	)
})
