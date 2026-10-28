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
	"sync"
	"time"

	"github.com/bborbe/errors"
)

// SpawnOptions is what a caller may ask for when starting a child.
type SpawnOptions struct {
	Cwd              string `json:"cwd"`
	Model            string `json:"model"`
	PermissionMode   string `json:"permission_mode"`
	PermissionPrompt string `json:"permission_prompt"`
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
	Answer(ctx context.Context, id string, requestID string, behavior string) error
	RegisterQuestion(ctx context.Context, id string, question Question) error
	PollAnswer(ctx context.Context, requestID string) (string, bool, error)
	Stop(ctx context.Context, id string) error
}

// NewSupervisor creates a Supervisor that runs the given claude binary.
func NewSupervisor(claudeBin string) Supervisor {
	return &supervisor{
		claudeBin: claudeBin,
		mutex:     sync.RWMutex{},
		sessions:  map[string]*child{},
		pending:   NewPending(),
	}
}

type supervisor struct {
	claudeBin string
	mutex     sync.RWMutex
	sessions  map[string]*child
	pending   *Pending
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

func (s *supervisor) Answer(ctx context.Context, id string, requestID string, behavior string) error {
	s.mutex.RLock()
	c, ok := s.sessions[id]
	s.mutex.RUnlock()
	if !ok {
		return errors.Wrapf(ctx, ErrSessionNotFound, "unknown session %q", id)
	}
	if err := s.pending.Resolve(ctx, requestID, behavior); err != nil {
		return err
	}
	c.clearQuestion(requestID)
	return nil
}

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
	s.pending.Register(question.RequestID)
	c.setQuestion(question)
	return nil
}

// PollAnswer reports whether the pending request has been answered yet.
func (s *supervisor) PollAnswer(ctx context.Context, requestID string) (string, bool, error) {
	return s.pending.Peek(requestID)
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
	mcpConfig, err := writeMCPConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	// context.Background(), never the caller's ctx: the child must outlive the
	// HTTP request that spawned it. Binding it to req.Context() killed every
	// child the instant POST /sessions responded, which read as a spawn that
	// silently exited.
	cmd := exec.CommandContext(context.Background(), s.claudeBin, buildArgs(id, options, mcpConfig)...)
	cmd.Dir = options.Cwd

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
		id:    id,
		cmd:   cmd,
		stdin: stdin,
		session: Session{
			ID:        id,
			State:     StateStarting,
			Controls:  []Control{ControlMessage},
			Cwd:       options.Cwd,
			StartedAt: time.Now().UTC(),
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
func writeMCPConfig(ctx context.Context, id string) (string, error) {
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
					"--wrapper-url", "http://localhost:18090",
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
		"--session-id", id,
		"--permission-mode", mode,
		"--mcp-config", mcpConfig,
		"--permission-prompt-tool", "mcp__claude-wrapper__" + PermissionToolName,
	}
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.PermissionPrompt != "" {
		args = append(args, "--permission-prompts", options.PermissionPrompt)
	}
	return args
}

// child is one running claude process and the state read off its stream.
type child struct {
	mutex     sync.RWMutex
	writeMu   sync.Mutex
	id        string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	startedAt time.Time
	session   Session
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
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return errors.Wrapf(ctx, err, "write message to session %q failed", c.id)
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
	c.session.Question = nil
	c.session.Controls = []Control{}
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
	switch event["type"] {
	case "system":
		if event["subtype"] == "init" {
			c.applyInit(event)
		}
	case "assistant":
		if message, ok := event["message"].(map[string]any); ok {
			if model, ok := message["model"].(string); ok && model != "" {
				c.session.Model = model
				c.session.ModelSource = "assistant"
			}
		}
	case "stream_event":
		c.applyStreamEvent(event)
	case "result":
		c.session.State = StateIdle
	}
}

func (c *child) applyInit(event map[string]any) {
	if model, ok := event["model"].(string); ok && model != "" && c.session.Model == "" {
		c.session.Model = model
		c.session.ModelSource = "init"
	}
	if c.session.State == StateStarting {
		c.session.State = StateIdle
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
