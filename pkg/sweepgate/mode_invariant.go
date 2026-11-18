// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"fmt"
	"io"
)

// Invariant prints the card payload when the invariant fires, and nothing
// otherwise. It returns 0 on EVERY path.
//
// The caller treats any non-zero exit other than its own reserved 3 as "the
// vault-side script is unavailable" and reports a broken install, so an
// unreadable subject answers "nothing" with the no-fire code rather than a
// failure. This mode never invents the consumer's 3.
func Invariant(ctx context.Context, cfg Config, stdout, stderr io.Writer) int {
	prepared, err := Prepare(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: subject unreadable/unparseable: %v\n", err)
		return ExitNoChange
	}
	// The basis is announced on EVERY run, armed or not: a correct run that prints
	// nothing is uncitable. stderr is the right channel; stdout belongs to the caller.
	fmt.Fprintf(stderr, "park: %s\n", prepared.ParkBasis)

	payload, fires, err := InvariantPayload(ctx, cfg, prepared, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: could not build invariant card: %v\n", err)
		return ExitNoChange
	}
	if fires && payload != nil {
		fmt.Fprintf(stdout, "%s\n", payload)
	}
	return ExitNoChange
}
