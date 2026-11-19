// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("open surface defaults", func() {
	// AC21: each flag default is the deployed marketplace clone, matching
	// --flagged-rows-script.
	ginkgo.It("defaults every script path to the marketplace clone", func() {
		gomega.Expect(DefaultAnswerPathCheckScript).To(gomega.Equal(
			"~/.claude/plugins/marketplaces/claude-supervisor/scripts/answer-path-check.py",
		))
		gomega.Expect(DefaultManagerLauncherScript).To(gomega.Equal(
			"~/.claude/plugins/marketplaces/claude-supervisor/scripts/manager-launcher.py",
		))
		gomega.Expect(DefaultTopicSpawnScript).To(gomega.Equal(
			"~/.claude/plugins/marketplaces/claude-supervisor/scripts/topic-spawn.py",
		))
	})

	ginkgo.It("caps the body at the MCP faces' 1 MiB", func() {
		gomega.Expect(MaxOpenBody).To(gomega.Equal(1 << 20))
	})
})

var _ = ginkgo.Describe("ValidateOpenRows", func() {
	var ctx context.Context
	vaults := []Vault{{Name: "v1", Path: "/tmp/v1"}}

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.DescribeTable("refuses a malformed request",
		func(rows []OpenRow, expected string) {
			err := ValidateOpenRows(ctx, rows, vaults)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(stderrors.Is(err, ErrInvalidOpenRequest)).To(gomega.BeTrue())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring(expected))
		},
		ginkgo.Entry("empty rows", nil, "rows is empty"),
		ginkgo.Entry("empty rows slice", []OpenRow{}, "rows is empty"),
		ginkgo.Entry("unknown kind",
			[]OpenRow{{Kind: "widget", Vault: "v1", Name: "A"}},
			`row 0: unknown kind "widget"`),
		ginkgo.Entry("empty name",
			[]OpenRow{{Kind: OpenKindTask, Vault: "v1", Name: "  "}},
			"row 0: name is empty"),
		ginkgo.Entry("empty vault",
			[]OpenRow{{Kind: OpenKindTask, Vault: "", Name: "A"}},
			"row 0: vault is empty"),
		ginkgo.Entry("name with a slash",
			[]OpenRow{{Kind: OpenKindTask, Vault: "v1", Name: "a/b"}},
			"row 0: name"),
		ginkgo.Entry("name with a backslash",
			[]OpenRow{{Kind: OpenKindTask, Vault: "v1", Name: `a\b`}},
			"row 0: name"),
		ginkgo.Entry("name with dot-dot",
			[]OpenRow{{Kind: OpenKindTask, Vault: "v1", Name: "a..b"}},
			"row 0: name"),
		ginkgo.Entry("name with a control character",
			[]OpenRow{{Kind: OpenKindTask, Vault: "v1", Name: "a\ab"}},
			"row 0: name"),
		ginkgo.Entry("unconfigured vault",
			[]OpenRow{{Kind: OpenKindTask, Vault: "nope", Name: "A"}},
			`row 0: vault "nope" is not configured`),
		ginkgo.Entry("second row names its index",
			[]OpenRow{
				{Kind: OpenKindTask, Vault: "v1", Name: "A"},
				{Kind: OpenKindTask, Vault: "v1", Name: "B/C"},
			},
			"row 1: name"),
	)

	ginkgo.It("accepts every kind and ignores first_step", func() {
		err := ValidateOpenRows(ctx, []OpenRow{
			{Kind: OpenKindTask, Vault: "v1", Name: "A", FirstStep: "look first"},
			{Kind: OpenKindGoal, Vault: "v1", Name: "G"},
			{Kind: OpenKindTopic, Vault: "v1", Name: "T"},
		}, vaults)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
})
