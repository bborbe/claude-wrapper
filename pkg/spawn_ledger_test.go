// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// patchRecordingLedger records the patch calls and answers with a fixed error.
type patchRecordingLedger struct {
	err     error
	patched int
}

func (l *patchRecordingLedger) Rows(context.Context) ([]LedgerRow, error) { return nil, nil }

func (l *patchRecordingLedger) Post(context.Context, string, LedgerRow) error { return nil }

func (l *patchRecordingLedger) Patch(context.Context, string, LedgerPatch) error {
	l.patched++
	return l.err
}

var _ = ginkgo.Describe("spawn ledger resume", func() {
	ginkgo.Describe("resumeSpawnPatch", func() {
		// The criterion that keeps this fix from silently reverting: the spawn
		// edge and the provenance belong to the original spawn, and a resume that
		// names them is the write that blanked 320 sessions' spawners (measured
		// 2026-11-26).
		ginkgo.It("never names the spawn edge or the provenance", func() {
			patch := resumeSpawnPatch("Some Task", "/tmp", RoleAgent, "abc-123")
			for _, forbidden := range []string{
				"spawner_session_id",
				"parent_session",
				"mode_source",
				"spawned_at",
			} {
				_, ok := patch[forbidden]
				gomega.Expect(ok).To(
					gomega.BeFalse(),
					"resumeSpawnPatch names %q — it belongs to the original spawn",
					forbidden,
				)
			}
			gomega.Expect(string(patch["resumed_from"])).To(gomega.Equal(`"abc-123"`))
			gomega.Expect(string(patch["status"])).To(gomega.Equal(`"running"`))
		})

		// The keep-alive resume carries nothing but the session id: writing ""
		// over a recorded label or role is the same field loss this patch stops.
		ginkgo.It("omits the fields the request did not name", func() {
			patch := resumeSpawnPatch("", "", "", "abc-123")
			for _, absent := range []string{"label", "cwd", "role"} {
				_, ok := patch[absent]
				gomega.Expect(ok).To(
					gomega.BeFalse(),
					"resumeSpawnPatch wrote %q from an empty request",
					absent,
				)
			}
		})
	})

	ginkgo.Describe("patchLedgerRow", func() {
		ginkgo.DescribeTable(
			"decides whether a POST is still needed",
			func(err error, wantDone bool) {
				ledger := &patchRecordingLedger{err: err}
				done := patchLedgerRow(
					context.Background(),
					ledger,
					"s1",
					LedgerPatch{"status": []byte(`"running"`)},
				)
				gomega.Expect(done).To(gomega.Equal(wantDone))
				gomega.Expect(ledger.patched).To(gomega.Equal(1))
			},
			ginkgo.Entry("the patch landed", nil, true),
			ginkgo.Entry(
				"the row is absent, so the caller POSTs",
				errors.Wrap(context.Background(), ErrLedgerRowAbsent, "no row"),
				false,
			),
			ginkgo.Entry(
				"the store failed, so the caller must not POST over the row",
				stderrors.New("boom"),
				true,
			),
		)
	})
})
