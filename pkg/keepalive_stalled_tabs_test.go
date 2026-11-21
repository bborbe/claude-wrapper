// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"

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

// ⚠️ The two safety properties of this leg, asserted against its own wiring source.
// `--allow-busy-lapse` is the operator's own judgement (ruled 2026-11-12) and the
// tick must never carry it; `--dry-run` is mandatory because without it the script
// acts on any row reading `idle` at call time.
var _ = ginkgo.Describe("the stalled-tabs wiring", func() {
	ginkgo.It("never passes --allow-busy-lapse and always passes --dry-run", func() {
		source, err := os.ReadFile(filepath.Join("..", "main_keepalive.go"))
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		// ⚠️ Comments are stripped first: this file's own comments NAME the
		// forbidden flag while explaining why it must never be passed, so a raw
		// scan fails on the documentation rather than on a call.
		text := codeOnly(string(source))

		gomega.Expect(strings.Contains(text, "--allow-busy-lapse")).To(
			gomega.BeFalse(),
			"the tick must never pass --allow-busy-lapse: that flag is the operator's own judgement",
		)
		gomega.Expect(strings.Contains(text, `"--dry-run"`)).To(
			gomega.BeTrue(),
			"the tick must always pass --dry-run, or the unattended restart becomes reachable",
		)
	})
})

// codeOnly drops whole-line and trailing `//` comments so a source scan reads the
// code rather than the prose that describes it.
func codeOnly(source string) string {
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
