// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("StalledTabEffect", func() {
	// ⚠️ `restart` and `carded` are BOTH `kept`: `restart` means the OPERATOR
	// answered and the real restart already ran, so claiming `restarted` would
	// attribute an act this leg never performed.
	ginkgo.It("maps every verdict onto the tick's closed effect set", func() {
		cases := []struct {
			verdict string
			token   string
			result  KeepaliveResult
			reason  string
		}{
			{"restart", "", KeepaliveResultKept, "handed-over"},
			{"carded", "", KeepaliveResultKept, "carded"},
			{"capped", "", KeepaliveResultCapped, ""},
			{"held", "", KeepaliveResultSkipped, "hold"},
			{"skipped", "busy-target", KeepaliveResultSkipped, "busy-target"},
			{"cannot-tell", "", KeepaliveResultSkipped, "cannot-tell"},
		}
		for _, c := range cases {
			effect := StalledTabEffect(StalledTab{SessionID: "s1", Verdict: c.verdict, Token: c.token})

			gomega.Expect(effect.Result).To(gomega.Equal(c.result), c.verdict)
			gomega.Expect(effect.Reason).To(gomega.Equal(c.reason), c.verdict)
			gomega.Expect(effect.SessionID).To(gomega.Equal("s1"))
		}
	})

	// A word this tick does not know is procedure/script drift, never a quiet fleet.
	ginkgo.It("treats an unknown verdict as undecidable, not as kept", func() {
		effect := StalledTabEffect(StalledTab{SessionID: "s1", Verdict: "invented"})

		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultSkipped))
		gomega.Expect(effect.Reason).To(gomega.Equal("cannot-tell"))
	})

	ginkgo.It("falls back to a bare reason when a skipped row carries no token", func() {
		effect := StalledTabEffect(StalledTab{Verdict: "skipped"})

		gomega.Expect(effect.Reason).To(gomega.Equal("skipped"))
	})
})

var _ = ginkgo.Describe("StalledTabsProcedure", func() {
	ctx := context.Background()

	ginkgo.It("names itself stalled-tabs", func() {
		gomega.Expect((&StalledTabsProcedure{}).Name()).To(gomega.Equal("stalled-tabs"))
	})

	ginkgo.It("reports none when no row is stalled", func() {
		procedure := &StalledTabsProcedure{Rows: func(context.Context) ([]StalledTab, error) { return nil, nil }}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultNone))
	})

	ginkgo.It("reports the first row, so the line names a session", func() {
		procedure := &StalledTabsProcedure{Rows: func(context.Context) ([]StalledTab, error) {
			return []StalledTab{
				{SessionID: "aaaaaaaa", Verdict: "held"},
				{SessionID: "bbbbbbbb", Verdict: "capped"},
			}, nil
		}}

		effect, err := procedure.Run(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(effect.SessionID).To(gomega.Equal("aaaaaaaa"))
		gomega.Expect(effect.Result).To(gomega.Equal(KeepaliveResultSkipped))
	})

	ginkgo.It("refuses a procedure wired without a row source", func() {
		_, err := (&StalledTabsProcedure{}).Run(ctx)

		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})

var _ = ginkgo.Describe("ParseStalledTabRows", func() {
	ctx := context.Background()

	ginkgo.It("reads one row per verdict line and ignores the summary", func() {
		output := "deadbeef\tcarded\ncafebabe\tskipped\tbusy-target\n" +
			"stalled-tabs: 2 seen · 0 restarted · 1 carded · 0 capped · 0 held · 1 skipped · 0 cannot-tell\n"

		rows, err := ParseStalledTabRows(ctx, output)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(rows).To(gomega.Equal([]StalledTab{
			{SessionID: "deadbeef", Verdict: "carded"},
			{SessionID: "cafebabe", Verdict: "skipped", Token: "busy-target"},
		}))
	})

	ginkgo.It("ignores a line whose second field is not a verdict", func() {
		rows, _ := ParseStalledTabRows(ctx, "deadbeef\tnot-a-verdict\n")

		gomega.Expect(rows).To(gomega.BeEmpty())
	})
})
