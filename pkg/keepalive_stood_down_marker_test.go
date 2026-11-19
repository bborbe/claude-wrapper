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

var _ = ginkgo.Describe("NewStoodDownMarker", func() {
	ctx := context.Background()

	candidates := func(items ...StoodDownCandidate) func(context.Context) ([]StoodDownCandidate, error) {
		return func(context.Context) ([]StoodDownCandidate, error) { return items, nil }
	}
	sessionFor := func(sessions map[string]string) func(context.Context, string) (string, error) {
		return func(_ context.Context, base string) (string, error) { return sessions[base], nil }
	}
	live := func(live map[string]bool) func(context.Context, string) (bool, error) {
		return func(_ context.Context, sessionID string) (bool, error) { return live[sessionID], nil }
	}

	ginkgo.It("names the session the join kept", func() {
		marker := NewStoodDownMarker(
			candidates(
				StoodDownCandidate{Slug: "goal--gone", Branch: "goal", Base: "gone"},
				StoodDownCandidate{Slug: "goal--alive", Branch: "goal", Base: "alive"},
			),
			sessionFor(map[string]string{"gone": "s-gone", "alive": "s-alive"}),
			live(map[string]bool{"s-gone": false, "s-alive": true}),
		)

		sessionID, stoodDown, err := marker(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(stoodDown).To(gomega.BeTrue())
		gomega.Expect(sessionID).To(gomega.Equal("s-alive"))
	})

	ginkgo.It("reports nothing stood down when no candidate survives the join", func() {
		marker := NewStoodDownMarker(
			candidates(StoodDownCandidate{Slug: "goal--gone", Branch: "goal", Base: "gone"}),
			sessionFor(map[string]string{"gone": "s-gone"}),
			live(map[string]bool{"s-gone": false}),
		)

		_, stoodDown, err := marker(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(stoodDown).To(gomega.BeFalse())
	})

	// ⚠️ One per tick, like every other procedure: a backlog must not fire at once.
	ginkgo.It("returns at most one session even when the join kept several", func() {
		marker := NewStoodDownMarker(
			candidates(
				StoodDownCandidate{Slug: "goal--a", Branch: "goal", Base: "a"},
				StoodDownCandidate{Slug: "goal--b", Branch: "goal", Base: "b"},
			),
			sessionFor(map[string]string{"a": "s-a", "b": "s-b"}),
			live(map[string]bool{"s-a": true, "s-b": true}),
		)

		sessionID, stoodDown, err := marker(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(stoodDown).To(gomega.BeTrue())
		gomega.Expect(sessionID).To(gomega.Equal("s-a"))
	})

	ginkgo.It("surfaces a candidate read failure rather than reporting nothing stood down", func() {
		marker := NewStoodDownMarker(
			func(context.Context) ([]StoodDownCandidate, error) {
				return nil, errors.New(ctx, "script failed")
			},
			sessionFor(nil),
			live(nil),
		)

		_, _, err := marker(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses a marker wired without a collaborator", func() {
		_, _, err := NewStoodDownMarker(nil, nil, nil)(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
