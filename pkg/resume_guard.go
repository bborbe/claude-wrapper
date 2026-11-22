// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"log"
	"strings"

	"github.com/bborbe/errors"
)

// refuseTerminalResume refuses a resume whose task is already finished.
//
// The resume that fires in production is `spawn_agent(resume=<id>)` with no
// `task` and no `vault` — the keepalive actor's path-A call — so the session id
// is the only identity available, and the task is resolved by the
// `claude_session_id:` it carries. A caller that DOES name a task is checked on
// that too: a task file may predate the id being written, and a completed task
// named by the caller is the same defect whether or not the id is recorded yet.
//
// An unresolvable id is ALLOWED with one log line. No task claims the session,
// so there is nothing to prove terminal, and refusing would break every existing
// path-A resume; the log line is what keeps that fail-open observable.
//
// A guard that could not RUN is different, and is REFUSED with
// ResumeCodePathBroken: it used to be allowed too, which is how a broken vault
// read surfaced as an opaque client timeout instead of a named cause.
func (s *RosterToolServer) refuseTerminalResume(
	ctx context.Context,
	body spawnAgentArgs,
	cwd string,
) error {
	session := strings.TrimSpace(body.Resume)
	if session == "" {
		return nil
	}
	ref, err := s.taskForResume(ctx, body, cwd)
	if err != nil {
		return resumeFailure(ResumeCodePathBroken, err)
	}
	if ref == nil {
		// One line, and it asserts no CAUSE: the vault list may have been
		// unreadable or simply held no match, and a message naming the wrong one
		// would send a reader looking in the wrong place.
		log.Printf(
			"resume guard: no task could be resolved for session %s — allowing the "+
				"resume unresolved",
			session,
		)
		return nil
	}
	return refuseTerminalTask(ctx, session, ref)
}

// taskForResume resolves the task a resumed session belongs to, or nil.
//
// The session id is asked first and is authoritative: it names the conversation
// being continued. The named task is a SECOND signal for a caller that supplies
// one — a task file may predate its `claude_session_id:` being written — so both
// are consulted rather than one.
//
// It returns an error ONLY when the guard could not run — the vault list was
// unreadable. "No task claims this session" is a nil ref and a nil error, which
// the caller allows; the two must not be folded together, or a broken path reads
// as an ordinary unresolvable id.
func (s *RosterToolServer) taskForResume(
	ctx context.Context,
	body spawnAgentArgs,
	cwd string,
) (*TaskRef, error) {
	var vaults []Vault
	if s.Vaults != nil {
		listed, err := s.Vaults.List(ctx)
		if err != nil {
			log.Printf(
				"resume guard: the vault list is unreadable (%v) — refusing session %s "+
					"with %s rather than allowing it unresolved",
				err,
				body.Resume,
				ResumeCodePathBroken,
			)
			return nil, errors.Wrapf(
				ctx, err,
				"the vault list could not be read, so the resume guard could not "+
					"resolve the session's task",
			)
		}
		vaults = listed
	}
	if ref := TaskBySessionID(ctx, vaults, body.Resume); ref != nil {
		return ref, nil
	}
	if strings.TrimSpace(body.Task) == "" {
		return nil, nil
	}
	vault, err := s.vaultFor(ctx, body, cwd)
	if err != nil || vault == nil {
		return nil, nil
	}
	status := TaskStatus(vault.Path, body.Task)
	if status == "" {
		return nil, nil
	}
	return &TaskRef{Title: body.Task, Status: status, Vault: vault.Name}, nil
}
