// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// ParseTopicKeepaliveVerdict reads `topic-keepalive.py`'s first stdout line.
//
// The script's own docstring defines the contract: *"The first line of stdout is
// the verdict, one token"*. A caller that hands over the whole output gets its
// first non-empty line read here, so the rule lives in one place.
func ParseTopicKeepaliveVerdict(ctx context.Context, output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.ContainsAny(trimmed, " \t") {
			return "", errors.Errorf(ctx, "keepalive verdict line is not one token: %q", trimmed)
		}
		return trimmed, nil
	}
	return "", errors.New(ctx, "keepalive printed no verdict")
}

// ParseParkNudges reads `park-nudge.py`'s `NUDGE` lines.
//
// Shape, from `agents/fleet-keepalive-actor.md` § 3:
//
//	NUDGE <session-id> quiet <N>m (<name>) :: <message>
//
// ⚠️ The text after `::` is taken VERBATIM, including any `::` inside it — the
// message is prose the operator reads, and splitting on every `::` would truncate
// a message that quotes one. Only the FIRST `::` separates the two.
func ParseParkNudges(ctx context.Context, output string) ([]ParkNudge, error) {
	nudges := []ParkNudge{}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "NUDGE ") {
			continue
		}
		body := strings.TrimPrefix(trimmed, "NUDGE ")
		header, message, found := strings.Cut(body, " :: ")
		if !found {
			return nil, errors.Errorf(ctx, "nudge line has no message separator: %q", trimmed)
		}
		fields := strings.Fields(header)
		if len(fields) < 1 || fields[0] == "" {
			return nil, errors.Errorf(ctx, "nudge line names no session: %q", trimmed)
		}
		nudges = append(nudges, ParkNudge{
			SessionID: fields[0],
			Name:      nudgeName(header),
			Message:   message,
		})
	}
	return nudges, nil
}

// nudgeName pulls the roster name out of the trailing `(<name>)` group.
//
// An absent group yields "", which the procedure reports as `undelivered` — the
// script emits no name for a session the roster cannot map.
func nudgeName(header string) string {
	open := strings.LastIndex(header, "(")
	if open < 0 {
		return ""
	}
	closeIdx := strings.LastIndex(header, ")")
	if closeIdx < open {
		return ""
	}
	return strings.TrimSpace(header[open+1 : closeIdx])
}

// ParseWakeMisses reads `wake-misses.py`'s `MISS` lines.
//
// Shape, from `commands/steps/wake-misses.md`:
//
//	MISS <vault> <subject> <ts> <result> <session> <live>
//
// ⚠️ **`STALE` is not a miss.** The step's own table sends `STALE` to
// fleet-triage and gives the keep-alive leg only `MISS`; folding the two would
// report a subject that may simply have stopped producing actionable ticks.
func ParseWakeMisses(ctx context.Context, output string) ([]WakeMiss, error) {
	misses := []WakeMiss{}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "MISS ") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 7 {
			return nil, errors.Errorf(ctx, "wake-miss line has %d fields, want 7: %q", len(fields), trimmed)
		}
		misses = append(misses, WakeMiss{
			Subject:   fields[2],
			Result:    fields[4],
			SessionID: fields[5],
			Live:      strings.TrimPrefix(fields[6], "live="),
		})
	}
	return misses, nil
}
