// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// captureHeaderPattern is the provenance line every fixture must carry:
// `# captured <ISO8601> pane=<pane-id>`.
var captureHeaderPattern = regexp.MustCompile(
	`^# captured \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2}|[+-]\d{4}) pane=\d+$`,
)

// readCapture loads a real screen capture.
//
// Every fixture under pkg/testdata/fleetpusher was produced by
// `pkg/testdata/fleetpusher/capture.sh`, which reads a scratch pane driven to the
// named state with `wezterm cli get-text --escapes --pane-id` and writes the
// provenance line above it, so the escape bytes are the terminal's own — not a
// hand-written approximation. A hand-written screen would not exercise the
// SGR/dim parsing that is the whole reason the guard exists.
//
// The provenance line is the file's first line. The guard skips it, so the
// fixture is replayed unmodified.
func readCapture(name string) *string {
	raw, err := os.ReadFile(filepath.Join("testdata", "fleetpusher", name+".txt"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	text := string(raw)
	return &text
}

var _ = ginkgo.Describe("FleetPusher guard", func() {
	ginkgo.Describe("capture provenance", func() {
		// capture.sh writes `# captured <ISO8601> pane=<id>` above every screen.
		// The line records provenance; it does not prove it — a hand-typist can
		// forge one — but a fixture added without it fails here, so a hand-typed
		// screen can no longer stand in for a capture silently.
		paths, globErr := filepath.Glob(filepath.Join("testdata", "fleetpusher", "*.txt"))
		if globErr != nil || len(paths) == 0 {
			ginkgo.It("finds fixtures to check", func() {
				ginkgo.Fail("no fixtures under testdata/fleetpusher")
			})
			return
		}
		for _, path := range paths {
			path := path
			ginkgo.It("headers "+filepath.Base(path), func() {
				raw, err := os.ReadFile(path)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				first := strings.SplitN(string(raw), "\n", 2)[0]
				gomega.Expect(strings.TrimSuffix(first, "\r")).To(
					gomega.MatchRegexp(captureHeaderPattern.String()),
				)
			})
		}

		// The header is provenance, not screen content: each capture must read
		// exactly as the same screen with the header line removed.
		ginkgo.It("reads every capture as its screen below the header", func() {
			for _, path := range paths {
				raw, err := os.ReadFile(path)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				text := string(raw)
				body := text
				if index := strings.IndexByte(text, '\n'); strings.HasPrefix(text, captureHeader) && index >= 0 {
					body = text[index+1:]
				}
				gomega.Expect(PaneGuardVerdict(&text, false, "")).To(
					gomega.Equal(PaneGuardVerdict(&body, false, "")),
					filepath.Base(path)+" reads differently with its capture header",
				)
			}
		})
	})

	// The stripping contract itself, over exact strings: what counts as the
	// header, and what is left behind.
	ginkgo.Describe("withoutCaptureHeader", func() {
		ginkgo.DescribeTable("drops a leading provenance line and nothing else",
			func(screen, expected string) {
				gomega.Expect(withoutCaptureHeader(screen)).To(gomega.Equal(expected))
			},
			ginkgo.Entry(
				"header above a screen",
				"# captured 2026-11-18T22:23:47+0100 pane=196\r\n  done.\r\n",
				"  done.\r\n",
			),
			ginkgo.Entry(
				"header with a bare LF",
				"# captured 2026-11-18T22:23:47+0100 pane=196\n  done.\n",
				"  done.\n",
			),
			ginkgo.Entry("no header", "  done.\r\n", "  done.\r\n"),
			ginkgo.Entry(
				"header only",
				"# captured 2026-11-18T22:23:47+0100 pane=196\r\n",
				"",
			),
			ginkgo.Entry(
				"header with no newline",
				"# captured 2026-11-18T22:23:47+0100 pane=196",
				"",
			),
			ginkgo.Entry(
				"marker below the first line is screen content",
				"  done.\r\n# captured 2026-11-18T22:23:47+0100 pane=196\r\n",
				"  done.\r\n# captured 2026-11-18T22:23:47+0100 pane=196\r\n",
			),
		)

		ginkgo.It("reads a header over an empty screen as an empty read", func() {
			screen := "# captured 2026-11-18T22:23:47+0100 pane=196\r\n\r\n"
			gomega.Expect(PaneGuardVerdict(&screen, false, "")).To(gomega.Equal(VerdictSkipUnclassified))
		})
	})

	ginkgo.Describe("PaneGuardVerdict over captured screens", func() {
		// The guard's screen verdicts. Eleven are screens captured from a real
		// terminal; `skip:sleeping` and `error:unreadable` are parameter/read-failure
		// branches with no screen to capture, and `skip:state-unreadable` is spec'd
		// in the subject-state block below.
		table := []struct {
			fixture string
			verdict string
		}{
			{"wake", VerdictWake},
			{"wake-suggestion", VerdictWakeSuggestion},
			{"spinner", VerdictSkipSpinner},
			{"operator-ask", VerdictSkipOperatorAsk},
			{"unclassified", VerdictSkipUnclassified},
			{"modal-select", VerdictSkipModalSelect},
			{"modal-cancel", VerdictSkipModalCancel},
			{"option-list", VerdictSkipOptionList},
			{"composer-not-empty", VerdictSkipComposerNotEmpty},
			{"no-composer", VerdictSkipNoComposer},
			{"machine-wait", VerdictSkipMachineWait},
		}
		for _, row := range table {
			row := row
			ginkgo.It("reads "+row.fixture+" as "+row.verdict, func() {
				gomega.Expect(PaneGuardVerdict(readCapture(row.fixture), false, "")).
					To(gomega.Equal(row.verdict))
			})
		}
	})

	ginkgo.Describe("subject-state branches", func() {
		ginkgo.It("refuses a date-gated asleep subject", func() {
			gomega.Expect(PaneGuardVerdict(readCapture("wake"), false, SubjectStateSleeping)).
				To(gomega.Equal(VerdictSkipSleeping))
		})

		ginkgo.It("fails closed when a resolved subject's state is unreadable", func() {
			gomega.Expect(PaneGuardVerdict(readCapture("wake"), false, SubjectStateUnreadable)).
				To(gomega.Equal(VerdictSkipStateUnreadable))
		})

		ginkgo.It("returns error:unreadable when the pane could not be read", func() {
			gomega.Expect(PaneGuardVerdict(nil, false, "")).
				To(gomega.Equal(VerdictErrorUnreadable))
		})
	})

	ginkgo.Describe("allowMachineWait", func() {
		ginkgo.It("lets a sweep-gate wake pass a declared machine wait", func() {
			gomega.Expect(PaneGuardVerdict(readCapture("machine-wait"), true, "")).
				To(gomega.Equal(VerdictWake))
		})
	})
})
