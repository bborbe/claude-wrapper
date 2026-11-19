// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// KeepaliveScriptRunner runs one script and returns its stdout and exit code,
// the shape `runScript` already has. It is a parameter rather than a package
// function so a test can drive the adapter without a script on disk.
//
// ⚠️ It is named for this file because `ScriptRunner` is already taken by the
// open-tab seam in this package, and two types of one name would not compile.
type KeepaliveScriptRunner func(ctx context.Context, args ...string) (string, int, error)

// NewKeepaliveClaim builds the resume-claim adapter over `topic-keepalive.py`.
//
// ⚠️ **It shells out rather than creating the claim file itself.** The claim is
// an `O_EXCL` file whose name is `<state_path>.<_claim_token(sid)>.claim`, and the
// token derivation is the script's. Re-deriving it in Go would be a second
// definition of the one thing that decides whether two writers collide — and a
// second definition that is *subtly* different fails exactly the way the guard
// exists to prevent: both writers win.
//
// The two words the script answers with are opposite-handed: `claim-taken` means
// THIS writer won and may act; `claimed` means another writer holds it.
func NewKeepaliveClaim(run KeepaliveScriptRunner) func(ctx context.Context, sessionID string) (bool, error) {
	return func(ctx context.Context, sessionID string) (bool, error) {
		if run == nil {
			return false, errors.New(ctx, "keepalive claim has no script runner")
		}
		if sessionID == "" {
			return false, errors.New(ctx, "keepalive claim needs a session id")
		}
		output, code, err := run(ctx, "--claim-resume", sessionID)
		if err != nil {
			return false, errors.Wrapf(ctx, err, "claim %s failed", sessionID)
		}
		verdict, parseErr := ParseTopicKeepaliveVerdict(ctx, output)
		if parseErr != nil {
			return false, errors.Wrapf(ctx, parseErr, "claim %s printed no verdict (exit %d)", sessionID, code)
		}
		switch verdict {
		case "claim-taken":
			return true, nil
		case "claimed":
			return false, nil
		default:
			return false, errors.Errorf(ctx, "claim %s answered %q (exit %d)", sessionID, verdict, code)
		}
	}
}
