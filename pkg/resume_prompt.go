// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
)

// refusePromptlessResume refuses a headless resume that carries no opening turn.
//
// A resume with no prompt starts a child with nothing to do: it closes one empty
// turn, the reaper stops it, and the caller is told the resume started. Measured
// 2026-11-16 — six of twelve flagged resumes returned `state: starting`, each
// with a `turn closed with nothing pending — stopping the session` line and an
// unchanged task file, so the resume looked delivered and did no work.
//
// The refusal is on the HEADLESS path only. A promptless interactive resume is
// legitimate — that is how a human reopens a tab to keep working — so the
// condition is this function's caller, never a rule about resumes in general.
func refusePromptlessResume(ctx context.Context, body spawnAgentArgs) error {
	if strings.TrimSpace(body.Resume) == "" {
		return nil
	}
	if strings.TrimSpace(body.Prompt) != "" {
		return nil
	}
	return errors.Wrapf(
		ctx,
		ErrResumeWithoutPrompt,
		"resume of session %s refused: a headless resume carries no opening turn, so the "+
			"child would close one empty turn and be reaped without doing any work — pass "+
			"`prompt` naming the work the resumed session should do",
		body.Resume,
	)
}
