// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"fmt"
	"io"
)

// Check reports whether the subject's digest moved, writing nothing but its
// verdict.
//
// It writes no tick file, no card and no state: the caller wants a cheap answer,
// not a side effect. An unreadable subject fails open on stderr and never emits
// a CHANGE line.
func Check(ctx context.Context, cfg Config, stdout, stderr io.Writer) int {
	prepared, ok := prepareForVerdict(ctx, cfg, stderr)
	if !ok {
		return ExitFailed
	}
	stored, failReason, ok := loadStoredDigest(ctx, cfg, stderr)
	if !ok {
		return ExitFailed
	}
	if failReason != "" || stored != prepared.Digest {
		reason := failReason
		if reason == "" {
			reason = "digest differs"
		}
		fmt.Fprintf(stdout, "CHANGE %s\n", reason)
		return ExitChange
	}
	fmt.Fprintln(stdout, "NOCHANGE")
	return ExitNoChange
}

// prepareForVerdict runs the pipeline and announces the park basis.
//
// A false return means the subject could not be prepared; the fail-open line has
// already been written to stderr and stdout must stay untouched.
func prepareForVerdict(ctx context.Context, cfg Config, stderr io.Writer) (Prepared, bool) {
	prepared, err := Prepare(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: subject unreadable/unparseable: %v\n", err)
		return Prepared{}, false
	}
	fmt.Fprintf(stderr, "park: %s\n", prepared.ParkBasis)
	return prepared, true
}

// loadStoredDigest reads the recorded digest and its failure reason.
//
// A false return means the state path itself could not be resolved; that is a
// fail-open line on stderr, not a verdict.
func loadStoredDigest(
	ctx context.Context,
	cfg Config,
	stderr io.Writer,
) (digest, failReason string, ok bool) {
	statePath, err := StatePath(ctx, cfg, cfg.Subject)
	if err != nil {
		fmt.Fprintf(stderr, "fail-open: subject unreadable/unparseable: %v\n", err)
		return "", "", false
	}
	digest, failReason, _ = LoadStored(ctx, statePath)
	return digest, failReason, true
}
