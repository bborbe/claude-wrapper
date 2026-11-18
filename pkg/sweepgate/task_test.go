// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func fmOf(text string) string {
	fm, ok := SplitFrontmatter(text)
	gomega.Expect(ok).To(gomega.BeTrue())
	return fm
}

var _ = ginkgo.Describe("parses every frontmatter shape", func() {
	ginkgo.It("reads the block-list goals shape", func() {
		fm := fmOf("---\ngoals:\n  - '[[Goal One]]'\n  - '[[Goal Two]]'\n---\n")
		gomega.Expect(FMWikilinks(fm, "goals")).
			To(gomega.Equal([]string{"Goal One", "Goal Two"}))
	})

	ginkgo.It("reads the inline flow-list goals shape", func() {
		fm := fmOf("---\ngoals: ['[[Goal One]]', '[[Goal Two]]']\n---\n")
		gomega.Expect(FMWikilinks(fm, "goals")).
			To(gomega.Equal([]string{"Goal One", "Goal Two"}))
	})

	ginkgo.It("reads the quoted-wikilink scalar goals shape", func() {
		fm := fmOf("---\ngoals: '[[Goal One]]'\n---\n")
		gomega.Expect(FMWikilinks(fm, "goals")).To(gomega.Equal([]string{"Goal One"}))
	})

	ginkgo.It("reads the bare-name scalar goals shape", func() {
		fm := fmOf("---\ngoals: Goal One\n---\n")
		gomega.Expect(FMWikilinks(fm, "goals")).To(gomega.Equal([]string{"Goal One"}))
	})

	ginkgo.It("reads a bare block item without brackets", func() {
		fm := fmOf("---\ngoals:\n  - Goal One\n---\n")
		gomega.Expect(FMWikilinks(fm, "goals")).To(gomega.Equal([]string{"Goal One"}))
	})

	ginkgo.It("keeps goals: [] empty", func() {
		fm := fmOf("---\ngoals: []\n---\n")
		gomega.Expect(FMWikilinks(fm, "goals")).To(gomega.BeEmpty())
	})

	ginkgo.It("reads a null claude_session_id as no id", func() {
		fm := fmOf("---\nclaude_session_id: null\n---\n")
		gomega.Expect(FMSessionID(fm, "claude_session_id")).To(gomega.BeEmpty())
	})

	ginkgo.It("reads metrics_sessions ids", func() {
		fm := fmOf("---\nmetrics_sessions:\n  - session_id: abc123\n" +
			"    started_at: 2026-01-01T00:00:00Z\n  - session_id: def456\n---\n")
		gomega.Expect(FMMetricsSessionIDs(fm, "metrics_sessions")).
			To(gomega.Equal([]string{"abc123", "def456"}))
	})

	ginkgo.It("puts the current session first in the id set and de-duplicates", func() {
		fm := fmOf("---\nclaude_session_id: own1\nmetrics_sessions:\n" +
			"  - session_id: own1\n  - session_id: extra2\n---\n")
		gomega.Expect(SessionIDSet(fm)).To(gomega.Equal([]string{"own1", "extra2"}))
	})

	ginkgo.It("reads blocked_by in both shapes", func() {
		fm := fmOf("---\nblocked_by:\n  - '[[Blocked One]]'\n  - Blocked Two\n---\n")
		gomega.Expect(FMList(fm, "blocked_by")).
			To(gomega.Equal([]string{"Blocked One", "Blocked Two"}))

		scalar := fmOf("---\nblocked_by: Blocked One, Blocked Two\n---\n")
		gomega.Expect(FMList(scalar, "blocked_by")).
			To(gomega.Equal([]string{"Blocked One", "Blocked Two"}))
	})

	ginkgo.It("reads the operator flag only for an operator or legacy setter", func() {
		task, ok := ParseTask("T", "---\nflag: true\nflag_set_by: operator\n---\n", time.Time{})
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(task.OperatorFlagged).To(gomega.BeTrue())
		agentTask, ok := ParseTask("T", "---\nflag: true\nflag_set_by: agent\n---\n", time.Time{})
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(agentTask.OperatorFlagged).To(gomega.BeFalse())
	})

	ginkgo.It("returns false when there is no frontmatter", func() {
		_, ok := ParseTask("T", "# No frontmatter\n", time.Time{})
		gomega.Expect(ok).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("hashes progress and counts checkboxes", func() {
	ginkgo.It("hashes the empty string when there is no Progress section", func() {
		sum := sha256.Sum256([]byte(""))
		gomega.Expect(ProgressHash("# Task\n")).To(gomega.Equal(hex.EncodeToString(sum[:])[:16]))
	})

	ginkgo.It("moves when a Progress entry is added", func() {
		before := ProgressHash("# Progress\n\n- 2026-01-01: start\n")
		after := ProgressHash("# Progress\n\n- 2026-01-01: start\n- 2026-01-02: more\n")
		gomega.Expect(before).NotTo(gomega.Equal(after))
	})

	ginkgo.It("ends the Progress section at the next H1", func() {
		a := ProgressHash("# Progress\n\n- one\n\n# Other\nignored\n")
		b := ProgressHash("# Progress\n\n- one\n")
		gomega.Expect(a).To(gomega.Equal(b))
	})

	ginkgo.It("excludes a struck row with trailing prose", func() {
		text := "# Success Criteria\n\n- [x] met\n- [ ] ~~[[Removed]]~~ — reason\n- [ ] open\n"
		gomega.Expect(CheckboxCount(text, "Success Criteria")).To(gomega.Equal("1/2"))
	})

	ginkgo.It("counts a ticked struck row", func() {
		text := "# Success Criteria\n\n- [x] ~~[[Done]]~~ — reason\n- [ ] open\n"
		gomega.Expect(CheckboxCount(text, "Success Criteria")).To(gomega.Equal("1/2"))
	})

	ginkgo.It("returns — for an absent section", func() {
		gomega.Expect(CheckboxCount("# Task\n", "Success Criteria")).To(gomega.Equal("—"))
	})
})
