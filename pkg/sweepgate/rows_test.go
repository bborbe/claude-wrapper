// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ClassifiedRows", func() {
	ginkgo.It("classifies one row per task, carrying the bucket key and label", func() {
		prepared := Prepared{
			Holds: map[string]Hold{},
			Tasks: []Task{
				{Name: "A", Session: "s-a", Status: "completed"},
				{Name: "B", Session: "s-b", Status: "in_progress", InFlight: true},
			},
		}

		rows := rowsFrom(fixedNow, prepared)

		gomega.Expect(rows).To(gomega.HaveLen(2))
		gomega.Expect(rows[0].Name).To(gomega.Equal("A"))
		gomega.Expect(rows[0].Bucket).To(gomega.Equal("done"))
		gomega.Expect(rows[1].Bucket).To(gomega.Equal("progressing"))
		gomega.Expect(rows[1].Label).To(gomega.ContainSubstring("progressing"))
	})

	// THE INVARIANT: rows must carry the overlays, or a consumer reading them
	// disagrees with the frame an operator reads. Classify alone would return
	// the underlying bucket here; Bucket returns the hold label. This spec fails
	// if rowsFrom is ever switched to Classify.
	ginkgo.It("carries the operator-hold overlay, not the pre-overlay bucket label", func() {
		prepared := Prepared{
			Holds: map[string]Hold{"s-a": {HeldAt: "2026-11-18T11:00:00Z", Reason: "operator parked it"}},
			Tasks: []Task{
				{Name: "A", Session: "s-a", Status: "in_progress", InFlight: true},
			},
		}

		rows := rowsFrom(fixedNow, prepared)

		gomega.Expect(rows).To(gomega.HaveLen(1))
		// The key is unchanged by the hold overlay — the label is what moves.
		gomega.Expect(rows[0].Bucket).To(gomega.Equal("progressing"))
		gomega.Expect(rows[0].Label).NotTo(gomega.ContainSubstring("progressing"))
		gomega.Expect(rows[0].Label).To(gomega.ContainSubstring("HELD"))
	})

	ginkgo.It("returns an empty slice for an empty tracked set, never nil", func() {
		rows := rowsFrom(fixedNow, Prepared{Holds: map[string]Hold{}})

		gomega.Expect(rows).NotTo(gomega.BeNil())
		gomega.Expect(rows).To(gomega.BeEmpty())
	})

	ginkgo.It("carries the task's own status and phase for traceability", func() {
		prepared := Prepared{
			Holds: map[string]Hold{},
			Tasks: []Task{{Name: "A", Status: "in_progress", Phase: "todo"}},
		}

		rows := rowsFrom(fixedNow, prepared)

		gomega.Expect(rows[0].Status).To(gomega.Equal("in_progress"))
		gomega.Expect(rows[0].Phase).To(gomega.Equal("todo"))
	})
})
