// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// writeEventFile writes one real events.jsonl fixture and returns its path.
func writeEventFile(dir, name, content string) string {
	path := filepath.Join(dir, name)
	gomega.Expect(os.WriteFile(path, []byte(content), 0o600)).To(gomega.Succeed())
	return path
}

var _ = ginkgo.Describe("parked closer", func() {
	ginkgo.It("reads the last closer from a real event file", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := writeEventFile(dir, sessionEventsName, strings.Join([]string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"an earlier closer"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}`,
			`{"type":"result","message":{}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"⏰ Ends: card 3f9a1c7e answered\n⏰ Next: nothing"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"a partial trailing write"`,
		}, "\n"))

		closer := LastCloser(path)
		gomega.Expect(closer).To(
			gomega.Equal("⏰ Ends: card 3f9a1c7e answered\n⏰ Next: nothing"),
		)
		declaration, parked := ClassifyParked(closer)
		gomega.Expect(parked).To(gomega.BeTrue())
		gomega.Expect(declaration.Class).To(gomega.Equal(ParkedOnCard))

		_, parkedNextOnly := ClassifyParked("⏰ Next: nothing")
		gomega.Expect(parkedNextOnly).To(gomega.BeFalse())

		fmt.Printf("closer=%q parked=%t\n", closer, parked)
	})

	ginkgo.It("reads the last closer past a partial first line in a large event file", func() {
		dir := ginkgo.GinkgoT().TempDir()
		filler := strings.Repeat("x", parkedCloserTailBytes+1024)
		first := fmt.Sprintf(`{"type":"summary","filler":%q}`, filler) + "\n"
		second := `{"type":"assistant","message":{"content":[{"type":"text","text":"⏰ Ends: sun:22 answers"}]}}` + "\n"
		path := writeEventFile(dir, sessionEventsName, first+second)

		info, err := os.Stat(path)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(info.Size()).To(gomega.BeNumerically(">", parkedCloserTailBytes))

		closer := LastCloser(path)
		gomega.Expect(closer).To(gomega.Equal("⏰ Ends: sun:22 answers"))
		gomega.Expect(strings.Contains(closer, "xxxx")).To(gomega.BeFalse())

		fmt.Printf("size=%d closer=%q\n", info.Size(), closer)
	})

	ginkgo.It("classifies a parked declaration into exactly one class", func() {
		type parkedCase struct {
			declaration string
			class       ParkedClass
		}
		cases := []parkedCase{
			{"card 3f9a1c7e answered", ParkedOnCard},
			{"the attention card 9b2f0d41 is answered", ParkedOnCard},
			{"bborbe/claude-wrapper#412 merges", ParkedOnPR},
			{"the bot posts a review on bborbe/nuke#88 or CI goes red", ParkedOnPR},
			{"sun:22 answers", ParkedOnHost},
			{"192.168.178.38:22 accepts a connection", ParkedOnHost},
			{"192.168.178.38 answers", ParkedOnHost},
			// ⚠️ A declaration carrying BOTH a host token and a clock must bound at the
			// clock, because ClassifyParked checks the clock before the host. The class
			// alone distinguishes correct from inverted precedence: ParkedUntil here,
			// ParkedOnHost if the two blocks were swapped.
			{"sun:22 answers by 2026-11-18T14:30", ParkedUntil},
			{"2026-11-18T09:00:00+01:00", ParkedUntil},
			// ⚠️ The date-time shape the live declarations actually write. Class alone
			// cannot distinguish the fixed code from the pre-fix code — both return
			// parked-until — so the Deadline assertion below is the real guard.
			{"2026-11-18T14:30", ParkedUntil},
			{"2026-11-18", ParkedUntil},
			{"task ABC-123 flips to a terminal status on disk", ParkedUntil},
			{"the tag v1.2.3 appears", ParkedUntil},
			{"#412 merges", ParkedUntil},
			{"sun answers", ParkedUntil},
			{"", ParkedUntil},
		}
		for _, testCase := range cases {
			declaration, parked := ClassifyParked(machineWaitEnds + " " + testCase.declaration)
			gomega.Expect(parked).To(gomega.BeTrue())
			gomega.Expect(declaration.Class).To(gomega.Equal(testCase.class))
			fmt.Printf("declaration=%s class=%s\n", testCase.declaration, declaration.Class)
		}

		_, parked := ClassifyParked("⏰ Next: nothing")
		gomega.Expect(parked).To(gomega.BeFalse())
		fmt.Printf("declaration=%s class=none\n", "⏰ Next: nothing")

		// The date-time shape must bound at the clock it names, not fall through to
		// the MAX_AGE_S expiry. A nil Deadline here is the pre-fix behaviour.
		timed, parked := ClassifyParked(machineWaitEnds + " 2026-11-18T14:30")
		gomega.Expect(parked).To(gomega.BeTrue())
		gomega.Expect(timed.Deadline).NotTo(gomega.BeNil())
		gomega.Expect(timed.Deadline.Format("2006-01-02T15:04")).To(gomega.Equal("2026-11-18T14:30"))
		fmt.Printf("declaration=%s deadline=%s\n", "2026-11-18T14:30", timed.Deadline.Format("2006-01-02T15:04"))
	})

	// Ginkgo registers one spec per DescribeTable entry and a table may only be
	// built inside a container, so the spec is a Describe wrapping the table.
	ginkgo.Describe("returns no closer when the file cannot yield one", func() {
		var dir string
		ginkgo.BeforeEach(func() {
			dir = ginkgo.GinkgoT().TempDir()
		})

		ginkgo.DescribeTable("no closer",
			func(name string, build func(dir string) string, skipAsRoot bool) {
				if skipAsRoot && os.Geteuid() == 0 {
					fmt.Printf("case=unreadable skipped=root\n")
					ginkgo.Skip("root can read a 0o000 file")
				}
				closer := LastCloser(build(dir))
				gomega.Expect(closer).To(gomega.Equal(""))
				fmt.Printf("case=%s closer=%q\n", name, closer)
			},
			ginkgo.Entry("missing", "missing", func(dir string) string {
				return filepath.Join(dir, "missing.jsonl")
			}, false),
			ginkgo.Entry("unreadable", "unreadable", func(dir string) string {
				path := writeEventFile(dir, "unreadable.jsonl", "{}\n")
				gomega.Expect(os.Chmod(path, 0o000)).To(gomega.Succeed())
				return path
			}, true),
			ginkgo.Entry("directory", "directory", func(dir string) string {
				return dir
			}, false),
			ginkgo.Entry("truncated first line only", "truncated first line only",
				func(dir string) string {
					return writeEventFile(
						dir,
						"truncated.jsonl",
						strings.Repeat("x", parkedCloserTailBytes+1024)+"\n"+
							`{"type":"assistant","message":{"content":[{"type":"text","text":"trunc`,
					)
				}, false),
			ginkgo.Entry("tool-only last assistant line", "tool-only last assistant line",
				func(dir string) string {
					return writeEventFile(dir, "toolonly.jsonl", strings.Join([]string{
						`{"type":"assistant","message":{"content":[{"type":"text","text":"an earlier closer"}]}}`,
						`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}`,
					}, "\n")+"\n")
				}, false),
			ginkgo.Entry("non-JSON", "non-JSON", func(dir string) string {
				return writeEventFile(dir, "nonjson.jsonl", "not json at all\n")
			}, false),
			ginkgo.Entry("assistant with no text block", "assistant with no text block",
				func(dir string) string {
					return writeEventFile(
						dir,
						"notext.jsonl",
						`{"type":"assistant","message":{"content":[]}}`+"\n",
					)
				}, false),
		)
	})

	ginkgo.It("rejects a declaration it cannot address safely", func() {
		declarations := []string{
			"-owner/repo#1",
			"owner/repo#0",
			"sun:99999",
			"999.1.1.1",
		}
		for _, declaration := range declarations {
			parsed, parked := ClassifyParked(machineWaitEnds + " " + declaration)
			gomega.Expect(parked).To(gomega.BeTrue())
			gomega.Expect(parsed.Class).To(gomega.Equal(ParkedUntil))
			gomega.Expect(parsed.Deadline).To(gomega.BeNil())
			gomega.Expect(parsed.PR.Owner).To(gomega.Equal(""))
			gomega.Expect(parsed.Host).To(gomega.Equal(""))
			fmt.Printf(
				"declaration=%q class=%s pr_owner=%q host=%q\n",
				declaration,
				parsed.Class,
				parsed.PR.Owner,
				parsed.Host,
			)
		}
	})
})
