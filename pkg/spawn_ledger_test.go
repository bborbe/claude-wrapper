// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/bborbe/errors"
)

// TestResumeSpawnPatchNeverNamesTheSpawnEdge is the criterion that keeps this
// fix from silently reverting: the spawn edge and the provenance belong to the
// original spawn, and a resume that names them is the write that blanked 320
// sessions' spawners (measured 2026-11-26).
func TestResumeSpawnPatchNeverNamesTheSpawnEdge(t *testing.T) {
	patch := resumeSpawnPatch("Some Task", "/tmp", RoleAgent, "abc-123")
	for _, forbidden := range []string{
		"spawner_session_id",
		"parent_session",
		"mode_source",
		"spawned_at",
	} {
		if _, ok := patch[forbidden]; ok {
			t.Fatalf(
				"resumeSpawnPatch names %q — it belongs to the original spawn and a "+
					"resume must never overwrite it",
				forbidden,
			)
		}
	}
	if got := string(patch["resumed_from"]); got != `"abc-123"` {
		t.Fatalf("resumed_from = %s, want %q", got, `"abc-123"`)
	}
	if got := string(patch["status"]); got != `"running"` {
		t.Fatalf("status = %s, want %q", got, `"running"`)
	}
}

// TestResumeSpawnPatchOmitsEmptyFields guards the keep-alive resume, which
// carries nothing but the session id: writing "" over a recorded label or role
// is the same field loss the patch exists to stop.
func TestResumeSpawnPatchOmitsEmptyFields(t *testing.T) {
	patch := resumeSpawnPatch("", "", "", "abc-123")
	for _, absent := range []string{"label", "cwd", "role"} {
		if _, ok := patch[absent]; ok {
			t.Fatalf(
				"resumeSpawnPatch wrote %q from an empty request, which would blank "+
					"the recorded value",
				absent,
			)
		}
	}
}

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

// TestPatchLedgerRowFallsThroughOnlyWhenTheRowIsAbsent pins the one case a POST
// is still correct. A store error must NOT fall through: the POST would replace
// the row the patch failed to update, which is the defect itself.
func TestPatchLedgerRowFallsThroughOnlyWhenTheRowIsAbsent(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantDone bool
	}{
		{"landed", nil, true},
		{
			"absent",
			errors.Wrap(context.Background(), ErrLedgerRowAbsent, "no row"),
			false,
		},
		{"store error", stderrors.New("boom"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &patchRecordingLedger{err: tc.err}
			done := patchLedgerRow(
				context.Background(),
				ledger,
				"s1",
				LedgerPatch{"status": []byte(`"running"`)},
			)
			if done != tc.wantDone {
				t.Fatalf("done = %v, want %v", done, tc.wantDone)
			}
			if ledger.patched != 1 {
				t.Fatalf("patched %d times, want 1", ledger.patched)
			}
		})
	}
}
