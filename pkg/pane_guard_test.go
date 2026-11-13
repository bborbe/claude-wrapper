// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// readCapture loads a real screen capture.
//
// Every fixture under pkg/testdata/fleetpusher was produced by
// `wezterm cli get-text --escapes --pane-id <id>` against a scratch pane driven to
// the named state, so the escape bytes are the terminal's own — not a hand-written
// approximation. A hand-written screen would not exercise the SGR/dim parsing that
// is the whole reason the guard exists.
func readCapture(name string) *string {
	raw, err := os.ReadFile(filepath.Join("testdata", "fleetpusher", name+".txt"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	text := string(raw)
	return &text
}

var _ = ginkgo.Describe("FleetPusher guard", func() {
	ginkgo.Describe("PaneGuardVerdict over captured screens", func() {
		// The 13 guard verdicts. Eleven are screens captured from a real terminal;
		// `skip:sleeping` and `error:unreadable` are parameter/read-failure
		// branches with no screen to capture, and are spec'd below.
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
