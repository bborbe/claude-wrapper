// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import stderrors "errors"

// Sentinel errors the handlers map onto HTTP status codes.
//
// stdlib errors.New rather than libborbe's context-taking constructor: these
// are package-level values created once at init, where there is no request to
// take a context from. Call sites wrap them with libborbe's errors.Wrapf and
// the real ctx, which is where the context belongs.
var (
	// ErrSessionNotFound is returned for an id the supervisor does not hold.
	ErrSessionNotFound = stderrors.New("session not found")
	// ErrSessionExited is returned when the child has already left.
	ErrSessionExited = stderrors.New("session exited")
	// ErrSessionLive is returned when a resume targets a conversation whose
	// child is still running, or is already being resumed.
	ErrSessionLive = stderrors.New("session still running")
	// ErrAnswerUnsupported is returned when a session declares no answer control.
	ErrAnswerUnsupported = stderrors.New("answer not supported")
	// ErrAnswerShapeMismatch is returned when an answer's shape does not match
	// the pending request it is aimed at — a selection aimed at a permission
	// request, or a behavior aimed at a question. It is deliberately distinct
	// from ErrAnswerUnsupported, which stays the unknown-or-already-answered
	// case, so a caller can tell "wrong answer for this request" (422) from
	// "no such request" (409).
	ErrAnswerShapeMismatch = stderrors.New("answer shape mismatch")
	// ErrInvalidSpawnOptions is returned when the caller's spawn options cannot
	// be expressed as a child command, so the request is refused before any child
	// or temp file exists.
	ErrInvalidSpawnOptions = stderrors.New("invalid spawn options")
	// ErrInvalidSessionChange is returned when a change to a running session is
	// refused before anything is written — an unknown permission mode, an empty
	// or malformed model name.
	//
	// It is a separate sentinel from ErrInvalidSpawnOptions on purpose: the two
	// describe different moments, and a caller that reads "invalid spawn
	// options" from a change call is being told about a spawn that never
	// happened.
	ErrInvalidSessionChange = stderrors.New("invalid session change")
	// ErrUnknownMacro is returned for a macro name the wrapper does not define.
	ErrUnknownMacro = stderrors.New("unknown macro")
	// ErrConfirmationRequired is returned when a macro contains a
	// production-touching step and the caller supplied no confirmation.
	ErrConfirmationRequired = stderrors.New("confirmation required")
	// ErrSessionNotIdle is returned when a sequence is asked to start on a
	// child that is mid-turn, where the in-flight turn's close would be read as
	// the first step's.
	ErrSessionNotIdle = stderrors.New("session not idle")
	// ErrMissingSessionHeader is returned when a roster call arrives without the
	// X-Claude-Code-Session-Id header.
	//
	// It is never defaulted to a placeholder: every roster answer is attributed to
	// the calling session, so an un-attributed call would be recorded against a
	// session that does not exist — a ledger row that reads as evidence and names
	// nobody.
	ErrMissingSessionHeader = stderrors.New("missing X-Claude-Code-Session-Id header")
	// ErrAgentNotOwned is returned when stop_agent names a session this daemon
	// did not spawn.
	//
	// The wrapper holds the child's stdin, which is what lets it answer and stop
	// it; a session spawned elsewhere has no such handle here, so a stop would be
	// a signal sent by a process that does not own the process it is killing.
	ErrAgentNotOwned = stderrors.New("agent is not a child of this wrapper")
	// ErrAgentNotFound is returned for an agent id no roster row carries.
	ErrAgentNotFound = stderrors.New("agent not found")
	// ErrStreamGap is returned when a stream resume point is no longer retained.
	//
	// It exists so a reconnect cannot silently substitute a different point: a
	// client that asked to continue from event 120 and was quietly handed the last
	// 4096 events would see a stream with an invisible hole, which is the failure
	// this whole stream exists to make impossible.
	ErrStreamGap = stderrors.New("stream resume point is no longer retained")
	// ErrUnknownRole is returned when a spawn names a role that is not
	// manager, agent or human.
	//
	// It refuses rather than defaulting: a worker opened in the wrong window with
	// the wrong colour is discovered only by noticing it, so a guess here trades a
	// refusal the caller can read for a defect they must spot.
	ErrUnknownRole = stderrors.New("unknown role")
	// ErrUnknownVault is returned when a spawn names a vault the vault-cli config
	// does not list, or names neither a vault nor a cwd.
	ErrUnknownVault = stderrors.New("unknown vault")
	// ErrNoLauncher is returned when a vault or cwd resolves to no launcher.
	//
	// Never defaulted to the bare `claude` binary: that routes around the router
	// env, the MCP config and the model selection every cc-* script carries.
	ErrNoLauncher = stderrors.New("no launcher")
)
