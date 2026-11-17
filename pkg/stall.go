// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "slices"

// The stall point records where a child that never emitted its init event was
// waiting, so the startup deadline's kill diagnoses itself from the session's
// exit record instead of a post-mortem.
//
// Two readers: `SessionExitFileRecorder`, which writes it into the exit line,
// and the session snapshot, which reports it over the API while a child is
// starting.
//
// The point is allocated when the child is spawned, not on its first event, so
// a child that emitted nothing at all is still diagnosable — that is the worst
// case, and it has no event to hang the record on.

// recordStallLocked keeps the stall point current while the child is still
// starting, so a kill at the startup deadline reports where it waited instead
// of only that no init event arrived.
//
// Only events this process saw are recorded. A replayed transcript describes an
// earlier run, and folding it in would report a stall point this child never
// reached — which is the false positive the resume population already suffers
// from at the deadline itself.
//
// The caller must hold c.mutex.
func (c *child) recordStallLocked(event map[string]any) {
	if c.session.Stall == nil {
		c.session.Stall = &Stall{}
	}
	stall := c.session.Stall
	stall.LastEvent = eventLabel(event)
	if event["type"] != "system" {
		return
	}
	name, _ := event["hook_name"].(string)
	switch event["subtype"] {
	case "hook_started":
		// One entry per start, never deduped by name: several SessionStart
		// hooks share a name, so a name-keyed set would drop the second start
		// and then clear the phase when the first one returned.
		if name != "" {
			stall.PendingHooks = append(stall.PendingHooks, name)
		}
	case "hook_response":
		// Remove exactly one entry: two same-named hooks that both started must
		// both return before the phase leaves "hook".
		if index := slices.Index(stall.PendingHooks, name); index >= 0 {
			stall.PendingHooks = slices.Delete(stall.PendingHooks, index, index+1)
		}
	}
}

// eventLabel renders an event as `type/subtype`, the vocabulary the exit record
// and the stall point share.
func eventLabel(event map[string]any) string {
	eventType, _ := event["type"].(string)
	if eventType == "" {
		return ""
	}
	subtype, _ := event["subtype"].(string)
	if subtype == "" {
		return eventType
	}
	return eventType + "/" + subtype
}

// stallPhase names where a starting child was waiting.
//
// "hook" is a hook that never returned, "no-event" is a child that emitted
// nothing at all, and "awaiting-init" is everything else — which is likely the
// MCP-connect phase, since the child emits init only after every server has
// been tried.
func stallPhase(stall *Stall) string {
	switch {
	case len(stall.PendingHooks) > 0:
		return "hook"
	case stall.LastEvent == "":
		return "no-event"
	}
	return "awaiting-init"
}
