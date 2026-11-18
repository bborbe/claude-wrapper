// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"regexp"
	"strings"
	"time"
)

// The three liveness verdicts a row can carry.
const (
	LivenessLive   = "live"
	LivenessParked = "parked"
	LivenessNone   = "none"
)

// buckets maps a definitive status to its label and bucket key.
//
// An aborted task is an overlay on `done`, not a bucket of its own: it keeps the
// glyph and the tally and carries a ` · aborted` suffix.
var buckets = map[string][2]string{
	"completed": {"✅ done", "done"},
	"aborted":   {"✅ done · aborted", "done"},
	"hold":      {"⏸️ blocked/hold", "hold"},
	"backlog":   {"—", "backlog"},
}

// nonSpawableRoles are the roles that are not spawn targets: a manager does not
// spawn managers, and a human's work is not a delegate's.
var nonSpawableRoles = map[string]bool{"human": true, "manager": true}

// deferOverlayExempt are the definitive dispositions the deferred overlay must not
// override. `waiting` is exempt because a raised gate must never be hidden by a
// defer date.
var deferOverlayExempt = map[string]bool{
	"done": true, "hold": true, "backlog": true, "waiting": true,
}

// Glyphs is the bucket key -> icon map. Every key Classify can return must appear
// here and in BucketOrder.
var Glyphs = map[string]string{
	"progressing":  "🔄",
	"waiting":      "⌛",
	"approval":     "⌛",
	"unregistered": "⌛",
	"external":     "⌛",
	"declared":     "⌛",
	"hold":         "⏸️",
	"problem":      "⚠️",
	"ready":        "🚀",
	"yours":        "👤",
	"done":         "✅",
	"backlog":      "◦",
	"other":        "—",
	"upstream":     "⏸️",
}

// BucketOrder is the marker's ordering. A key present in Glyphs but absent here
// is counted and then silently dropped from the marker, so both maps stay in step.
var BucketOrder = []string{
	"progressing",
	"waiting",
	"approval",
	"unregistered",
	"external",
	"declared",
	"hold",
	"problem",
	"ready",
	"yours",
	"done",
	"backlog",
	"upstream",
	"other",
}

// deferDateRe matches the date prefix of either stored shape: the bare
// `YYYY-MM-DD` and the RFC3339 datetime vault-cli writes unquoted.
var deferDateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)

// Bucket renders one row's status cell and bucket key.
//
// The hold overlay is applied FIRST and unconditionally: a hold is an operator
// statement about the session and must read as held whatever else the row says.
// The deferred overlay changes the label and leaves the key alone, and is
// suppressed for a definitive disposition.
func Bucket(now time.Time, holds map[string]Hold, t Task) (string, string) {
	label, key := Classify(now, t)
	// Read t.Session only, never the id set: the reference records that reading
	// the raw frontmatter key here compiles, runs and is inert.
	if held, ok := HeldLabel(now, holds, t.Session); ok {
		return held, key
	}
	if !deferOverlayExempt[key] && IsDeferred(now, t) {
		return "⏸️ deferred", key
	}
	return label, key
}

// Classify is the ordinary bucket precedence, before the overlays.
//
// The order is the contract: each placement has a recorded false positive behind
// it. Do not reorder to tidy it.
func Classify(now time.Time, t Task) (string, string) {
	if pair, ok := buckets[t.Status]; ok {
		return pair[0], pair[1]
	}
	// A raised gate IS the worker asking the human.
	if t.Phase == "human_review" {
		return "⌛ waiting-on-human", "waiting"
	}
	// A parked worker is waiting on a human, sharper than any work state.
	if t.Liveness == LivenessParked {
		return "⌛ waiting-on-human", "waiting"
	}
	// `todo` is the operator's approval boundary: unapproved work is not ready.
	if t.Phase == "todo" && !t.OperatorFlagged {
		return "⌛ waiting-approval", "approval"
	}
	// A stopped worker with a machine-watched wait: must beat in_flight.
	if t.ExternalWait {
		return "⌛ waiting-external", "external"
	}
	// Mid-tool-call work: must beat both the park cell and stuck.
	if t.InFlight {
		return "🔄 progressing", "progressing"
	}
	// A declared wait is quiet, not stalled: must beat the park cell and stuck.
	if t.DeclaredWaitActive {
		return "⌛ waiting-declared", "declared"
	}
	// A live owner parked on an unregistered gate: beats stuck, after approval.
	if t.UnregisteredPark {
		return "⌛ parked-on-unregistered-gate", "unregistered"
	}
	// The role carve-out is phase-independent: it must beat stuck and the offer.
	if isYours(t) {
		return "👤 YOURS", "yours"
	}
	// Stuck is suppressed for a row held from outside: waiting is not stalled.
	if t.Stuck && !IsWaiting(now, t) {
		return "⚠️ problem", "problem"
	}
	if isOffer(t) {
		return offerCell(t)
	}
	if t.Status == "in_progress" {
		return "🔄 progressing", "progressing"
	}
	return "—", "other"
}

// isYours reports the non-spawnable-role carve-out. A live owner is being worked,
// so it keeps `progressing`; a genuinely blocked row is named better as blocked.
func isYours(t Task) bool {
	return nonSpawableRoles[t.Role] && !t.BlockedUpstream && t.Liveness != LivenessLive
}

// isOffer reports the approved-and-not-yet-started arm. `next` is kept because
// many rows carry it with no phase key; `planning` is the narrow approved state.
func isOffer(t Task) bool {
	return t.Status == "next" || (t.Status == "in_progress" && t.Phase == "planning")
}

// offerCell resolves the offer arm: a live owner is progressing, an unmet blocker
// is upstream, otherwise the row is ready to start.
func offerCell(t Task) (string, string) {
	if t.Liveness == LivenessLive {
		return "🔄 progressing", "progressing"
	}
	if t.BlockedUpstream {
		return "⏸️ blocked/upstream", "upstream"
	}
	return "🚀 ready-to-start", "ready"
}

// StatusLabel is the bucket icon for a bare status, used by topic and goal rows.
//
// It builds the neutral row Bucket requires rather than re-implementing the
// status-to-glyph mapping. The nil hold store and the neutral row's empty session
// make the hold and defer overlays inert.
func StatusLabel(now time.Time, t Task) string {
	neutral := Task{
		Status:   t.Status,
		Phase:    "",
		Session:  "",
		Liveness: LivenessNone,
		Stuck:    false,
	}
	label, _ := Bucket(now, nil, neutral)
	return label
}

// IsDeferred reports whether defer_date names a date strictly in the future.
//
// The clock is a parameter, never time.Now(): the reference's date.today() is the
// only non-injected clock in this port. An unparseable or absent date is false.
func IsDeferred(now time.Time, t Task) bool {
	year, month, date, ok := parseDeferDate(t.DeferDate)
	if !ok {
		return false
	}
	// ⚠️ Compare calendar dates, never instants. `defer_date` is a bare date with
	// no zone; parsed as UTC and compared against a LOCAL midnight it reads as
	// "after" on every UTC+ host, so a task whose defer expires TODAY stays
	// deferred for the whole day — no action line, stuck suppressed — which is
	// exactly the dead park the defer rule exists to avoid. The reference compares
	// date to date (`sweep-gate.py:2004`, `day > date.today()`), and so does this.
	todayYear, todayMonth, todayDate := now.Local().Date()
	if year != todayYear {
		return year > todayYear
	}
	if month != todayMonth {
		return month > todayMonth
	}
	return date > todayDate
}

// parseDeferDate parses the date prefix of either stored defer_date shape and
// returns its calendar parts.
//
// It returns Y/M/D rather than a time.Time on purpose: a time.Time carries a zone
// this value does not have, and every zone choice is a bug waiting to compare
// against the wrong midnight. See IsDeferred.
func parseDeferDate(raw string) (int, time.Month, int, bool) {
	trimmed := strings.Trim(strings.TrimSpace(raw), `'"`)
	match := deferDateRe.FindStringSubmatch(trimmed)
	if match == nil {
		return 0, 0, 0, false
	}
	day, err := time.Parse("2006-01-02", match[0])
	if err != nil {
		return 0, 0, 0, false
	}
	return day.Year(), day.Month(), day.Day(), true
}

// IsWaiting reports whether something outside the worker holds the row.
//
// The stuck proxy is the task file's mtime, which a wedged worker and a correctly
// parked one satisfy identically. Suppression only: the row keeps its place.
func IsWaiting(now time.Time, t Task) bool {
	if t.Liveness == LivenessParked {
		return true
	}
	if t.BlockedUpstream {
		return true
	}
	if IsDeferred(now, t) {
		return true
	}
	return t.DeclaredWaitActive
}
