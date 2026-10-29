// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/claude-wrapper/pkg"
)

// Healthz reports process liveness. It touches no child.
func Healthz() http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		writeJSON(resp, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// Sessions serves the collection: GET lists, POST spawns.
func Sessions(supervisor pkg.Supervisor) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		switch req.Method {
		case http.MethodGet:
			sessions, err := supervisor.List(ctx)
			if err != nil {
				writeError(resp, http.StatusInternalServerError, err)
				return
			}
			writeJSON(resp, http.StatusOK, sessions)
		case http.MethodPost:
			var options pkg.SpawnOptions
			if err := json.NewDecoder(req.Body).Decode(&options); err != nil {
				writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
				return
			}
			session, err := supervisor.Spawn(ctx, options)
			if err != nil {
				// statusFor, not a hardcoded 500: a refused option set is the
				// caller's error and must read as one, or a spawn that never
				// started looks like a server fault.
				writeError(resp, statusFor(err), err)
				return
			}
			writeJSON(resp, http.StatusCreated, session)
		default:
			writeError(resp, http.StatusMethodNotAllowed,
				errors.Errorf(ctx, "method %q not allowed", req.Method))
		}
	})
}

// Session serves one child: GET, DELETE, and the POST …/answer, …/message,
// …/question and …/commands sub-resources.
func Session(
	supervisor pkg.Supervisor,
	sequencer pkg.Sequencer,
	locks *pkg.SessionLocks,
) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		rest := strings.TrimPrefix(req.URL.Path, "/api/1.0/sessions/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		id := parts[0]
		if id == "" {
			writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "session id missing"))
			return
		}
		action := ""
		requestID := ""
		if len(parts) > 1 {
			action = parts[1]
		}
		if len(parts) > 2 {
			requestID = parts[2]
		}
		switch action {
		case "answer":
			handleAnswer(ctx, resp, req, supervisor, id)
		case "message":
			handleMessage(ctx, resp, req, supervisor, locks, id)
		case "question":
			handleQuestion(ctx, resp, req, supervisor, id, requestID)
		case "events":
			handleEvents(ctx, resp, req, supervisor, id)
		case "commands":
			handleCommands(ctx, resp, req, sequencer, id)
		case "model":
			handleModel(ctx, resp, req, supervisor, sequencer, id)
		case "permission-mode":
			handlePermissionMode(ctx, resp, req, supervisor, locks, id)
		default:
			handleSession(ctx, resp, req, supervisor, id)
		}
	})
}

type messageRequest struct {
	Text string `json:"text"`
}

func handleMessage(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	locks *pkg.SessionLocks,
	id string,
) {
	if req.Method != http.MethodPost {
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return
	}
	var body messageRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
		return
	}
	if body.Text == "" {
		writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "text is required"))
		return
	}
	// The same lock a sequence holds: a turn written between two steps would
	// have its result read as the current step's close, firing the next step
	// one turn early. Fail fast rather than queue — waiting out a run would
	// outlast the server's write deadline, delivering the turn with no way to
	// report it.
	release, ok := locks.TryAcquire(id)
	if !ok {
		writeError(resp, http.StatusConflict,
			errors.Errorf(ctx, "session %q is running a sequence", id))
		return
	}
	defer release()
	if err := supervisor.Send(ctx, id, body.Text); err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	writeJSON(resp, http.StatusAccepted, map[string]string{"status": "sent"})
}

type commandsRequest struct {
	Macro   string `json:"macro"`
	Confirm string `json:"confirm"`
}

// handleModel changes the model of a running session.
//
// The change is the child's own `/model` command followed by one probe turn,
// run through the same sequencer every other multi-step write uses: the
// sequencer holds the session lock, refuses a child that is mid-turn, and
// advances on the child's own result events. The reported model is read from
// the session afterwards — that is, from the probe turn's assistant event —
// never from the request body, which would report the caller back to itself.
func handleModel(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	sequencer pkg.Sequencer,
	id string,
) {
	if req.Method != http.MethodPost {
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
		return
	}
	if err := pkg.ValidateModelName(ctx, body.Model); err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	if sequencer == nil {
		writeError(resp, http.StatusServiceUnavailable,
			errors.Errorf(ctx, "no sequencer is wired"))
		return
	}
	// A model change runs a real probe turn, so its response can legitimately
	// outlive the server's 60 s WriteTimeout — the same reason handleCommands
	// clears it. Best effort: a recorder under test does not implement
	// deadlines, and refusing the change over that would make the route
	// untestable for no gain.
	if err := clearWriteDeadline(ctx, resp); err != nil {
		writeError(resp, http.StatusInternalServerError, err)
		return
	}
	// Read before the run and after it. Both reads sit outside the sequencer's
	// lock, so two model changes racing on one session can report each other's
	// served model: the WRITES are serialized, the reported fields are
	// best-effort. A single caller — the documented threat model — never sees
	// it, and closing it would need the sequencer to hand back the state it
	// observed, which it does not do.
	before, err := supervisor.Get(ctx, id)
	if err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	result, runErr := sequencer.Run(ctx, id, pkg.ModelChangeMacro(body.Model))
	if runErr != nil && result == nil {
		writeError(resp, statusFor(runErr), runErr)
		return
	}
	after, err := supervisor.Get(ctx, id)
	if err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	change := pkg.ModelChange{
		Requested:     body.Model,
		PreviousModel: before.Model,
		ServedModel:   after.Model,
		Changed:       after.Model != before.Model,
	}
	if runErr != nil {
		// The command landed but the probe did not, so the change is applied and
		// unverified. Reported rather than swallowed: an error alone would read
		// as "nothing happened", which is the one thing this is not.
		change.Partial = true
		change.Steps = result.Steps
		writeJSON(resp, http.StatusOK, change)
		return
	}
	writeJSON(resp, http.StatusOK, change)
}

// handlePermissionMode changes the permission mode of a running session.
//
// It is the one write that is not a turn: the mode travels as the SDK's own
// control request, so it takes the session lock but no sequencer. The reported
// mode is the one the child answers with, not the one the caller sent.
func handlePermissionMode(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	locks *pkg.SessionLocks,
	id string,
) {
	if req.Method != http.MethodPost {
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
		return
	}
	if body.Mode == "" {
		writeError(resp, http.StatusBadRequest, errors.Errorf(ctx, "mode is required"))
		return
	}
	// The same lock a sequence holds, and fail fast rather than queue: a
	// compaction can hold it for minutes, which outlasts the server's write
	// deadline — the response would be truncated after the mode had already
	// changed. handleMessage takes the same choice for the same reason.
	release, ok := locks.TryAcquire(id)
	if !ok {
		writeError(resp, http.StatusConflict,
			errors.Errorf(ctx, "session %q is running a sequence", id))
		return
	}
	defer release()
	reported, err := supervisor.SetPermissionMode(ctx, id, body.Mode)
	if err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	writeJSON(resp, http.StatusOK, map[string]string{
		"status":          "changed",
		"permission_mode": reported,
	})
}

// handleCommands runs a named macro against one child.
//
// It blocks until the run finishes and answers with every step, so a caller
// cannot start a sequence and walk away: the response is the only report, and
// a run that stopped early names the steps it never reached. The write
// deadline is cleared for the duration because a compaction takes minutes,
// far past the server's 60-second default.
func handleCommands(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	sequencer pkg.Sequencer,
	id string,
) {
	if req.Method != http.MethodPost {
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return
	}
	var body commandsRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
		return
	}
	macro, err := pkg.LookupMacro(body.Macro)
	if err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	if macro.RequiresConfirmation() && body.Confirm == "" {
		writeError(resp, http.StatusConflict, errors.Wrapf(
			ctx, pkg.ErrConfirmationRequired, "macro %q needs a confirmation", macro.Name))
		return
	}
	if sequencer == nil {
		writeError(resp, http.StatusServiceUnavailable,
			errors.Errorf(ctx, "no sequencer is wired"))
		return
	}
	// Best effort: a recorder under test does not implement deadlines, and
	// refusing the run over an unsupported write deadline would make the route
	// untestable for no gain.
	if err := clearWriteDeadline(ctx, resp); err != nil {
		writeError(resp, http.StatusInternalServerError, err)
		return
	}
	result, err := sequencer.Run(ctx, id, macro)
	if err != nil && result == nil {
		writeError(resp, statusFor(err), err)
		return
	}
	writeJSON(resp, http.StatusOK, result)
}

// askUserQuestionTool is the child-side tool name whose answer is a selection
// rather than a verdict. It arrives through the same permission_prompt channel
// as every other gate, so the tool name is the only thing that tells the two
// shapes apart on registration.
const askUserQuestionTool = "AskUserQuestion"

type questionRequest struct {
	RequestID string               `json:"request_id"`
	Kind      string               `json:"kind"`
	ToolName  string               `json:"tool_name"`
	ToolUseID string               `json:"tool_use_id"`
	Input     map[string]any       `json:"input"`
	Questions []pkg.QuestionPrompt `json:"questions"`
}

// answerKindFor resolves the kind a registration describes.
//
// The caller states it when it can; otherwise the tool name decides, because
// AskUserQuestion is the only tool whose answer is a selection. A stated kind is
// validated: an out-of-set value would create a request no answer shape can ever
// match, so it would 422 forever while the child sat until its hold expired.
func answerKindFor(ctx context.Context, kind string, toolName string) (pkg.AnswerKind, error) {
	if kind != "" {
		stated := pkg.AnswerKind(kind)
		if err := stated.Validate(); err != nil {
			return "", errors.Wrapf(ctx, err, "invalid kind %q", kind)
		}
		return stated, nil
	}
	if toolName == askUserQuestionTool {
		return pkg.AnswerKindQuestion, nil
	}
	return pkg.AnswerKindPermission, nil
}

// handleQuestion is the two halves the MCP tool needs: POST registers a
// permission request, GET polls for its answer. Polling rather than long-poll
// keeps one pending prompt from holding a server goroutine.
func handleQuestion(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	id string,
	requestID string,
) {
	switch req.Method {
	case http.MethodPost:
		var body questionRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
			return
		}
		kind, kindErr := answerKindFor(ctx, body.Kind, body.ToolName)
		if kindErr != nil {
			writeError(resp, http.StatusBadRequest, kindErr)
			return
		}
		question := pkg.Question{
			RequestID: body.RequestID,
			Kind:      kind,
			ToolName:  body.ToolName,
			ToolUseID: body.ToolUseID,
			Input:     body.Input,
			Questions: body.Questions,
		}
		if err := supervisor.RegisterQuestion(ctx, id, question); err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		writeJSON(resp, http.StatusAccepted, question)
	case http.MethodGet:
		pending, err := supervisor.PollAnswer(ctx, requestID)
		if err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		writeJSON(resp, http.StatusOK, map[string]any{
			"answered": pending.Answered,
			"question": pending.Question,
			"kind":     pending.Answer.Kind,
			"behavior": pending.Answer.Behavior,
			"answers":  pending.Answer.Answers,
		})
	case http.MethodDelete:
		// The child's hold expired unanswered, so nothing is waiting on this
		// request any more. Without this the session keeps reporting it as
		// pending, and every later read says "parked" forever.
		//
		// An empty id is refused rather than treated as a no-op: answering 204
		// would report success for a request that was never dropped.
		if requestID == "" {
			writeError(resp, http.StatusBadRequest,
				errors.Errorf(ctx, "request_id is required to forget a question"))
			return
		}
		if err := supervisor.ForgetQuestion(ctx, id, requestID); err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		resp.WriteHeader(http.StatusNoContent)
	default:
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
	}
}

func handleSession(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	id string,
) {
	switch req.Method {
	case http.MethodGet:
		session, err := supervisor.Get(ctx, id)
		if err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		writeJSON(resp, http.StatusOK, session)
	case http.MethodDelete:
		if err := supervisor.Stop(ctx, id); err != nil {
			writeError(resp, statusFor(err), err)
			return
		}
		resp.WriteHeader(http.StatusNoContent)
	default:
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
	}
}

// clearWriteDeadline removes the server's WriteTimeout for this one response.
//
// `main.go` sets `WriteTimeout: 60s`, and Go applies it as a deadline on the connection at
// the moment the request is read. Any route whose response legitimately outlives that — a
// macro run, an MCP turn, an event stream — must clear it, or its response is cut at the
// first write after the deadline, which a client reports as a truncated body rather than as
// a server error.
//
// Best effort: a recorder under test does not implement deadlines, and refusing the request
// over an unsupported write deadline would make those routes untestable for no gain.
func clearWriteDeadline(ctx context.Context, resp http.ResponseWriter) error {
	if err := http.NewResponseController(resp).SetWriteDeadline(time.Time{}); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		return errors.Wrapf(ctx, err, "clear write deadline failed")
	}
	return nil
}

// handleEvents serves the child's event stream as server-sent events.
//
// The replay is written before the live channel is drained, so a subscriber
// that arrives mid-turn sees the turn it missed rather than joining at an
// arbitrary point. A closed channel ends the response: the child exited, or the
// subscriber fell so far behind that the stream stopped being a faithful
// account of it — both are reported as `event: end` rather than as silence.
func handleEvents(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	id string,
) {
	if req.Method != http.MethodGet {
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return
	}
	flusher, ok := resp.(http.Flusher)
	if !ok {
		writeError(resp, http.StatusInternalServerError,
			errors.Errorf(ctx, "streaming unsupported"))
		return
	}
	replay, live, cancel, err := supervisor.Subscribe(req.Context(), id)
	if err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	defer cancel()

	// ⚠️ An SSE response outlives the server's WriteTimeout by design, and it must say so
	// explicitly. `main.go` sets `WriteTimeout: 60s`, which Go applies as a deadline on the
	// connection at the moment the request is read — and a deadline does not close the
	// connection, it makes the NEXT WRITE fail. A child that parks on a permission prompt
	// writes nothing for minutes, so the stream looks healthy right up to the first event
	// after the 60s mark; that write then fails, this handler returns, and the subscriber's
	// body is cut mid-stream. A Node client surfaces that as `TypeError: terminated`, which
	// is how a perfectly healthy child gets recorded as a dead worker.
	if err := clearWriteDeadline(ctx, resp); err != nil {
		writeError(resp, http.StatusInternalServerError, err)
		return
	}

	resp.Header().Set("Content-Type", "text/event-stream")
	resp.Header().Set("Cache-Control", "no-cache")
	resp.Header().Set("Connection", "keep-alive")
	resp.WriteHeader(http.StatusOK)
	// Flush the headers now, before any frame. Without this a subscriber to a child with
	// an empty replay receives no byte at all until the child next speaks, so its HTTP
	// client cannot resolve the response and "connected and quiet" is indistinguishable
	// from "not connected yet".
	flusher.Flush()

	writeFrame := func(event pkg.Event) bool {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return true
		}
		if _, writeErr := fmt.Fprintf(resp, "data: %s\n\n", encoded); writeErr != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for _, event := range replay {
		if !writeFrame(event) {
			return
		}
	}
	for {
		select {
		case <-req.Context().Done():
			return
		case event, open := <-live:
			if !open {
				fmt.Fprint(resp, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			if !writeFrame(event) {
				return
			}
		}
	}
}

type answerRequest struct {
	RequestID string            `json:"request_id"`
	Behavior  string            `json:"behavior"`
	Answers   map[string]string `json:"answers"`
}

// answerFrom reads the caller's body as one of the two answer shapes.
//
// The field the caller filled decides the kind; Resolve then refuses a kind that
// does not match the pending request, so a selection aimed at a permission
// request is a 422 rather than a silent default to allow.
func answerFrom(body answerRequest) pkg.Answer {
	if len(body.Answers) > 0 {
		return pkg.Answer{Kind: pkg.AnswerKindQuestion, Answers: body.Answers}
	}
	behavior := body.Behavior
	if behavior == "" {
		behavior = "allow"
	}
	return pkg.Answer{Kind: pkg.AnswerKindPermission, Behavior: behavior}
}

func handleAnswer(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	supervisor pkg.Supervisor,
	id string,
) {
	if req.Method != http.MethodPost {
		writeError(resp, http.StatusMethodNotAllowed,
			errors.Errorf(ctx, "method %q not allowed", req.Method))
		return
	}
	var body answerRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(resp, http.StatusBadRequest, errors.Wrapf(ctx, err, "decode body failed"))
		return
	}
	if err := supervisor.Answer(ctx, id, body.RequestID, answerFrom(body)); err != nil {
		writeError(resp, statusFor(err), err)
		return
	}
	writeJSON(resp, http.StatusOK, map[string]string{"status": "answered"})
}

// statusFor maps the supervisor's sentinels onto the documented status contract.
func statusFor(err error) int {
	switch {
	case errors.Is(err, pkg.ErrSessionNotFound):
		return http.StatusNotFound
	case errors.Is(err, pkg.ErrSessionExited):
		return http.StatusGone
	case errors.Is(err, pkg.ErrAnswerUnsupported):
		return http.StatusConflict
	case errors.Is(err, pkg.ErrAnswerShapeMismatch):
		return http.StatusUnprocessableEntity
	case errors.Is(err, pkg.ErrInvalidSpawnOptions):
		return http.StatusBadRequest
	case errors.Is(err, pkg.ErrInvalidSessionChange):
		return http.StatusBadRequest
	case errors.Is(err, pkg.ErrUnknownMacro):
		return http.StatusBadRequest
	case errors.Is(err, pkg.ErrConfirmationRequired):
		return http.StatusConflict
	case errors.Is(err, pkg.ErrSessionNotIdle):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func writeError(resp http.ResponseWriter, status int, err error) {
	writeJSON(resp, status, map[string]any{
		"error": map[string]string{
			"code":    http.StatusText(status),
			"message": err.Error(),
		},
	})
}

func writeJSON(resp http.ResponseWriter, status int, payload any) {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(status)
	_ = json.NewEncoder(resp).Encode(payload)
}
