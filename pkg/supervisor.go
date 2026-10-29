// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// SpawnOptions is what a caller may ask for when starting a child.
type SpawnOptions struct {
	Cwd              string `json:"cwd"`
	Model            string `json:"model"`
	PermissionMode   string `json:"permission_mode"`
	PermissionPrompt string `json:"permission_prompt"`
	// Env is merged OVER this process's environment, never substituted for it.
	//
	// A caller that resolved an environment for the child — a mode handover, a
	// router base URL, a per-call override — has no other way to hand it over,
	// and the loss is invisible from the outside: the child starts, answers and
	// behaves differently. Replacing the environment instead of merging would
	// drop PATH and HOME and break the child outright, which is why the
	// inherited half is always kept.
	Env map[string]string `json:"env"`
	// Resume continues an existing conversation instead of starting one.
	//
	// The child's claude session id is then the RESUMED id, not the id this
	// supervisor minted for it. A caller that needs the child's id reads it from
	// the child's own `system`/`init` event rather than from the spawn response.
	Resume string `json:"resume"`
	// MCPConfigs are additional MCP config files, loaded alongside the generated
	// one.
	//
	// This is what makes a launcher's own config reachable: it arrives as a CLI
	// flag, not as a setting, so no setting source can reach it, and a child
	// without it has measurably fewer servers than its hand-started twin.
	MCPConfigs []string `json:"mcp_configs"`
}

// Supervisor owns the claude children this process spawned.
//
// It owns them deliberately: a store that observes someone else's session can
// report a question, but only the process holding the child can answer it.
type Supervisor interface {
	Spawn(ctx context.Context, options SpawnOptions) (*Session, error)
	List(ctx context.Context) ([]Session, error)
	Get(ctx context.Context, id string) (*Session, error)
	Send(ctx context.Context, id string, text string) error
	// SetPermissionMode changes the permission mode of a running child and
	// returns the mode the child itself reports for it.
	//
	// The caller must hold the session lock: a control request written between
	// two steps of a sequence would land in the middle of a run.
	SetPermissionMode(ctx context.Context, id string, mode string) (string, error)
	Answer(ctx context.Context, id string, requestID string, answer Answer) error
	RegisterQuestion(ctx context.Context, id string, question Question) error
	// ForgetQuestion drops a request the child has stopped waiting on.
	//
	// It must be called when the child-facing tool's hold expires: without it the
	// session keeps reporting a question nothing is waiting on, so every later
	// read says "parked" and every later send returns that stale request instead
	// of awaiting its own turn.
	ForgetQuestion(ctx context.Context, id string, requestID string) error
	PollAnswer(ctx context.Context, requestID string) (PendingAnswer, error)
	Stop(ctx context.Context, id string) error
	Subscribe(ctx context.Context, id string) ([]Event, <-chan Event, func(), error)
}

// NewSupervisor creates a Supervisor that runs the given claude binary.
//
// selfURL is this process's own base URL (e.g. "http://localhost:18090"); every
// child's permission tool is pointed at it so questions come back here.
func NewSupervisor(
	ctx context.Context,
	claudeBin string,
	now libtime.CurrentDateTimeGetter,
	selfURL string,
) Supervisor {
	return &supervisor{
		ctx:       ctx,
		claudeBin: claudeBin,
		now:       now,
		selfURL:   selfURL,
		mutex:     sync.RWMutex{},
		sessions:  map[string]*child{},
		pending:   NewPending(),
	}
}

type supervisor struct {
	ctx       context.Context
	claudeBin string
	now       libtime.CurrentDateTimeGetter
	// selfURL is this process's own base URL, handed to every child's
	// permission tool so it reports its questions HERE.
	//
	// It is a field rather than the constant it used to be: a wrapper started on
	// any other port gave its children `http://localhost:18090`, so the child
	// registered its question against a different process — silently, because the
	// tool only logs a failed registration. Measured 2026-10-29 on a :18091
	// instance, where the child's question never reached the wrapper that spawned
	// it.
	selfURL  string
	mutex    sync.RWMutex
	sessions map[string]*child
	pending  Pending
}

// Spawn starts one claude child in stream-json duplex mode.
//
// --include-partial-messages is load-bearing rather than cosmetic: without it
// the stream carries only completed assistant messages, so an open turn is not
// observable and State can only ever read idle between turns. The partial
// stream events are what make StateGenerating distinguishable from StateIdle.
func (s *supervisor) Spawn(ctx context.Context, options SpawnOptions) (*Session, error) {
	id := newID()
	c, err := s.startChild(ctx, id, options)
	if err != nil {
		return nil, err
	}
	s.mutex.Lock()
	s.sessions[id] = c
	s.mutex.Unlock()
	return c.snapshot(), nil
}

func (s *supervisor) List(ctx context.Context) ([]Session, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	result := make([]Session, 0, len(s.sessions))
	for _, c := range s.sessions {
		result = append(result, *c.snapshot())
	}
	return result, nil
}

func (s *supervisor) Get(ctx context.Context, id string) (*Session, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	c, ok := s.sessions[id]
	if !ok {
		return nil, errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.snapshot(), nil
}

// Subscribe streams one child's events: the replay of what it has already
// emitted, then everything that follows.
//
// The channel closes when the child exits, or when the caller falls more than
// subscriberBuffer events behind — the two cases are deliberately the same
// signal, because in both the stream has stopped being a faithful account of
// the child. Cancel must be called once the caller stops reading; it is safe to
// call more than once.
func (s *supervisor) Subscribe(ctx context.Context, id string) ([]Event, <-chan Event, func(), error) {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return nil, nil, nil, errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	replay, live, cancel := c.subscribe()
	return replay, live, cancel, nil
}

func (s *supervisor) Answer(ctx context.Context, id string, requestID string, answer Answer) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	if err := s.pending.Resolve(ctx, requestID, answer); err != nil {
		return err
	}
	c.clearQuestion(requestID)
	return nil
}

// syntheticModel is the message.model the child reports for a turn a local
// slash command answered instead of a model.
//
// Measured 2026-10-29: `/model <name>` written as a user turn produces an
// assistant event whose message.model is exactly this string. Storing it would
// replace the last real model with a value that names no model at all, so every
// later read would report it as the one that answered.
const syntheticModel = "<synthetic>"

// modelProbeTurn is written after a model change to obtain the new model's
// first real answer.
//
// A change cannot read its own effect: the `/model` command's own turn is
// synthetic. One cheap turn makes the read-back the next assistant event rather
// than an inference from the caller's argument.
const modelProbeTurn = "Reply with only the word OK."

// controlRequestTimeout bounds one control exchange. The child answers a
// set_permission_mode request immediately, so a silence here is a child that
// will never answer and must surface rather than hang the caller.
const controlRequestTimeout = 30 * time.Second

// Send writes one user turn to the child's stdin.
//
// Without this the wrapper can start a child and watch it, but never make it
// do anything — and a child that is never asked never reaches a permission
// gate, so the question path could not be exercised at all.
func (s *supervisor) Send(ctx context.Context, id string, text string) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.send(ctx, text)
}

// SetPermissionMode changes the permission mode of a running child and returns
// the mode the child itself reports for it.
func (s *supervisor) SetPermissionMode(
	ctx context.Context,
	id string,
	mode string,
) (string, error) {
	if !isPermissionMode(mode) {
		return "", errors.Wrapf(ctx, ErrInvalidSpawnOptions,
			"permission mode %q is not one of %v", mode, AvailablePermissionModes())
	}
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return "", errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.setPermissionMode(ctx, mode)
}

// RegisterQuestion records a permission request raised by the child's MCP tool.
func (s *supervisor) RegisterQuestion(ctx context.Context, id string, question Question) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	if c.isExited() {
		return errors.Wrapf(ctx, ErrSessionExited, "session %q exited", id)
	}
	s.pending.Register(question.RequestID, question)
	c.setQuestion(question)
	return nil
}

// PollAnswer reports whether the pending request has been answered yet.
func (s *supervisor) PollAnswer(ctx context.Context, requestID string) (PendingAnswer, error) {
	return s.pending.Peek(requestID)
}

// ForgetQuestion drops a request nothing is waiting on any more.
func (s *supervisor) ForgetQuestion(ctx context.Context, id string, requestID string) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	s.pending.Forget(requestID)
	c.clearQuestion(requestID)
	return nil
}

func (s *supervisor) Stop(ctx context.Context, id string) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	return c.stop(ctx)
}

// newID returns a random UUID v4.
//
// It must be a UUID: `claude --session-id` rejects anything else outright
// ("Invalid session ID. Must be a valid UUID."), and the child exits before
// producing a single event — which reads as a spawn that silently failed.
func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is not recoverable in any useful way here.
		panic(err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func (s *supervisor) startChild(ctx context.Context, id string, options SpawnOptions) (*child, error) {
	// Validated first, so a refusal costs no temp file: writeMCPConfig below
	// already writes one, and a rejected spawn must leave nothing behind.
	if err := validateEnv(ctx, options.Env); err != nil {
		return nil, err
	}
	if err := validateArgv(ctx, options); err != nil {
		return nil, err
	}
	mcpConfig, err := writeMCPConfig(ctx, id, s.selfURL)
	if err != nil {
		return nil, err
	}
	// s.ctx, never the caller's ctx: the child must outlive the HTTP request
	// that spawned it. Binding it to req.Context() killed every child the
	// instant POST /sessions responded, which read as a spawn that silently
	// exited.
	// #nosec G204 -- claudeBin is an operator-supplied flag, never request input.
	cmd := exec.CommandContext(s.ctx, s.claudeBin, buildArgs(id, options, mcpConfig)...)
	cmd.Dir = options.Cwd
	// The child inherits THIS process's environment unless it is given its own,
	// so a caller's resolved environment would otherwise be discarded without a
	// word — the child starts, answers, and behaves differently.
	cmd.Env = childEnv(os.Environ(), options.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "stdin pipe failed")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "stdout pipe failed")
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrapf(ctx, err, "start claude failed")
	}

	c := &child{
		id:          id,
		now:         s.now,
		cmd:         cmd,
		stdin:       stdin,
		subscribers: map[int]chan Event{},
		session: Session{
			ID:        id,
			State:     StateStarting,
			Controls:  []Control{ControlMessage},
			Cwd:       options.Cwd,
			StartedAt: s.now.Now().Time().UTC(),
		},
	}
	go c.consume(bufio.NewScanner(stdout))
	go c.reap()
	return c, nil
}

// DefaultPermissionMode is how a child is spawned unless the caller says
// otherwise.
//
// `auto` rather than `bypassPermissions`: auto approves what policy permits and
// routes the remainder to the permission-prompt tool, which is what makes a
// pending question observable at all. Measured 2026-10-28, `--permission-prompts
// host` emits no control_request on stdout or stderr, so a child that asks with
// no tool configured blocks until killed — the failure this default avoids.
const DefaultPermissionMode = "auto"

// writeMCPConfig points the child at this binary's stdio MCP mode.
//
// A permission-prompt tool has to resolve to a real MCP tool, so the child is
// given a generated config whose `command` is the running wrapper. The log path
// is what makes the tool's payload observable: its schema is undocumented, so
// the first runs record it verbatim rather than parsing a guess.
func writeMCPConfig(ctx context.Context, id string, selfURL string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", errors.Wrapf(ctx, err, "resolve own binary failed")
	}
	config := map[string]any{
		"mcpServers": map[string]any{
			"claude-wrapper": map[string]any{
				"command": self,
				"args": []string{
					"--mcp-stdio",
					"--mcp-log", filepath.Join(os.TempDir(), "claude-wrapper-"+id+".mcp.log"),
					"--wrapper-url", selfURL,
					"--session-id", id,
				},
			},
		},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "marshal mcp config failed")
	}
	path := filepath.Join(os.TempDir(), "claude-wrapper-"+id+".mcp.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", errors.Wrapf(ctx, err, "write mcp config failed")
	}
	return path, nil
}

// buildArgs pins the flags the wrapper depends on. The stream formats are
// --print-only, which is why phase 1 wraps only children it spawns.
func buildArgs(id string, options SpawnOptions, mcpConfig string) []string {
	mode := options.PermissionMode
	if mode == "" {
		mode = DefaultPermissionMode
	}
	args := []string{
		"--print",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
	}
	// Resuming and minting an id are mutually exclusive: the resumed
	// conversation already has one, and passing both would leave the CLI holding
	// two identities for a single conversation.
	if options.Resume != "" {
		args = append(args, "--resume", options.Resume)
	} else {
		args = append(args, "--session-id", id)
	}
	args = append(args,
		"--permission-mode", mode,
		// --mcp-config is variadic, so the extra files belong immediately after
		// the generated one and before the next flag.
		"--mcp-config", mcpConfig,
	)
	args = append(args, options.MCPConfigs...)
	args = append(args, "--permission-prompt-tool", "mcp__claude-wrapper__"+PermissionToolName)
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.PermissionPrompt != "" {
		args = append(args, "--permission-prompts", options.PermissionPrompt)
	}
	return args
}

// validateEnv refuses an environment key that cannot be expressed as KEY=VALUE.
//
// Refused rather than filtered, on the same reasoning the option itself exists
// for: a key silently dropped is a value the child does not have, and a child
// missing one value looks exactly like one that was never given it.
//
// A NUL in the VALUE is refused too, and not for symmetry: execve treats each
// entry as an opaque NUL-terminated string, so a value containing one reaches
// os/exec and fails at cmd.Start with EINVAL — which surfaces as a 500 for what
// is plainly a caller error, the exact confusion the statusFor mapping exists to
// avoid.
func validateEnv(ctx context.Context, env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "env key %q is not a valid environment name", key)
		}
		if strings.ContainsRune(value, 0) {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "env value for %q contains a NUL byte", key)
		}
	}
	return nil
}

// uuidPattern is the shape `claude` accepts for a session id. newID mints one
// and its doc records the CLI's refusal ("Invalid session ID. Must be a valid
// UUID.") when the shape is wrong; `--resume` takes the same kind of value, so a
// malformed one has to be refused here rather than left to exit before producing
// a single event.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validateArgv refuses values that would not reach the child as the argument
// they look like.
//
// ⚠️ This is not cosmetic validation. `--mcp-config` is VARIADIC, so every entry
// is appended straight into argv — an entry beginning with `-` is parsed as a
// FLAG rather than a path, and `mcp_configs: ["--dangerously-skip-permissions"]`
// would silently change the child's permission posture instead of being refused.
// The same reasoning covers `resume`, which sits immediately after `--resume` and
// would otherwise consume the next flag as its value.
func validateArgv(ctx context.Context, options SpawnOptions) error {
	for _, config := range options.MCPConfigs {
		if config == "" {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "mcp_configs contains an empty entry")
		}
		if strings.HasPrefix(config, "-") {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions,
				"mcp config %q begins with '-' and would be parsed as a flag", config)
		}
		// Same reason as the env-value check: a NUL survives JSON, is rejected by
		// os/exec at cmd.Start, and surfaces as a 500 for what is plainly a caller
		// error. `resume` cannot carry one — uuidPattern already excludes it.
		if strings.ContainsRune(config, 0) {
			return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "mcp config %q contains a NUL byte", config)
		}
	}
	if options.Resume != "" && !uuidPattern.MatchString(options.Resume) {
		return errors.Wrapf(ctx, ErrInvalidSpawnOptions, "resume %q is not a UUID", options.Resume)
	}
	return nil
}

// childEnv merges overrides over the inherited environment.
//
// An override REPLACES the inherited value for its key rather than appending a
// second copy: a duplicated key resolves to whichever copy the child reads
// first, which is not something a caller can observe from outside. Keys are
// emitted in sorted order so one spawn's environment is reproducible.
func childEnv(inherited []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return inherited
	}
	merged := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		if key, _, ok := strings.Cut(entry, "="); ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		merged = append(merged, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		merged = append(merged, key+"="+overrides[key])
	}
	return merged
}

// child is one running claude process and the state read off its stream.
type child struct {
	mutex       sync.RWMutex
	writeMu     sync.Mutex
	id          string
	now         libtime.CurrentDateTimeGetter
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	lastEventAt time.Time
	session     Session
	// events retains what the child has already emitted so a subscriber that
	// arrives mid-turn still sees the turn it missed.
	events []Event
	// subscribers holds one channel per live Subscribe caller, keyed by an id
	// that only exists so cancel can find its own entry.
	subscribers map[int]chan Event
	nextSubID   int
	// pendingTurns counts turns written here that the child has not closed.
	//
	// It is the only signal that covers the gap between our write and the
	// child's own message_start: State still reads idle in that window, so a
	// caller that wrote a turn would be invisible to anything reading State.
	pendingTurns int
}

func (c *child) snapshot() *Session {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	clone := c.session
	if c.session.Question != nil {
		question := *c.session.Question
		clone.Question = &question
	}
	clone.Controls = append([]Control{}, c.session.Controls...)
	clone.PendingTurns = c.pendingTurns
	if !c.lastEventAt.IsZero() {
		age := int(c.now.Now().Time().Sub(c.lastEventAt).Seconds())
		clone.LastEventAgeSeconds = &age
	}
	return &clone
}

func (c *child) stop(ctx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.session.State == StateExited {
		return errors.Wrapf(ctx, ErrSessionExited, "session %q already exited", c.id)
	}
	if err := c.cmd.Process.Kill(); err != nil {
		return errors.Wrapf(ctx, err, "kill session %q failed", c.id)
	}
	return nil
}

// send writes one user turn to the child's stdin as JSONL.
func (c *child) send(ctx context.Context, text string) error {
	c.mutex.RLock()
	state := c.session.State
	c.mutex.RUnlock()
	if state == StateExited {
		return errors.Wrapf(ctx, ErrSessionExited, "session %q exited", c.id)
	}
	payload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal message failed")
	}
	// Count the turn BEFORE the write, not after it.
	//
	// apply() takes only c.mutex, so a result applied between the write
	// returning and a later increment would find the counter at zero, skip its
	// decrement, and leave the count stuck at one for the child's lifetime —
	// refusing every subsequent run on a child that is actually idle. Counting
	// first makes the window impossible; a failed write rolls it back.
	c.mutex.Lock()
	c.pendingTurns++
	c.mutex.Unlock()

	c.writeMu.Lock()
	_, writeErr := c.stdin.Write(append(encoded, '\n'))
	c.writeMu.Unlock()
	if writeErr != nil {
		c.mutex.Lock()
		c.pendingTurns--
		c.mutex.Unlock()
		return errors.Wrapf(ctx, writeErr, "write message to session %q failed", c.id)
	}
	return nil
}

// setPermissionMode writes the SDK's own control request and waits for the
// child's answer.
//
// A permission mode is not reachable as a user turn: measured 2026-10-29, none
// of the child's 219 slash commands changes it, and send wraps text into a user
// message. The control channel is the only path, and the child answers it with
// the mode it now holds — which is the read-back, since this process stores no
// mode of its own.
func (c *child) setPermissionMode(ctx context.Context, mode string) (string, error) {
	if c.isExited() {
		return "", errors.Wrapf(ctx, ErrSessionExited, "session %q exited", c.id)
	}
	_, live, cancel := c.subscribe()
	defer cancel()

	requestID := "perm-mode-" + newID()
	if err := c.writeControl(ctx, requestID, map[string]any{
		"subtype": "set_permission_mode",
		"mode":    mode,
	}); err != nil {
		return "", err
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, controlRequestTimeout)
	defer cancelWait()
	for {
		select {
		case <-waitCtx.Done():
			return "", errors.Wrapf(ctx, waitCtx.Err(),
				"waiting for the child to answer permission mode %q", mode)
		case event, open := <-live:
			if !open {
				return "", errors.Wrapf(ctx, ErrSessionExited,
					"stream closed before the child answered permission mode %q", mode)
			}
			reported, done, err := permissionModeAnswer(ctx, event, requestID, mode)
			if err != nil {
				return "", err
			}
			if done {
				return reported, nil
			}
		}
	}
}

// permissionModeAnswer reports whether one event answers the given request, and
// with which mode.
//
// It is a function rather than an inline block so the response shape is testable
// without a live child: the envelope is the SDK's, not this repo's, and a silent
// shape change must fail a test rather than read as a timeout.
func permissionModeAnswer(
	ctx context.Context,
	event Event,
	requestID string,
	mode string,
) (string, bool, error) {
	if event["type"] != "control_response" {
		return "", false, nil
	}
	response, ok := event["response"].(map[string]any)
	if !ok || response["request_id"] != requestID {
		return "", false, nil
	}
	if subtype, _ := response["subtype"].(string); subtype != "success" {
		return "", false, errors.Errorf(ctx, "child refused permission mode %q: %v", mode, response)
	}
	inner, _ := response["response"].(map[string]any)
	reported, _ := inner["mode"].(string)
	if reported == "" {
		return "", false, errors.Errorf(ctx, "child answered %q without reporting a mode", requestID)
	}
	return reported, true, nil
}

// writeControl writes one SDK control request as JSONL.
//
// Unlike send it does not touch pendingTurns: a control request is not a turn
// and the child never answers it with a result event, so counting it would leave
// the counter one too high for the child's lifetime and refuse every later run.
func (c *child) writeControl(ctx context.Context, requestID string, request map[string]any) error {
	encoded, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    request,
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal control request failed")
	}
	c.writeMu.Lock()
	_, writeErr := c.stdin.Write(append(encoded, '\n'))
	c.writeMu.Unlock()
	if writeErr != nil {
		return errors.Wrapf(ctx, writeErr, "write control request to session %q failed", c.id)
	}
	return nil
}

func (c *child) isExited() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.session.State == StateExited
}

func (c *child) setQuestion(question Question) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	clone := question
	c.session.Question = &clone
	c.session.Controls = []Control{ControlAnswer, ControlMessage}
}

func (c *child) clearQuestion(requestID string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.session.Question != nil && c.session.Question.RequestID == requestID {
		c.session.Question = nil
	}
}

func (c *child) reap() {
	err := c.cmd.Wait()
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.session.State = StateExited
	// A child killed mid-turn closes nothing, so the counter would otherwise
	// stay positive for the life of the process and misreport the field.
	c.pendingTurns = 0
	c.session.Question = nil
	c.session.Controls = []Control{}
	c.closeSubscribers()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	}
	c.session.ExitCode = &code
}

// consume reads the child's stdout as JSONL and folds each event into state.
func (c *child) consume(scanner *bufio.Scanner) {
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		c.apply(event)
	}
}

// apply is the single place an event becomes state. Every branch is derived
// from the event itself; none reads a clock.
func (c *child) apply(event map[string]any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.lastEventAt = c.now.Now().Time()
	switch event["type"] {
	case "system":
		if event["subtype"] == "init" {
			c.applyInit(event)
		}
	case "assistant":
		if message, ok := event["message"].(map[string]any); ok {
			if model, ok := message["model"].(string); ok && model != "" && model != syntheticModel {
				c.session.Model = model
				c.session.ModelSource = "assistant"
			}
		}
	case "stream_event":
		c.applyStreamEvent(event)
	case "control_response":
		c.applyControlResponse(event)
	case "result":
		c.session.State = StateIdle
		if c.pendingTurns > 0 {
			c.pendingTurns--
		}
	}
	c.publish(Event(event))
}

// publish retains one event for replay and hands it to every live subscriber.
//
// The caller must hold c.mutex — apply is the only caller, and it already does.
// Taking the lock here instead would deadlock, so the precondition is stated
// rather than enforced: a second caller that forgets it races the reader
// goroutine on c.events and c.subscribers.
//
// A subscriber that has fallen subscriberBuffer events behind is closed rather
// than skipped: a supervision stream with an invisible hole in it is worse than
// one that ends and says so.
func (c *child) publish(event Event) {
	c.events = append(c.events, event)
	if len(c.events) > maxRetainedEvents {
		c.events = append([]Event{}, c.events[len(c.events)-maxRetainedEvents:]...)
	}
	for id, ch := range c.subscribers {
		select {
		case ch <- event:
		default:
			close(ch)
			delete(c.subscribers, id)
		}
	}
}

// subscribe returns everything the child has emitted so far plus a channel
// carrying what follows. The replay is copied under the lock, so a caller never
// races the reader goroutine.
func (c *child) subscribe() ([]Event, <-chan Event, func()) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	replay := append([]Event{}, c.events...)
	live := make(chan Event, subscriberBuffer)
	if c.session.State == StateExited {
		close(live)
		return replay, live, func() {}
	}
	id := c.nextSubID
	c.nextSubID++
	c.subscribers[id] = live
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			if existing, ok := c.subscribers[id]; ok {
				close(existing)
				delete(c.subscribers, id)
			}
		})
	}
	return replay, live, cancel
}

// closeSubscribers ends every live stream. Called once the child is gone, so a
// consumer stops waiting on events that can no longer arrive.
func (c *child) closeSubscribers() {
	for id, ch := range c.subscribers {
		close(ch)
		delete(c.subscribers, id)
	}
}

func (c *child) applyInit(event map[string]any) {
	if model, ok := event["model"].(string); ok && model != "" && c.session.Model == "" {
		c.session.Model = model
		c.session.ModelSource = "init"
	}
	// The mode is taken from init on every turn, not only the first: init is
	// re-emitted for each turn, so a mode changed on a running child appears
	// here on the next one. Reading it only at startup would pin the field to
	// the spawn value and report a stale mode as current.
	if mode, ok := event["permissionMode"].(string); ok && mode != "" {
		c.session.PermissionMode = mode
	}
	if c.session.State == StateStarting {
		c.session.State = StateIdle
	}
}

// applyControlResponse stores the mode a set_permission_mode answer reports.
//
// Without this the change would be visible only in the HTTP response that asked
// for it: GET would keep reporting whatever the last init event said, which is
// the mode the child started with. Measured 2026-10-29 — a change answered
// `acceptEdits` left the session reading `default` until the next turn's init.
//
// A `mode` field is the discriminator rather than a request id: no other control
// response this wrapper reads carries one, and tracking ids here would duplicate
// the waiter that already does.
func (c *child) applyControlResponse(event map[string]any) {
	response, ok := event["response"].(map[string]any)
	if !ok {
		return
	}
	if subtype, _ := response["subtype"].(string); subtype != "success" {
		return
	}
	inner, ok := response["response"].(map[string]any)
	if !ok {
		return
	}
	if mode, ok := inner["mode"].(string); ok && mode != "" {
		c.session.PermissionMode = mode
	}
}

func (c *child) applyStreamEvent(event map[string]any) {
	inner, ok := event["event"].(map[string]any)
	if !ok {
		return
	}
	switch inner["type"] {
	case "message_start":
		c.session.State = StateGenerating
	case "message_stop":
		c.session.State = StateIdle
	}
}
