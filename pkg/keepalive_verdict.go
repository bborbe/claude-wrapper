// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// KeepaliveEffectForVerdict maps one script verdict token to the effect the tick
// logs.
//
// The scripts emit MORE words than the effect set has, and the two vocabularies
// answer different questions: a script word says what the probe found, an effect
// word says what the tick did about it. Keeping the translation in one place is
// what stops a procedure inventing a ninth word, and it is the only place the
// mapping has to be corrected when a script adds a verdict.
//
// ⚠️ **`crash-loop` is the resume cap spent**, not a separate crash guard. The
// script's own docstring says so: *"the resume cap is spent; stop restarting and
// escalate once"*. It therefore logs `capped`, and a mapping that gave it its own
// reason would hide the cap from the one probe that counts it.
//
// ⚠️ **`open` and `open-unflagged` are not this task's acts.** Opening a row that
// never had a session belongs to the sibling task; here they are recorded as
// skipped with a reason naming that boundary, never silently dropped — a quiet
// tick would make an unopened row indistinguishable from a healthy one.
//
// The verdict token is the FIRST line of the script's stdout. A caller that hands
// over the whole output gets its first line read here rather than at every call
// site.
func KeepaliveEffectForVerdict(ctx context.Context, verdict string) (KeepaliveEffect, error) {
	switch verdict {
	case "resume":
		return KeepaliveEffect{Result: KeepaliveResultResumed}, nil
	case "restart":
		return KeepaliveEffect{Result: KeepaliveResultRestarted}, nil
	case "crash-loop":
		return KeepaliveEffect{Result: KeepaliveResultCapped, Reason: "crash-loop"}, nil
	case "claimed":
		return KeepaliveEffect{Result: KeepaliveResultSkipped, Reason: "claimed"}, nil
	case "stand-down":
		// The flag was cleared and a marker exists: the manager is RETIRED and never
		// resumed. `kept` is right — there is nothing to act on — but the reason
		// names it, so a retired manager is distinguishable from an armed one.
		return KeepaliveEffect{Result: KeepaliveResultKept, Reason: "stand-down"}, nil
	case "leave", "jump":
		return KeepaliveEffect{Result: KeepaliveResultKept, Reason: verdict}, nil
	case "cannot-tell":
		return KeepaliveEffect{Result: KeepaliveResultSkipped, Reason: "cannot-tell"}, nil
	case "open", "open-unflagged":
		return KeepaliveEffect{Result: KeepaliveResultSkipped, Reason: "open-leg"}, nil
	case "claim-taken":
		// The claim was WON, so the act follows; the caller replaces this effect
		// with the one its act reports. Reaching the end of a tick still holding
		// this value means the act never ran, which is an error rather than a
		// quiet `none`.
		return KeepaliveEffect{Result: KeepaliveResultSkipped, Reason: "claim-taken-act-missing"}, nil
	case "":
		return KeepaliveEffect{}, errors.New(ctx, "keepalive verdict is empty")
	default:
		return KeepaliveEffect{}, errors.Errorf(ctx, "unknown keepalive verdict %q", verdict)
	}
}
