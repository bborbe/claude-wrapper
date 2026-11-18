// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"
)

// Hold is one operator session hold.
type Hold struct {
	Reason string `json:"reason"`
	HeldAt string `json:"held_at"`
}

// HoldStore is the on-disk hold store shape.
type HoldStore struct {
	Holds map[string]Hold `json:"holds"`
}

// heldAtFormat is the writer's stamp format.
const heldAtFormat = "2006-01-02T15:04:05Z"

// ReadHolds reads the hold store, keyed by session id.
//
// A missing, unreadable or malformed store returns an empty map and a nil error.
// A hold is an operator instruction to STOP: inventing one from an unreadable file
// would suppress wakes for sessions the operator never held, while reading a real
// store as empty merely fails to honour a hold, loudly, on the next tick.
func ReadHolds(ctx context.Context, path string) (map[string]Hold, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]Hold{}, nil
	}
	var envelope struct {
		Holds map[string]json.RawMessage `json:"holds"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return map[string]Hold{}, nil
	}
	out := make(map[string]Hold, len(envelope.Holds))
	for sessionID, entry := range envelope.Holds {
		var hold Hold
		// A non-object entry is not a hold.
		if err := json.Unmarshal(entry, &hold); err != nil {
			continue
		}
		out[sessionID] = hold
	}
	return out, nil
}

// holdEntry returns the hold for a session, or false when it is not held.
func holdEntry(holds map[string]Hold, sessionID string) (Hold, bool) {
	if sessionID == "" {
		return Hold{}, false
	}
	entry, ok := holds[sessionID]
	if !ok {
		return Hold{}, false
	}
	if entry.Reason == "" {
		entry.Reason = "held"
	}
	return entry, true
}

// HeldReason returns the hold's reason for this session, or false when unheld.
func HeldReason(holds map[string]Hold, sessionID string) (string, bool) {
	entry, ok := holdEntry(holds, sessionID)
	if !ok {
		return "", false
	}
	return entry.Reason, true
}

// HeldAge renders how long a hold has stood: `12m` / `4h` / `3d`, `?` unreadable.
//
// `?` is a real answer, not a fallback: an absent or malformed stamp rendered as
// age zero would read as a hold the operator set seconds ago.
func HeldAge(now time.Time, heldAt string) string {
	started, err := time.Parse(heldAtFormat, heldAt)
	if err != nil {
		return "?"
	}
	seconds := int(now.Sub(started).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 3600 {
		return formatUnit(seconds/60, "m")
	}
	if seconds < 86400 {
		return formatUnit(seconds/3600, "h")
	}
	return formatUnit(seconds/86400, "d")
}

// HeldLabel renders the full Status cell for a held session.
//
// `⏸️ HELD — <reason> · <age>` is the ONE render every consumer owes a held row.
func HeldLabel(now time.Time, holds map[string]Hold, sessionID string) (string, bool) {
	entry, ok := holdEntry(holds, sessionID)
	if !ok {
		return "", false
	}
	return "⏸️ HELD — " + entry.Reason + " · " + HeldAge(now, entry.HeldAt), true
}

// formatUnit renders an integer with its unit suffix.
func formatUnit(value int, unit string) string {
	return strconv.Itoa(value) + unit
}
