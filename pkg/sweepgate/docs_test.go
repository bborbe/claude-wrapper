// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// docsPath is the tracked page the refusal table lives on. The test's working
// directory is the package directory.
const docsPath = "../../docs/sweep-gate.md"

// countRe extracts a labelled count from the document.
func countRe(label string) *regexp.Regexp {
	return regexp.MustCompile(label + `: (\d+)`)
}

// mustCount parses one labelled count and fails when it is absent.
func mustCount(text, label string) int {
	match := countRe(label).FindStringSubmatch(text)
	gomega.ExpectWithOffset(1, match).NotTo(gomega.BeNil(),
		"docs/sweep-gate.md has no %q line", label)
	value, err := strconv.Atoi(match[1])
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	return value
}

// refusalsSection returns the lines of the `## Refusals` section, header included.
func refusalsSection(lines []string) []string {
	start := -1
	for index, line := range lines {
		if strings.HasPrefix(line, "## Refusals") {
			start = index
			break
		}
	}
	gomega.ExpectWithOffset(1, start).To(gomega.BeNumerically(">=", 0),
		"docs/sweep-gate.md has no ## Refusals section")
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if strings.HasPrefix(lines[index], "## ") {
			end = index
			break
		}
	}
	return lines[start:end]
}

// tableRow is one refusal row's guard and test name.
type tableRow struct {
	Guard string
	Test  string
}

// parseRows collects every data row (a line starting with `| `) in the section.
func parseRows(section []string) []tableRow {
	rows := make([]tableRow, 0)
	for _, line := range section {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(line, "|")
		gomega.ExpectWithOffset(1, len(cells)).To(gomega.BeNumerically(">=", 5),
			"refusal row has too few columns: %q", line)
		rows = append(rows, tableRow{
			Guard: strings.TrimSpace(cells[1]),
			Test:  strings.TrimSpace(cells[3]),
		})
	}
	return rows
}

// testExists reports whether any package test file carries `It("<name>"`.
func testExists(name string) bool {
	files, err := filepath.Glob("*_test.go")
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	needle := `It("` + name + `"`
	for _, file := range files {
		raw, readErr := os.ReadFile(file)
		gomega.ExpectWithOffset(1, readErr).NotTo(gomega.HaveOccurred())
		if strings.Contains(string(raw), needle) {
			return true
		}
	}
	return false
}

var _ = ginkgo.Describe("carries every refusal", func() {
	ginkgo.It("binds every listed refusal to the test carrying it", func() {
		raw, err := os.ReadFile(docsPath)
		if err != nil {
			ginkgo.Fail("docs/sweep-gate.md is missing or unreadable: " + err.Error())
		}
		document := string(raw)
		lines := strings.Split(document, "\n")
		rows := parseRows(refusalsSection(lines))

		unfiltered := mustCount(document, "unfiltered hits")
		excluded := mustCount(document, "excluded")
		rawHits := mustCount(document, "raw hits")

		gomega.Expect(rows).To(gomega.HaveLen(rawHits),
			"the refusal table has %d data rows but the derivation records raw hits: %d",
			len(rows), rawHits)
		gomega.Expect(unfiltered-excluded).To(gomega.Equal(rawHits),
			"unfiltered hits (%d) - excluded (%d) must equal raw hits (%d)",
			unfiltered, excluded, rawHits)

		for _, row := range rows {
			gomega.Expect(row.Test).NotTo(gomega.BeEmpty(),
				"refusal %q has no test name", row.Guard)
			gomega.Expect(testExists(row.Test)).To(gomega.BeTrue(),
				"refusal %q cites %q, which no package test carries", row.Guard, row.Test)
			ginkgo.GinkgoWriter.Printf("refusal=%s test=%s\n", row.Guard, row.Test)
		}
	})
})
