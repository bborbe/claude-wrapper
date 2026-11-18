// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"fmt"
	"io"
)

// Render prints the frame and returns the verdict its change flag implies.
//
// It writes no tick file, no card and no state: rendering is a read. An
// unreadable subject fails open on stderr with nothing on stdout.
func Render(ctx context.Context, cfg Config, stdout, stderr io.Writer) int {
	prepared, ok := prepareForVerdict(ctx, cfg, stderr)
	if !ok {
		return ExitFailed
	}
	stored, failReason, ok := loadStoredDigest(ctx, cfg, stderr)
	if !ok {
		return ExitFailed
	}
	changed := failReason != "" || stored != prepared.Digest

	frame, err := Frame(ctx, cfg, prepared, changed)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: could not render frame (%v)\n", err)
		return ExitFailed
	}
	fmt.Fprintf(stdout, "%s\n", frame)

	if changed {
		return ExitChange
	}
	return ExitNoChange
}
