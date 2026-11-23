// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"testing"
	"time"
)

// TestDecisionProvablyGone pins the admission rule for a row whose liveness is
// not ABSENT: it is opened only when the ledger says the session ended AND the
// session store holds a child record whose pid is dead.
//
// The cases are the contract. (a) is the fix; (b)-(e) are the shapes it must
// not break. (e) is the one that matters most: a missing key in
// map[string]bool reads false, so a literal `!childLive[id]` would admit a
// registry-only session and put a second writer on a live conversation.
func TestDecisionProvablyGone(t *testing.T) {
	const sessionID = "11111111-2222-3333-4444-555555555555"
	now := time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)

	terminal := map[string]struct{}{sessionID: {}}

	tests := []struct {
		name       string
		liveness   string
		childLive  map[string]bool
		wantSkip   bool
		wantReason string
	}{
		{
			name:       "(a) terminal ledger + child pid dead -> admitted",
			liveness:   "LIVE",
			childLive:  map[string]bool{sessionID: false},
			wantSkip:   false,
			wantReason: "",
		},
		{
			name:       "(b) terminal ledger + child pid alive -> skipped",
			liveness:   "LIVE",
			childLive:  map[string]bool{sessionID: true},
			wantSkip:   true,
			wantReason: reasonNotAbsentDead,
		},
		{
			name:       "(c) UNKNOWN + terminal + no live process -> skipped",
			liveness:   "UNKNOWN",
			childLive:  map[string]bool{sessionID: false},
			wantSkip:   true,
			wantReason: reasonNotAbsentDead,
		},
		{
			name:       "(d) terminal ledger + live pid -> skipped",
			liveness:   "LIVE",
			childLive:  map[string]bool{sessionID: true},
			wantSkip:   true,
			wantReason: reasonNotAbsentDead,
		},
		{
			name:       "(e) terminal ledger + session absent from the map -> skipped",
			liveness:   "LIVE",
			childLive:  map[string]bool{},
			wantSkip:   true,
			wantReason: reasonNotAbsentDead,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := FlaggedRow{SessionID: sessionID, Liveness: tt.liveness}
			reason, refused, skipped := decision(row, nil, terminal, tt.childLive, now, false, false)
			if refused {
				t.Fatalf("refused = true, want false (reason %q)", reason)
			}
			if skipped != tt.wantSkip {
				t.Fatalf("skipped = %v, want %v (reason %q)", skipped, tt.wantSkip, reason)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
