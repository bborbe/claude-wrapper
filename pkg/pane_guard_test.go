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
// `wezterm cli get-text --escapes --pane-id <id>` against a scratch pane driven to
// the named state, so the escape bytes are the terminal's own — not a hand-written
// approximation. A hand-written screen would not exercise the SGR/dim parsing that
// is the whole reason the guard exists.
//
// Each file opens with a `# captured <ISO8601> pane=<id>` line naming the pane it
// was read from. The guard skips that line; the provenance spec below refuses a
// file that lacks it, so a hand-typed screen cannot stand in for a capture.
func readCapture(name string) *string {
	raw, err := os.ReadFile(filepath.Join("testdata", "fleetpusher", name+".txt"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	text := string(raw)
	return &text
}

var _ = ginkgo.Describe("FleetPusher guard", func() {
	ginkgo.Describe("capture provenance", func() {
		// Every fixture in the directory must carry the capture header. The
		// directory is globbed rather than listed, so a fixture added by hand
		// later fails here instead of passing as a capture.
		ginkgo.It("headers every fixture in testdata/fleetpusher", func() {
			entries, err := filepath.Glob(filepath.Join("testdata", "fleetpusher", "*.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(entries).NotTo(gomega.BeEmpty())
			for _, entry := range entries {
				raw, readErr := os.ReadFile(entry)
				gomega.Expect(readErr).NotTo(gomega.HaveOccurred())
				first := strings.SplitN(string(raw), "\n", 2)[0]
				gomega.Expect(strings.TrimSuffix(first, "\r")).To(
					gomega.MatchRegexp(captureHeaderPattern.String()),
					filepath.Base(entry)+" does not open with `# captured <ISO8601> pane=<pane-id>`",
				)
			}
		})

		// The header is provenance, not screen content: each capture must read
		// exactly as the same screen with the header line removed.
		ginkgo.It("reads each capture exactly as its screen below the header", func() {
			entries, err := filepath.Glob(filepath.Join("testdata", "fleetpusher", "*.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(entries).NotTo(gomega.BeEmpty())
			for _, entry := range entries {
				raw, readErr := os.ReadFile(entry)
				gomega.Expect(readErr).NotTo(gomega.HaveOccurred())
				text := string(raw)
				body := text
				if index := strings.IndexByte(text, '\n'); strings.HasPrefix(text, captureHeader) && index >= 0 {
					body = text[index+1:]
				}
				gomega.Expect(PaneGuardVerdict(&text, false, "")).To(
					gomega.Equal(PaneGuardVerdict(&body, false, "")),
					filepath.Base(entry)+" reads differently with its capture header",
				)
			}
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
