// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Refusals", func() {
	ginkgo.It("admits a flag the operator set", func() {
		for _, actor := range []string{"operator", "legacy"} {
			block := "flag: true\nflag_set_by: " + actor + "\n"
			gomega.Expect(Refusals("task", block)).To(gomega.BeEmpty(), "actor=%s", actor)
			gomega.Expect(FlagAdmits(block, "task")).To(gomega.BeTrue(), "actor=%s", actor)
		}
	})

	ginkgo.It("refuses a flag an agent set", func() {
		// The whole point of the rule: an agent-set flag opens nothing at any tier.
		block := "flag: true\nflag_set_by: agent\n"
		gomega.Expect(Refusals("task", block)).To(gomega.Equal([]string{RefusalAgentSetFlag}))
		gomega.Expect(FlagAdmits(block, "task")).To(gomega.BeFalse())
	})

	ginkgo.It("refuses a flag with NO provenance as UNRECORDED, not as agent-set", func() {
		// Absence admits nothing — `legacy` exists precisely so a missing field could
		// mean refuse without breaking the rows already dispositioned. But absence is
		// not evidence an AGENT set it, so the reason must say "unrecorded" and name
		// the command that records the actor.
		block := "flag: true\n"
		refusals := Refusals("task", block)
		gomega.Expect(refusals).To(gomega.HaveLen(1))
		gomega.Expect(refusals[0]).To(gomega.ContainSubstring(RefusalProvenanceUnrecorded))
		gomega.Expect(refusals[0]).To(gomega.ContainSubstring("--by operator"))
	})

	ginkgo.It("refuses `unknown` and `none` as unrecorded, never as agent-set", func() {
		// `unknown` is what `vault-cli task set … flag true` stamps when the write
		// declares no actor. Folding it into agent-set refused a legitimate operator
		// task forever, with no visible remedy — the defect this split fixes.
		for _, actor := range []string{"unknown", "none"} {
			block := "flag: true\nflag_set_by: " + actor + "\n"
			refusals := Refusals("task", block)
			gomega.Expect(refusals).To(gomega.HaveLen(1), "actor=%s", actor)
			gomega.Expect(refusals[0]).To(gomega.ContainSubstring(RefusalProvenanceUnrecorded), "actor=%s", actor)
			gomega.Expect(refusals[0]).NotTo(gomega.ContainSubstring(RefusalAgentSetFlag), "actor=%s", actor)
		}
	})

	ginkgo.It("admits the same row once the actor is recorded as operator", func() {
		// SC3's second half: the remedy is a real state transition, not a string.
		// A row refused as unrecorded is admitted when its `flag_set_by` becomes
		// `operator` — the write the refusal names.
		unrecorded := "flag: true\nflag_set_by: unknown\n"
		gomega.Expect(Refusals("task", unrecorded)).NotTo(gomega.BeEmpty())
		recorded := "flag: true\nflag_set_by: operator\n"
		gomega.Expect(Refusals("task", recorded)).To(gomega.BeEmpty())
	})

	ginkgo.It("names the three provenance classes", func() {
		for actor, want := range map[string]string{
			"operator": ProvenanceOperator,
			"legacy":   ProvenanceOperator,
			"agent":    ProvenanceAgent,
			"unknown":  ProvenanceUnrecorded,
			"none":     ProvenanceUnrecorded,
			"":         ProvenanceUnrecorded,
		} {
			gomega.Expect(ProvenanceClass(actor)).To(gomega.Equal(want), "actor=%q", actor)
		}
	})

	ginkgo.It("says nothing about an unflagged row", func() {
		// "Not flagged" is the eligibility gate's reason, not a provenance refusal.
		// Emitting it here would put a spurious token on every unflagged row.
		for _, block := range []string{"", "flag_set_by: operator\n", "flag: false\nflag_set_by: operator\n"} {
			gomega.Expect(Refusals("task", block)).To(gomega.BeEmpty(), "block=%q", block)
		}
	})

	ginkgo.It("reads a task flag as a YAML boolean, so a quoted scalar is not set", func() {
		// `commands/open.md` reads the flag off a parsed page; `flag: "true"` is a
		// string there, so admitting it here would let this predicate and the consumer
		// disagree about the same page.
		block := "flag: \"true\"\nflag_set_by: operator\n"
		gomega.Expect(FlagSet(block, "task")).To(gomega.BeFalse())
		gomega.Expect(Refusals("task", block)).To(gomega.BeEmpty())
	})

	ginkgo.It("accepts the full YAML 1.1 truth set for a task flag", func() {
		for _, value := range []string{"true", "True", "TRUE", "yes", "Yes", "YES", "on", "On", "ON", "y", "Y"} {
			block := "flag: " + value + "\nflag_set_by: operator\n"
			gomega.Expect(FlagAdmits(block, "task")).To(gomega.BeTrue(), "flag=%s", value)
		}
	})

	ginkgo.It("reads a goal or topic flag tolerantly, including `1`", func() {
		// Those pages are hand-edited by several writers and historically accepted `1`;
		// a task page carrying `1` is NOT flagged, because its reader parses YAML.
		goal := "flag: 1\nflag_set_by: operator\n"
		gomega.Expect(FlagAdmits(goal, "goal")).To(gomega.BeTrue())
		gomega.Expect(FlagAdmits(goal, "topic")).To(gomega.BeTrue())
		gomega.Expect(FlagAdmits(goal, "task")).To(gomega.BeFalse())
	})

	ginkgo.It("refuses an agent-set flag on a goal exactly as on a task", func() {
		// The 2026-11-10 ruling: the reason is the same for all three subjects. Before
		// it, this returned nothing for a goal and a manager opened.
		for _, kind := range []string{"task", "goal", "topic"} {
			block := "flag: true\nflag_set_by: agent\n"
			gomega.Expect(Refusals(kind, block)).To(gomega.Equal([]string{RefusalAgentSetFlag}), "kind=%s", kind)
		}
	})

	ginkgo.It("ignores a YAML inline comment on either field", func() {
		// `flag: true  # note` loads as a real `true`; reading the comment as part of the
		// value would refuse a row the consumer counts as approved.
		block := "flag: true  # set by hand\nflag_set_by: operator  # me\n"
		gomega.Expect(FlagAdmits(block, "task")).To(gomega.BeTrue())
		gomega.Expect(Refusals("task", block)).To(gomega.BeEmpty())
	})

	ginkgo.It("admits a quoted provenance value", func() {
		block := "flag: true\nflag_set_by: \"operator\"\n"
		gomega.Expect(FlagAdmits(block, "task")).To(gomega.BeTrue())
	})
})
