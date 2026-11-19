// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// NewKeepaliveVerdicts builds the liveness read the dead-worker procedure
// consumes.
//
// ⚠️ **It calls `LivenessVerdicts` in process rather than fetching
// `/api/1.0/liveness`.** This daemon IS the endpoint's server: an HTTP
// round-trip would make a job depend on its own listener being up, so a tick
// during shutdown or a port bind failure would read "nothing is live" — the
// direction that hides every running session. The endpoint exists so OTHER
// consumers get one derivation; this one already holds it.
//
// The three sources are passed as functions so a caller can wire the registry
// read, the child table and the heartbeat store independently, and so a test can
// drive them without a daemon.
func NewKeepaliveVerdicts(
	registry SessionRegistry,
	children func(ctx context.Context) ([]Session, error),
	heartbeats func(ctx context.Context) ([]HeartbeatRow, error),
) func(ctx context.Context) ([]LivenessVerdict, error) {
	return func(ctx context.Context) ([]LivenessVerdict, error) {
		if registry == nil {
			return nil, errors.New(ctx, "keepalive verdicts need a session registry")
		}
		if children == nil || heartbeats == nil {
			return nil, errors.New(ctx, "keepalive verdicts need a child and heartbeat source")
		}
		childRows, err := children(ctx)
		if err != nil {
			return nil, errors.Wrap(ctx, err, "read child table failed")
		}
		heartbeatRows, err := heartbeats(ctx)
		if err != nil {
			return nil, errors.Wrap(ctx, err, "read heartbeats failed")
		}
		return LivenessVerdicts(ctx, registry, childRows, heartbeatRows)
	}
}
