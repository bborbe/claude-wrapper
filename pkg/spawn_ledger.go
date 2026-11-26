// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"log"
	"strconv"
	"strings"
)

// resumeSpawnPatch is the field set a RESUME or a reopen writes over a row that
// already exists.
//
// ⚠️ **It never names `spawner_session_id` or `mode_source`.** Both belong to the
// original spawn: the spawn edge is what lets a manager own the worker's parks,
// and the provenance records which path opened the session. A full-row POST on a
// resume overwrote both — measured 2026-11-26, 320 of 2749 sessions in the
// wrapper log carried a non-empty spawner followed by an empty POST, and the
// live instance was `36283b05`, whose parks no manager could answer.
//
// Only the fields the caller actually names are included: a keep-alive resume
// carries nothing but the session id, and writing "" over a recorded label or
// role would be the same field loss this patch exists to stop.
func resumeSpawnPatch(label, cwd, role, resumedFrom string) LedgerPatch {
	patch := LedgerPatch{
		"mode":         json.RawMessage(strconv.Quote(SpawnModeHeadless)),
		"resumed_from": json.RawMessage(strconv.Quote(resumedFrom)),
		"status":       json.RawMessage(`"running"`),
	}
	if label != "" {
		patch["label"] = json.RawMessage(strconv.Quote(label))
	}
	if cwd != "" {
		patch["cwd"] = json.RawMessage(strconv.Quote(cwd))
	}
	if role != "" {
		patch["role"] = json.RawMessage(strconv.Quote(role))
	}
	return patch
}

// patchLedgerRow writes a resume patch and reports whether the caller is DONE
// with the row.
//
// true means handled: either the patch landed, or the store refused it and the
// failure was logged. The caller must not fall through to a POST in that case —
// a POST replaces the whole row, which is the write this patch exists to avoid.
// false means the row does not exist, which is the one case a POST is correct:
// a resume whose record was never written or has been pruned would otherwise
// leave the session unrecorded.
func patchLedgerRow(
	ctx context.Context,
	ledger LedgerReader,
	sessionID string,
	patch LedgerPatch,
) bool {
	err := ledger.Patch(ctx, sessionID, patch)
	if err == nil {
		return true
	}
	if stderrors.Is(err, ErrLedgerRowAbsent) {
		return false
	}
	log.Printf("WARNING: ledger patch for resumed session %s failed: %v", sessionID, err)
	return true
}

// recordSpawn files the ledger row a spawned child would otherwise lack.
//
// Role and ResumedFrom are recorded because the caps count BY them: a row with
// no role reads as a worker (so a headless manager would be charged to the
// worker cap and never to the manager cap), and a row with no `resumed_from`
// cannot be excluded as an auto-resume.
//
// ⚠️ **A RESUME PATCHES; ONLY A FRESH SPAWN POSTS.** The flagged tick resumes
// sessions a manager opened, and a POST there blanked the spawn edge that made
// the worker's parks answerable. See `resumeSpawnPatch`.
func (s *RosterToolServer) recordSpawn(
	ctx context.Context,
	session *Session,
	callerSessionID string,
	body spawnAgentArgs,
	modeSource string,
) {
	if s.Ledger == nil {
		return
	}
	if strings.TrimSpace(body.Resume) != "" {
		if patchLedgerRow(
			ctx,
			s.Ledger,
			session.ID,
			resumeSpawnPatch(body.Label, session.Cwd, body.Role, body.Resume),
		) {
			return
		}
	}
	if err := s.Ledger.Post(ctx, callerSessionID, LedgerRow{
		SessionID:  session.ID,
		AgentID:    session.ID,
		Label:      body.Label,
		Cwd:        session.Cwd,
		Mode:       SpawnModeHeadless,
		ModeSource: modeSource,
		// The REQUEST's role, not the session snapshot's: the row describes what was
		// asked for, and a session type that does not carry a role would otherwise
		// file a manager as a worker.
		Role:        body.Role,
		ResumedFrom: body.Resume,
		Location:    "local",
	}); err != nil {
		log.Printf("WARNING: ledger post for spawned session %s failed: %v", session.ID, err)
	}
}
