// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The stand-down rule is ordering, not presence: a row the operator stopped is
// reopened only when the operator flagged it again strictly after the stop. The
// table is shaped so the spec's mutation check — swapping the two `After`
// operands — flips `stopped-after-flag` to open and `flag-after-stop` to skip.
var _ = ginkgo.Describe("skips a stopped row only when the stop is newer than the flag", func() {
	now := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)

	stamp := func(literal string) *time.Time {
		at, err := time.Parse(time.RFC3339, literal)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		return &at
	}

	ginkgo.DescribeTable("each ordering case",
		func(row FlaggedRow, wantOpened bool) {
			reason, refused, skipped := decision(row, nil, nil, nil, now, false, false)
			ginkgo.GinkgoWriter.Printf(
				"case=%s reason=%s opened=%t\n", row.Name, reason, !refused && !skipped,
			)
			gomega.Expect(!refused && !skipped).To(gomega.Equal(wantOpened))
			if !wantOpened {
				gomega.Expect(reason).To(gomega.Equal(reasonStopped))
			}
		},
		ginkgo.Entry("stopped-after-flag", FlaggedRow{
			Kind: "task", Name: "stopped-after-flag", Liveness: "ABSENT",
			Stopped:   true,
			StopDate:  stamp("2026-11-24T18:05:33+01:00"),
			FlagSetAt: stamp("2026-11-24T17:10:00+01:00"),
		}, false),
		ginkgo.Entry("flag-after-stop", FlaggedRow{
			Kind: "task", Name: "flag-after-stop", Liveness: "ABSENT",
			Stopped:   true,
			StopDate:  stamp("2026-11-24T18:05:33+01:00"),
			FlagSetAt: stamp("2026-11-24T19:36:58+01:00"),
		}, true),
		ginkgo.Entry("no-stop-date", FlaggedRow{
			Kind: "task", Name: "no-stop-date", Liveness: "ABSENT",
			Stopped:   true,
			FlagSetAt: stamp("2026-11-24T19:36:58+01:00"),
		}, false),
		ginkgo.Entry("unparseable-flag-instant", FlaggedRow{
			Kind: "task", Name: "unparseable-flag-instant", Liveness: "ABSENT",
			Stopped:  true,
			StopDate: stamp("2026-11-24T18:05:33+01:00"),
		}, false),
		ginkgo.Entry("unparseable-stop-date", FlaggedRow{
			Kind: "task", Name: "unparseable-stop-date", Liveness: "ABSENT",
			Stopped:   true,
			FlagSetAt: stamp("2026-11-24T19:36:58+01:00"),
		}, false),
		ginkgo.Entry("not-stopped", FlaggedRow{
			Kind: "task", Name: "not-stopped", Liveness: "ABSENT",
			FlagSetAt: stamp("2026-11-24T19:36:58+01:00"),
		}, true),
		ginkgo.Entry("stopped-false", FlaggedRow{
			Kind: "task", Name: "stopped-false", Liveness: "ABSENT",
			Stopped:   false,
			FlagSetAt: stamp("2026-11-24T19:36:58+01:00"),
		}, true),
	)
})
