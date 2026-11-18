// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

// The four exit codes are a caller contract, not an implementation detail.
//
// The plugin's step file reads a non-zero, non-`3` exit as "the vault-side script
// is unavailable", and `3` belongs to the `--invariant` consumer, so these values
// are not free to change. Every later mode returns one of them by name; no mode
// may return a bare integer.
const (
	// ExitNoChange is a successful run that found nothing to report.
	ExitNoChange = 0
	// ExitChange is a successful run that found a change.
	ExitChange = 10
	// ExitUsage is a caller error, and the code a failed run returns.
	ExitUsage = 2
	// ExitFailed is the reference's EXIT_FAILED — the same value as ExitUsage.
	ExitFailed = ExitUsage
)
