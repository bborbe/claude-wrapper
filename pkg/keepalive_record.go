// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// The record-resume half of the claim pair.
//
// `--claim-resume` takes the claim; `--record-resume` RELEASES it and arms the
// resume cap. A port that takes a claim and never records it leaves the cap
// unarmed, so the same session is resumed again on the next tick — forever. The
// source names that failure for the missing `--subject`: argparse exits 2 and the
// cap silently never arms.
//
// ⚠️ **The two tiers record through DIFFERENT scripts and flags.** The manager
// tier records on `topic-keepalive.py --record-resume <sid>`; the worker tier on
// `worker-keepalive.py --vault <vault> --task <task> --record-resume <id>`. One
// adapter cannot serve both, which is why there are two constructors here.

// NewKeepaliveRecordResume builds the MANAGER tier's record adapter.
//
// ⚠️ **A non-zero exit is an error, not a quiet tick.** The script refuses to
// record unless the id reads LIVE, so a refusal means the resume it is recording
// did not take — reporting that as success would arm a cap for a resume that
// never happened.
func NewKeepaliveRecordResume(run KeepaliveScriptRunner) func(ctx context.Context, sessionID string) error {
	return func(ctx context.Context, sessionID string) error {
		if run == nil {
			return errors.New(ctx, "record-resume has no script runner")
		}
		if sessionID == "" {
			return errors.New(ctx, "record-resume needs a session id")
		}
		_, code, err := run(ctx, "--record-resume", sessionID)
		if err != nil {
			return errors.Wrapf(ctx, err, "record-resume %s failed", sessionID)
		}
		if code != 0 {
			return errors.Errorf(ctx, "record-resume %s exited %d — the resume did not take, so the cap must not arm", sessionID, code)
		}
		return nil
	}
}

// NewWorkerRecordResume builds the WORKER tier's record adapter.
//
// ⚠️ **It needs the vault AND the task**, because `worker-keepalive.py` selects
// its population per task page (`--task` is `required=True`). A call missing
// either exits 2 and the worker cap silently never arms.
func NewWorkerRecordResume(run KeepaliveScriptRunner) func(ctx context.Context, vault, task, sessionID string) error {
	return func(ctx context.Context, vault, task, sessionID string) error {
		if run == nil {
			return errors.New(ctx, "worker record-resume has no script runner")
		}
		if vault == "" || task == "" || sessionID == "" {
			return errors.New(ctx, "worker record-resume needs a vault, a task and a session id")
		}
		_, code, err := run(ctx, "--vault", vault, "--task", task, "--record-resume", sessionID)
		if err != nil {
			return errors.Wrapf(ctx, err, "worker record-resume %s failed", sessionID)
		}
		if code != 0 {
			return errors.Errorf(ctx, "worker record-resume %s exited %d — the resume did not take, so the cap must not arm", sessionID, code)
		}
		return nil
	}
}
