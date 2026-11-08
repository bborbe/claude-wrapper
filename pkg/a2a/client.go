// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
)

// Wire constants. The values are measured rather than taken from the spec, and the
// comments say what each one repaired.
const (
	// AgentCardPath is the well-known path the card is served at
	// (`a2asrv.WellKnownAgentCardPath`).
	AgentCardPath = "/.well-known/agent-card.json"
	// SendMessageMethod is the one-shot JSON-RPC method the control plane uses.
	SendMessageMethod = "SendMessage"
	// SendStreamingMethod is the streaming spelling the WORKER plane uses.
	//
	// ⚠️ BARE, NOT THE SPEC'S `message/stream`. The v2 SDK dispatches the bare
	// names; the slash spellings survive only in the `a2acompat/a2av0` bridge a
	// v2 service does not mount, and a client using them gets `-32601 method not
	// found`.
	SendStreamingMethod = "SendStreamingMessage"
	// PushConfigMethod registers a push callback against a task.
	//
	// ⚠️ NOT `tasks/pushNotificationConfig/set`. That name belongs to a
	// different transport, where the params wrap the config in a
	// `pushNotificationConfig` object. This binding's params are flat.
	PushConfigMethod = "CreateTaskPushNotificationConfig"
	// ListPushConfigMethod reads back the push configs a worker HOLDS for a task.
	//
	// ⚠️ LIST, NOT GET, AND THAT IS NOT A PREFERENCE. The SDK's get is keyed by a
	// config id the registration never sends, so a get can never match what the
	// registration stored — only a list can answer the question this call exists
	// to answer.
	ListPushConfigMethod = "ListTaskPushNotificationConfigs"
	// UserRole is the enum string every part carries — not `"user"`.
	UserRole = "ROLE_USER"
	// TaskStateFailed is the controller's failed state.
	TaskStateFailed = "TASK_STATE_FAILED"
	// VersionHeader is the protocol-version header.
	//
	// ⚠️ AN ABSENT HEADER IS NOT "NO VERSION", IT IS `0.3`, and a service serving
	// 1.0 refuses it with `-32009 … requested "0.3", served "1.0"` INSIDE a
	// well-formed HTTP 200. So the value is read from the peer's card and never
	// guessed.
	VersionHeader = "A2A-Version"
)

// Client is the A2A wire client: the control plane, the worker turn plane and
// the push registration.
type Client struct {
	HTTP *http.Client
	// Now mints the message ids and context ids a call needs.
	NewID func() string
}

// NewClient creates a client with a bounded HTTP timeout.
func NewClient(newID func() string) *Client {
	return &Client{
		HTTP:  &http.Client{Timeout: 5 * time.Minute},
		NewID: newID,
	}
}

// ─── wire shapes ────────────────────────────────────────────────────────────

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error"`
}

// AgentCard is the part of a card this client reads: the endpoint and the
// protocol version.
//
// ⚠️ THE ENDPOINT IS `supportedInterfaces[0].url`, NOT A TOP-LEVEL `url`. A v2
// card carries no top-level `url` at all, so reading the obvious one yields
// nothing — and a fallback would turn that into a turn sent somewhere the card
// never named.
type AgentCard struct {
	SupportedInterfaces []struct {
		URL             string `json:"url"`
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"supportedInterfaces"`
}

// Endpoint returns the A2A endpoint the card advertises.
func (c *AgentCard) Endpoint(ctx context.Context) (string, error) {
	if len(c.SupportedInterfaces) == 0 {
		return "", errors.Errorf(
			ctx,
			"the Agent Card carries no `supportedInterfaces` array, so no A2A endpoint "+
				"can be read — a correct card advertises it at `supportedInterfaces[0].url`",
		)
	}
	if c.SupportedInterfaces[0].URL == "" {
		return "", errors.Errorf(
			ctx,
			"`supportedInterfaces[0]` carries no `url` — do not fall back to a top-level "+
				"`url`: a correct card does not carry one",
		)
	}
	return c.SupportedInterfaces[0].URL, nil
}

// ProtocolVersion returns the version the card advertises, or "" when it names
// none.
func (c *AgentCard) ProtocolVersion() string {
	if len(c.SupportedInterfaces) == 0 {
		return ""
	}
	return c.SupportedInterfaces[0].ProtocolVersion
}

// ─── control plane ──────────────────────────────────────────────────────────

// ControlRequest is one control-API call.
type ControlRequest struct {
	Operation string         `json:"operation"`
	Create    *CreateRequest `json:"create,omitempty"`
	Stop      *StopRequest   `json:"stop,omitempty"`
}

// CreateRequest names the task the controller creates one worker for.
type CreateRequest struct {
	Task string `json:"task"`
}

// StopRequest names the worker to stop.
type StopRequest struct {
	WorkerID string `json:"worker_id"`
}

// Worker is one worker the controller owns.
type Worker struct {
	WorkerID string `json:"worker_id"`
	Task     string `json:"task"`
	A2AURL   string `json:"a2a_url"`
	Backend  string `json:"backend"`
	State    string `json:"state"`
	PaneID   string `json:"pane_id"`
}

// ControlResponse is the controller's reply to one control call.
type ControlResponse struct {
	Operation string `json:"operation"`
	Create    *struct {
		Worker Worker `json:"worker"`
		Reused bool   `json:"reused,omitempty"`
		Token  string `json:"token"`
	} `json:"create,omitempty"`
	List *struct {
		Workers []Worker `json:"workers"`
	} `json:"list,omitempty"`
	Stop *struct {
		Worker Worker `json:"worker"`
	} `json:"stop,omitempty"`
}

// CreateWorker asks the controller for one worker on the given task.
func (c *Client) CreateWorker(
	ctx context.Context,
	cfg ControllerConfig,
	task string,
) (*ControlResponse, error) {
	return c.control(ctx, cfg, ControlRequest{
		Operation: "create",
		Create:    &CreateRequest{Task: task},
	})
}

// ListWorkers reads every worker the controller owns.
func (c *Client) ListWorkers(ctx context.Context, cfg ControllerConfig) ([]Worker, error) {
	response, err := c.control(ctx, cfg, ControlRequest{Operation: "list"})
	if err != nil {
		return nil, err
	}
	if response.List == nil {
		// gob does not transmit zero values, so an empty list round-trips as
		// `null` rather than `[]`; both mean the same fact.
		return nil, nil
	}
	return response.List.Workers, nil
}

// StopWorker asks the controller to stop and remove one worker.
func (c *Client) StopWorker(ctx context.Context, cfg ControllerConfig, workerID string) error {
	_, err := c.control(ctx, cfg, ControlRequest{
		Operation: "stop",
		Stop:      &StopRequest{WorkerID: workerID},
	})
	return err
}

// control performs one one-shot control call.
func (c *Client) control(
	ctx context.Context,
	cfg ControllerConfig,
	request ControlRequest,
) (*ControlResponse, error) {
	version := c.controllerVersion(ctx, cfg)
	contextID := c.newID()

	body := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  SendMessageMethod,
		Params: map[string]any{
			"message": map[string]any{
				"messageId": c.newID(),
				"role":      UserRole,
				"contextId": contextID,
				// ⚠️ A DATA part, never `{text}` or `{content}`. A `content` key
				// exists only on the Go struct and never on the wire, and it is
				// refused inside a well-formed HTTP 200 envelope.
				"parts": []any{map[string]any{"data": request}},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal control request failed")
	}

	headers := map[string]string{
		"content-type":  "application/json",
		"authorization": "Bearer " + cfg.Token,
	}
	if version != "" {
		headers[VersionHeader] = version
	}
	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, cfg.URL, bytes.NewReader(encoded),
	)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "build control request failed")
	}
	for name, value := range headers {
		httpReq.Header.Set(name, value)
	}
	res, err := c.http().Do(httpReq)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "the control call to %s could not be made", redactURL(cfg.URL))
	}
	defer func() { _ = res.Body.Close() }()
	// ⚠️ Read as TEXT, never decoded as JSON here: the controller serves the same
	// executor behind two framings — a one-shot JSON-RPC object and an SSE stream
	// — and only the raw body says which arrived.
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read the control reply failed")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.Errorf(
			ctx,
			"the controller answered HTTP %d to the control call at %s",
			res.StatusCode, redactURL(cfg.URL),
		)
	}
	return parseControlResponse(ctx, string(raw), request.Operation)
}

// parseControlResponse reads either framing and returns the ControlResponse.
func parseControlResponse(
	ctx context.Context,
	body string,
	sentOperation string,
) (*ControlResponse, error) {
	frames, err := controlFrames(ctx, body)
	if err != nil {
		return nil, err
	}

	state := controlState(frames)
	if state == TaskStateFailed {
		return nil, errors.Errorf(
			ctx,
			"the controller reported the control call FAILED. It returns no detail on "+
				"this path, and it does NOT mean the operation did not happen — the "+
				"failure can land in the controller's task store after the executor has "+
				"run. Treat it as an UNKNOWN outcome and reconcile against the "+
				"controller's own `list`",
		)
	}

	payload := findDataPart(frames)
	if payload == nil {
		return nil, errors.Errorf(
			ctx,
			"the control call ended %q but carried no data part, so there is no "+
				"`ControlResponse` to read. A text part here is not a near miss: the "+
				"controller answers with a data part",
			state,
		)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "re-encode the control response failed")
	}
	var response ControlResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		return nil, errors.Wrapf(ctx, err, "decode the control response failed")
	}
	if sentOperation != "" && response.Operation != sentOperation {
		return nil, errors.Errorf(
			ctx,
			"the control call asked for %q but the reply names %q — the answer does not "+
				"belong to this call",
			sentOperation, response.Operation,
		)
	}
	return &response, nil
}

// controlFrames reads the frames a control reply carries, in either framing.
func controlFrames(ctx context.Context, body string) ([]map[string]any, error) {
	if strings.Contains(body, "data:") {
		return parseFrames(ctx, body)
	}
	var envelope jsonRPCResponse
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return nil, errors.Errorf(
			ctx, "the control reply is neither SSE nor JSON: %s", truncate(body),
		)
	}
	if envelope.Error != nil {
		return nil, errors.Errorf(
			ctx,
			"the controller refused the control call: %s. A refusal here is a JSON-RPC "+
				"error, not a FAILED task — the request never reached a backend",
			envelope.Error.Error(),
		)
	}
	var result map[string]any
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, errors.Wrapf(ctx, err, "decode the control result failed")
	}
	// The result is a `SendMessageResponse` ONEOF — `{task}` or `{message}` — not
	// the task itself.
	for _, key := range []string{"task", "message"} {
		if nested, ok := result[key].(map[string]any); ok {
			return []map[string]any{nested}, nil
		}
	}
	return []map[string]any{result}, nil
}

// controlState reads the terminal state a control reply reports.
//
// ⚠️ THE STATUS IS READ BEFORE THE PAYLOAD, and the order is load-bearing: a
// backend failure yields `TASK_STATE_FAILED` with NO artifact, so a client that
// hunted for the data part first would find none and have to invent a reason.
func controlState(frames []map[string]any) string {
	state := ""
	for _, frame := range frames {
		if update, ok := frame["statusUpdate"].(map[string]any); ok {
			if s := eventState(update); s != "" {
				state = s
			}
		}
	}
	if state != "" {
		return state
	}
	for _, frame := range frames {
		if status, ok := frame["status"].(map[string]any); ok {
			if s, ok := status["state"].(string); ok {
				state = s
			}
		}
	}
	return state
}

// findDataPart returns the first `data` part carried by a frame or its
// artifacts.
func findDataPart(frames []map[string]any) any {
	candidates := make([]map[string]any, 0, len(frames)*2)
	for _, frame := range frames {
		candidates = append(candidates, frame)
		if task, ok := frame["task"].(map[string]any); ok {
			candidates = append(candidates, task)
		}
		if update, ok := frame["artifactUpdate"].(map[string]any); ok {
			if artifact, ok := update["artifact"].(map[string]any); ok {
				candidates = append(candidates, artifact)
			}
		}
	}
	for _, candidate := range candidates {
		artifacts, _ := candidate["artifacts"].([]any)
		for _, raw := range artifacts {
			artifact, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := dataPartOf(artifact); ok {
				return value
			}
		}
		if value, ok := dataPartOf(candidate); ok {
			return value
		}
	}
	return nil
}

func dataPartOf(container map[string]any) (any, bool) {
	parts, ok := container["parts"].([]any)
	if !ok {
		return nil, false
	}
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if value, ok := part["data"]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

// ─── card ───────────────────────────────────────────────────────────────────

// CardURL is where a card is served, given an A2A endpoint.
//
// ⚠️ THE ORIGIN, NOT THE PATH. `create` returns the worker's `/a2a` endpoint;
// the card is served at the well-known path on the SAME origin.
func CardURL(ctx context.Context, endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.Errorf(ctx, "the A2A URL %q is not a parseable URL", redactURL(endpoint))
	}
	return parsed.Scheme + "://" + parsed.Host + AgentCardPath, nil
}

// ReadCard reads an Agent Card.
//
// ⚠️ THE CONTROLLER'S CARD IS AUTHENTICATED, AND READING IT WITHOUT THE TOKEN
// SILENTLY READS NOTHING: an unauthenticated GET answers 401, the version read
// comes back empty, no `A2A-Version` is sent, and the controller refuses the
// control call with the exact `-32009` this read exists to prevent. The worker's
// card is public, which is why the turn plane never needed this.
func (c *Client) ReadCard(ctx context.Context, endpoint, token string) (*AgentCard, error) {
	cardURL, err := CardURL(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "build card request failed")
	}
	req.Header.Set("accept", "application/json")
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read the agent card at %s failed", cardURL)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.Errorf(
			ctx, "the agent card at %s answered HTTP %d", cardURL, res.StatusCode,
		)
	}
	var card AgentCard
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&card); err != nil {
		return nil, errors.Wrapf(ctx, err, "decode the agent card at %s failed", cardURL)
	}
	return &card, nil
}

// controllerVersion reads the controller's protocol version, or "".
//
// A failed read is NOT a failed control call, so it returns "" rather than an
// error: "" sends no header — the pre-negotiation behaviour a controller with no
// version check still accepts — and the control call that follows reports what
// actually happened.
func (c *Client) controllerVersion(ctx context.Context, cfg ControllerConfig) string {
	card, err := c.ReadCard(ctx, cfg.URL, cfg.Token)
	if err != nil {
		return ""
	}
	return card.ProtocolVersion()
}

// ─── worker turn plane ──────────────────────────────────────────────────────

// TurnResult is what one turn produced.
type TurnResult struct {
	State     string
	ContextID string
	Reply     string
	TaskID    string
}

// SendTurn sends one user turn to a worker and collects its stream.
//
// `onTaskID` is called once, as soon as the stream names a task id — the push
// callback must be registered against that task BEFORE the worker emits its
// mid-turn status (~10 s in), and a reader that only yields at the end of the
// stream learns the id minutes too late.
func (c *Client) SendTurn(
	ctx context.Context,
	endpoint string,
	token string,
	contextID string,
	text string,
	protocolVersion string,
	onTaskID func(taskID string),
) (*TurnResult, error) {
	body := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  SendStreamingMethod,
		Params: map[string]any{
			"message": map[string]any{
				"messageId": c.newID(),
				"role":      UserRole,
				"contextId": contextID,
				// ⚠️ A FLATTENED TEXT part — `{text}`, never `{content: …}`.
				"parts": []any{map[string]any{"text": text}},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal the turn failed")
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(encoded),
	)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "build the turn request failed")
	}
	req.Header.Set("content-type", "application/json")
	// The SSE half is requested explicitly: a client that omits it gets the
	// one-shot response from a streaming route, which reads as a turn that
	// produced no frames.
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("authorization", "Bearer "+token)
	if protocolVersion != "" {
		req.Header.Set(VersionHeader, protocolVersion)
	}

	res, err := c.http().Do(req)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "the turn to %s could not be made", redactURL(endpoint))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, errors.Errorf(
			ctx, "the worker answered HTTP %d to the turn: %s",
			res.StatusCode, truncate(string(raw)),
		)
	}
	return collectTurn(ctx, res.Body, contextID, onTaskID)
}

// collectTurn reads the SSE stream incrementally.
//
// ⚠️ THE BUFFER IS THE WHOLE CORRECTNESS PROBLEM: a chunk boundary falls wherever
// the network puts it, so a `data:` line routinely arrives split across two
// reads. Only text up to the LAST newline is parsed, and the remainder is held
// for the next chunk.
func collectTurn(
	ctx context.Context,
	body io.Reader,
	sentContextID string,
	onTaskID func(taskID string),
) (*TurnResult, error) {
	frames, taskID, err := readTurnFrames(ctx, body, onTaskID)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, errors.Errorf(ctx, "the turn produced no frames — nothing executed")
	}

	contextID := turnContextID(frames)
	if sentContextID != "" && contextID != "" && contextID != sentContextID {
		return nil, errors.Errorf(
			ctx,
			"the turn ran under contextId %q but was sent under %q — the conversation is "+
				"not the one intended. This is the only client-side observable of a dropped "+
				"`contextId`, and it is silent otherwise",
			contextID, sentContextID,
		)
	}
	return &TurnResult{
		State:     controlState(frames),
		ContextID: contextID,
		Reply:     turnReply(frames),
		TaskID:    taskID,
	}, nil
}

// readTurnFrames reads the stream chunk by chunk and returns every frame.
//
// `onTaskID` fires once, on the first frame that names a task id.
func readTurnFrames(
	ctx context.Context,
	body io.Reader,
	onTaskID func(taskID string),
) ([]map[string]any, string, error) {
	reader := bufio.NewReaderSize(body, 64<<10)
	accumulator := &turnFrames{onTaskID: onTaskID}
	var buffer string

	for {
		// The body is bound to ctx by the caller's request, so a cancel closes it
		// underneath the reader. Checking explicitly keeps that exit visible here
		// rather than inferring it from an I/O error that reads like a broken
		// stream.
		if cancelErr := ctx.Err(); cancelErr != nil {
			return nil, "", errors.Wrapf(ctx, cancelErr, "the turn stream was cancelled")
		}
		chunk, readErr := reader.ReadString('\n')
		buffer += chunk
		complete := buffer
		if strings.HasSuffix(buffer, "\n") {
			buffer = ""
		} else if readErr == nil {
			continue
		}
		// A trailing line with NO newline is still a frame: the last `data:` line
		// often arrives exactly so, and dropping it loses the terminal event.
		if err := accumulator.consume(ctx, complete); err != nil {
			return nil, "", err
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			break
		}
		return nil, "", errors.Wrapf(ctx, readErr, "read the turn stream failed")
	}
	return accumulator.frames, accumulator.taskID, nil
}

// turnFrames accumulates a turn's frames and announces the first task id.
type turnFrames struct {
	frames    []map[string]any
	taskID    string
	onTaskID  func(taskID string)
	announced sync.Once
}

// consume parses one complete chunk of the stream.
func (t *turnFrames) consume(ctx context.Context, chunk string) error {
	for _, line := range strings.Split(strings.TrimSuffix(chunk, "\n"), "\n") {
		frame, skip, err := parseFrame(ctx, line)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		t.frames = append(t.frames, frame)
		t.announce(frame)
	}
	return nil
}

// announce fires `onTaskID` once, on the first frame that names a task id.
func (t *turnFrames) announce(frame map[string]any) {
	if t.taskID != "" {
		return
	}
	id := taskIDOf(frame)
	if id == "" {
		return
	}
	t.taskID = id
	if t.onTaskID != nil {
		t.announced.Do(func() { t.onTaskID(id) })
	}
}

// turnContextID reads the contextId a turn ran under.
func turnContextID(frames []map[string]any) string {
	for _, frame := range frames {
		if task, ok := frame["task"].(map[string]any); ok {
			if id, ok := task["contextId"].(string); ok && id != "" {
				return id
			}
		}
		if id, ok := frame["contextId"].(string); ok && id != "" {
			return id
		}
	}
	return ""
}

// turnReply concatenates the text parts a turn's frames carry.
func turnReply(frames []map[string]any) string {
	var reply strings.Builder
	for _, frame := range frames {
		for _, text := range frameTexts(frame) {
			if reply.Len() > 0 {
				reply.WriteString("\n")
			}
			reply.WriteString(text)
		}
	}
	return reply.String()
}

// frameTexts reads the text parts of a frame or of the artifact it carries.
func frameTexts(frame map[string]any) []string {
	container := frame
	if update, ok := frame["artifactUpdate"].(map[string]any); ok {
		container = update
		if artifact, ok := update["artifact"].(map[string]any); ok {
			container = artifact
		}
	}
	parts, ok := container["parts"].([]any)
	if !ok {
		return nil
	}
	texts := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := part["text"].(string); ok && text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

// RegisterPush arms the worker -> daemon status channel for one task.
func (c *Client) RegisterPush(
	ctx context.Context,
	endpoint string,
	token string,
	taskID string,
	pushURL string,
	pushToken string,
	protocolVersion string,
) error {
	body := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  PushConfigMethod,
		// ⚠️ THE PARAMS ARE FLAT. The obvious spelling —
		// `{taskId, pushNotificationConfig: {url, token}}` — is WRONG here: the
		// handler unmarshals `a2a.PushConfig` directly, and that struct carries
		// `taskId`, `url` and `token` as its OWN fields. A wrapped body is
		// well-formed JSON the handler refuses as an unmarshal error.
		Params: map[string]any{
			"taskId": taskID,
			"url":    pushURL,
			"token":  pushToken,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal the push registration failed")
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(encoded),
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "build the push registration failed")
	}
	req.Header.Set("content-type", "application/json")
	// The same header as the turn and for the same reason: this call reaches the
	// worker's own JSONRPC handler, which runs the same version gate. Omitting it
	// does not error — the status channel simply never carries anything.
	req.Header.Set("authorization", "Bearer "+token)
	if protocolVersion != "" {
		req.Header.Set(VersionHeader, protocolVersion)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return errors.Wrapf(ctx, err, "the push registration to %s failed", redactURL(endpoint))
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.Errorf(
			ctx, "the push registration answered HTTP %d: %s", res.StatusCode, truncate(string(raw)),
		)
	}
	var envelope jsonRPCResponse
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error != nil {
		return errors.Errorf(
			ctx, "the worker refused the push registration: %s", envelope.Error.Error(),
		)
	}
	return nil
}

// ListPushConfigs reads back the push configs a worker HOLDS for one task.
//
// ⚠️ THIS IS THE READ HALF OF THE ROUND TRIP. A successful RegisterPush is only
// evidence that the worker ACCEPTED a registration; an accepted-then-dropped
// config is indistinguishable from a working one until something reads it back,
// and this call is that read.
//
// ⚠️ AN EMPTY LIST IS A RESULT, NOT AN ERROR. It says the worker holds no config
// for this task, which is a different fact from a refused call — and the caller
// must be able to tell them apart.
func (c *Client) ListPushConfigs(
	ctx context.Context,
	endpoint string,
	token string,
	taskID string,
	protocolVersion string,
) ([]any, error) {
	if endpoint == "" {
		return nil, errors.Errorf(ctx, "endpoint is required to read a push callback back over A2A")
	}
	if token == "" {
		return nil, errors.Errorf(ctx, "token is required to read a push callback back over A2A")
	}
	if taskID == "" {
		return nil, errors.Errorf(ctx, "taskId is required to read a push callback back over A2A")
	}
	body := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  ListPushConfigMethod,
		// `taskId` ALONE — the list is keyed by the task, and the config id the
		// registration never sends is exactly what a get would need.
		Params: map[string]any{"taskId": taskID},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "marshal the push-config read-back failed")
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, endpoint, bytes.NewReader(encoded),
	)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "build the push-config read-back failed")
	}
	req.Header.Set("content-type", "application/json")
	// The same header as the registration and for the same reason: this reaches
	// the worker's own JSONRPC handler, which runs the same version gate.
	req.Header.Set("authorization", "Bearer "+token)
	if protocolVersion != "" {
		req.Header.Set(VersionHeader, protocolVersion)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, errors.Wrapf(
			ctx, err, "the push-config read-back to %s failed", redactURL(endpoint),
		)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.Errorf(
			ctx,
			"the worker answered HTTP %d to the push-config read-back: %s",
			res.StatusCode,
			truncate(string(raw)),
		)
	}
	var envelope jsonRPCResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errors.Wrapf(ctx, err, "the push-config read-back body was not JSON")
	}
	if envelope.Error != nil {
		return nil, errors.Errorf(
			ctx, "the worker refused the push-config read-back: %s", envelope.Error.Error(),
		)
	}
	var result struct {
		Configs []any `json:"configs"`
	}
	if len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			return nil, errors.Wrapf(ctx, err, "decode the push-config read-back result failed")
		}
	}
	if result.Configs == nil {
		return []any{}, nil
	}
	return result.Configs, nil
}

// ─── helpers ────────────────────────────────────────────────────────────────

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) newID() string {
	if c.NewID != nil {
		return c.NewID()
	}
	return NewID()
}

// NewID mints a message or context id.
func NewID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("cw-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// parseFrames parses a whole SSE body.
func parseFrames(ctx context.Context, body string) ([]map[string]any, error) {
	frames := make([]map[string]any, 0, 8)
	for _, line := range strings.Split(body, "\n") {
		frame, skip, err := parseFrame(ctx, line)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// parseFrame parses one SSE line.
//
// ⚠️ AN ERROR FRAME IS FATAL. The measured defect is a refused request arriving
// inside a well-formed HTTP 200 `text/event-stream` envelope: every transport
// check goes green while nothing was executed.
func parseFrame(ctx context.Context, line string) (map[string]any, bool, error) {
	if !strings.HasPrefix(line, "data:") {
		return nil, true, nil
	}
	raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if raw == "" || raw == "[DONE]" {
		return nil, true, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, false, errors.Errorf(ctx, "an SSE frame is not JSON: %s", truncate(raw))
	}
	if rpcErr, ok := payload["error"]; ok && rpcErr != nil {
		return nil, false, errors.Errorf(
			ctx,
			"the service refused the turn: %v. This arrives inside a well-formed HTTP 200 "+
				"SSE envelope, so the transport status proves nothing",
			rpcErr,
		)
	}
	if result, ok := payload["result"].(map[string]any); ok {
		return result, false, nil
	}
	return payload, false, nil
}

// eventState reads the state a status update carries — nested, `{status:{state}}`.
func eventState(update map[string]any) string {
	if status, ok := update["status"].(map[string]any); ok {
		if state, ok := status["state"].(string); ok {
			return state
		}
	}
	if state, ok := update["state"].(string); ok {
		return state
	}
	return ""
}

// taskIDOf reads the task id from whichever shape a frame carries it in.
func taskIDOf(frame map[string]any) string {
	if task, ok := frame["task"].(map[string]any); ok {
		if id, ok := task["id"].(string); ok && id != "" {
			return id
		}
	}
	if id, ok := frame["id"].(string); ok && id != "" {
		return id
	}
	for _, key := range []string{"statusUpdate", "artifactUpdate"} {
		if update, ok := frame[key].(map[string]any); ok {
			if id, ok := update["taskId"].(string); ok && id != "" {
				return id
			}
		}
	}
	return ""
}

func truncate(value string) string {
	if len(value) > 200 {
		return value[:200]
	}
	return value
}
