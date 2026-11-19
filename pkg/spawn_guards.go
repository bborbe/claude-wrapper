// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// enforceSpawnCaps runs the worker cap, the manager cap for a manager, then
// reserves a start slot — the order and the reasons are enforceCaps' own.
// manager is nil for a non-manager spawn; for a manager it returns the
// manager kind and the vault roots the live count classifies against, and is
// called only after the worker cap passed (one vault read, as before).
func enforceSpawnCaps(
	ctx context.Context,
	caps SpawnCaps,
	mode string,
	manager func(ctx context.Context) (kind string, roots []string),
	operatorNamed bool,
) error {
	if caps == nil {
		return nil
	}
	// A manager answers to the manager axes, never to a WORKER mode sub-cap: the
	// headless/interactive pair bounds the worker population, and a manager counted
	// against it would be refused by a cap over a population it is not in. The
	// worker total and the session backstop still apply to it.
	workerMode := mode
	if manager != nil {
		workerMode = ""
	}
	if err := caps.CheckWorker(ctx, workerMode, operatorNamed); err != nil {
		return err
	}
	if manager != nil {
		kind, roots := manager(ctx)
		if err := caps.CheckManager(ctx, kind, roots, operatorNamed); err != nil {
			return err
		}
	}
	// Last, as the node has it: a spawn refused by a cap above must not spend a
	// start slot the next legitimate spawn then waits for.
	return caps.ReserveStart(ctx)
}

// refuseTerminalTask refuses a resume of session whose task ref is finished.
// nil ref, or a status terminalTaskStatus does not call terminal, is allowed.
func refuseTerminalTask(ctx context.Context, session string, ref *TaskRef) error {
	if ref == nil || !terminalTaskStatus(ref.Status) {
		return nil
	}
	return errors.Wrapf(
		ctx,
		ErrTaskTerminal,
		"resume of session %s refused: its task %q in vault %q is %s, and a finished "+
			"task must not be handed a live worker again — closing is the fleet's only "+
			"way to reclaim a spawn slot",
		session,
		ref.Title,
		ref.Vault,
		ref.Status,
	)
}
