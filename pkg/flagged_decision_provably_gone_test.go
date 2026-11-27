// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestDecisionProvablyGone pins the admission rule for a row whose liveness is
// not ABSENT: the classifier verdict decides. LIVE is skipped `not-absent`, an
// unreadable source is skipped `indeterminate` (never auto-opened), and only a
// provably absent session is admitted.
func TestDecisionProvablyGone(t *testing.T) {
	tests := []struct {
		name       string
		verdict    livenessVerdict
		wantSkip   bool
		wantReason string
	}{
		{
			name:       "live -> skipped not-absent",
			verdict:    livenessLive,
			wantSkip:   true,
			wantReason: reasonNotAbsent,
		},
		{
			name:       "indeterminate -> skipped indeterminate",
			verdict:    livenessIndeterminate,
			wantSkip:   true,
			wantReason: reasonIndeterminate,
		},
		{
			name:       "absent -> admitted",
			verdict:    livenessAbsent,
			wantSkip:   false,
			wantReason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, skipped := skipNotAbsent(tt.verdict)
			if skipped != tt.wantSkip {
				t.Fatalf("skipped = %v, want %v (reason %q)", skipped, tt.wantSkip, reason)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// TestChildLivenessResolvesTheStore pins the wiring, not just the decision rule.
//
// main.go sets no Sessions, so in the daemon the field is nil. A childLiveness
// that returned an empty map for nil would make the whole fix a no-op: every row
// would stay "not provably gone" and be skipped forever, which is the defect the
// change exists to end.
func TestChildLivenessResolvesTheStore(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	store := NewSessionStore(filepath.Join(home, ".claude", "state", "claude-wrapper", "sessions"))
	// A pid above the platform's range: FindProcess succeeds, Signal fails, so
	// pidAlive reports it gone without relying on a real process having exited.
	const deadPID = 1 << 22
	if _, err := store.Create(ctx, SessionRecord{ID: "dead-child", ChildPID: deadPID, StartedAt: time.Now()}); err != nil {
		t.Fatalf("create record: %v", err)
	}

	tick := &FlaggedTick{Home: home, Logf: func(string, ...any) {}}

	got := tick.childLiveness(ctx)
	alive, ok := got["dead-child"]
	if !ok {
		t.Fatalf("childLiveness did not read the resolved store; map = %v", got)
	}
	if alive {
		t.Fatalf("dead child pid reported alive")
	}
}
