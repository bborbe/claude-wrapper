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

// stubRegistry is the one method the verdict derivation reads.
//
// ⚠️ It EMBEDS SessionRegistry rather than hand-implementing every method. The
// interface carries a half-dozen reads this derivation never calls, and writing
// each one out is noise that has to be maintained every time the interface grows
// — the first attempt at this stub failed three times on methods nobody calls.
// An embedded nil interface satisfies the type at compile time; only the method
// overridden below is ever reached.
type stubRegistry struct {
	SessionRegistry
	entries []RegistryEntry
	err     error
}

func (s stubRegistry) Live(context.Context) ([]RegistryEntry, error) {
	return s.entries, s.err
}

var _ = ginkgo.Describe("NewKeepaliveVerdicts", func() {
	ctx := context.Background()

	source := func(registry SessionRegistry, children []Session, heartbeats []HeartbeatRow) func(context.Context) ([]LivenessVerdict, error) {
		return NewKeepaliveVerdicts(
			registry,
			func(context.Context) ([]Session, error) { return children, nil },
			func(context.Context) ([]HeartbeatRow, error) { return heartbeats, nil },
		)
	}

	ginkgo.It("derives one verdict per session from the three sources", func() {
		registry := stubRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 42, Name: "worker-a"}}}
		exitCode := 0

		verdicts, err := source(registry,
			[]Session{{ID: "s2", ExitCode: &exitCode}},
			nil,
		)(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(verdicts).To(gomega.HaveLen(2))
		byID := map[string]LivenessVerdict{}
		for _, verdict := range verdicts {
			byID[verdict.SessionID] = verdict
		}
		gomega.Expect(byID["s1"].Liveness).To(gomega.Equal(LivenessLive))
		gomega.Expect(byID["s1"].Source).To(gomega.Equal("registry"))
		gomega.Expect(byID["s2"].Liveness).To(gomega.Equal(LivenessNotLive))
		gomega.Expect(byID["s2"].Source).To(gomega.Equal("child-table"))
	})

	// ⚠️ A registry read failure must surface, not return an empty set: "nothing
	// is live" is the direction that hides every running session.
	ginkgo.It("surfaces a registry read failure rather than reporting nothing live", func() {
		registry := stubRegistry{err: errors.New(ctx, "registry unreadable")}

		_, err := source(registry, nil, nil)(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("surfaces a child table read failure", func() {
		source := NewKeepaliveVerdicts(
			stubRegistry{},
			func(context.Context) ([]Session, error) { return nil, errors.New(ctx, "child table gone") },
			func(context.Context) ([]HeartbeatRow, error) { return nil, nil },
		)

		_, err := source(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses a source wired without a registry", func() {
		source := NewKeepaliveVerdicts(
			nil,
			func(context.Context) ([]Session, error) { return nil, nil },
			func(context.Context) ([]HeartbeatRow, error) { return nil, nil },
		)

		_, err := source(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
