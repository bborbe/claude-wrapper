// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// BranchInput is everything ResolveBranch decides from.
//
// The tri-state fields are POINTERS, not bools, because the Python distinguishes
// three answers and folding two of them together is the defect this port exists
// to preserve: `ChainLive` is nil when the probe could not answer, and a nil there
// must NOT fire `open-manager` — only a proven false may. Same for `OwnerLive`.
type BranchInput struct {
	Kind      string
	Role      string
	SessionID string
	// Live is `LIVE`, `ABSENT`, `UNKNOWN`, `AMBIGUOUS`, `MALFORMED`, or `none`.
	Live string
	// Pane is the resolved pane id, empty when none.
	Pane string
	// ChainLive is whether the row's subject has a live manager; nil = unanswerable.
	ChainLive *bool
	// BlockRole is the task page's own `role:` value, read from its block.
	BlockRole string
	// Owner is the resolved owning subject's name, or FleetOwner.
	Owner string
	// OwnerLive is the owner page's recorded session verdict; nil = unanswerable.
	OwnerLive *bool
	// RegistryOK reports whether the registry read succeeded.
	RegistryOK bool
	// Parked reports a declared external wait on the row's own last closer.
	Parked bool
}

// FleetOwner is the sentinel owning every task whose goal and topic have no
// live manager.
const FleetOwner = "Fleet Manager"

// Refusal reasons a row may carry.
const (
	RefusalAgentSetFlag = "agent-set-flag"
	RefusalManagerRow   = "manager-row"
)

// ResolveBranch is the ordered predicate list — first match wins, so the set is
// a partition. Ported from `flagged-rows.py:875`; the ORDER is the rule, and each
// branch's position carries a reason, recorded below.
//
// `unknown` outranks everything: an unanswerable probe must never be folded into
// "not live", which is the double-writer the liveness probe forbids.
//
// `degraded` is the one exception, and it does not weaken that ranking. When the
// registry read SUCCEEDED, the owner is known even though the row-level probe
// could not answer: the registry proved the process alive while the heartbeat
// says the session is not working — two different questions. Folding that into
// `unknown` throws the owner away. `degraded` never spawns, so it cannot duplicate
// the manager that already exists, and it never resumes, so it cannot put a second
// writer on a live process. An unreadable registry keeps `unknown`: there the owner
// is not known.
//
// `open-manager` keys on `ChainLive == false` — proven, never assumed. nil does
// not fire it, for the same reason `unknown` outranks `create`.
//
// `held-owner-live` is the TASK-tier gate: a task that would otherwise CREATE a
// worker is held when its owning subject already has a live manager, because two
// sessions on one subject is the collision this branch prevents. It sits after
// every row-own branch, so a task whose own session is live still jumps or
// resumes — only a new worker is suppressed. Two guards keep it narrow: the fleet
// sentinel owns every task whose goal and topic have no live manager, so holding
// on it would suppress the whole batch; and `OwnerLive` must be true, never nil —
// a probe that cannot answer must not suppress work.
//
// `parked` is the one branch that is NOT a liveness verdict. It sits AFTER every
// LIVE branch and BEFORE `resume`, and that position IS the rule: a LIVE row is
// open — it jumps or skips — and folding it into `parked` would drop a session
// that exists out of coverage's numerator. An ABSENT row whose last closer
// declared an external wait is exactly the row `resume` would otherwise reopen on
// every tick, so this is what stops a 100% target from driving the fleet back into
// resuming idle workers.
func ResolveBranch(in BranchInput) (branch string, refused []string) {
	refused = []string{}
	switch in.Live {
	case "UNKNOWN", "AMBIGUOUS", "MALFORMED":
		if in.RegistryOK && in.Owner != "" {
			return "degraded", refused
		}
		return "unknown", refused
	}
	if in.Role == "human" {
		return "direct", refused
	}
	if in.Kind == "task" && in.BlockRole == "manager" {
		return "skip", []string{RefusalManagerRow}
	}
	if (in.Kind == "goal" || in.Kind == "topic") && in.ChainLive != nil && !*in.ChainLive {
		return "open-manager", refused
	}
	if in.Live == "LIVE" && in.Pane != "" {
		return "jump", refused
	}
	if in.Live == "LIVE" {
		return "skip", refused
	}
	if in.Parked {
		return "parked", refused
	}
	if in.Live == "ABSENT" && in.SessionID != "" {
		return "resume", refused
	}
	if in.Kind == "task" && in.Owner != FleetOwner && in.OwnerLive != nil && *in.OwnerLive {
		return "held-owner-live", refused
	}
	return "create", refused
}
