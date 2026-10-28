// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
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
	Answer(ctx context.Context, id string, requestID string, behavior string) error
	Stop(ctx context.Context, id string) error
}

// NewSupervisor creates a Supervisor that runs the given claude binary.
func NewSupervisor(claudeBin string) Supervisor {
	return &supervisor{
		claudeBin: claudeBin,
		mutex:     sync.RWMutex{},
		sessions:  map[string]*child{},
	}
}

type supervisor struct {
	claudeBin string
	mutex     sync.RWMutex
	sessions  map[string]*child
}

// Spawn starts one claude child in stream-json duplex mode.
//
// --include-partial-messages is load-bearing rather than cosmetic: without it
// the stream carries only completed assistant messages, so an open turn is not
// observable and State can only ever read idle between turns. The partial
// stream events are what make StateGenerating distinguishable from StateIdle.
func (s *supervisor) Spawn(ctx context.Context, options SpawnOptions) (*Session, error) {
	id := time.Now().UTC().Format("20060102T150405.000000000")
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
	return c.answer(ctx, requestID, behavior)
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

func (s *supervisor) startChild(ctx context.Context, id string, options SpawnOptions) (*child, error) {
	cmd := exec.CommandContext(ctx, s.claudeBin, buildArgs(id, options)...)
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

// buildArgs pins the flags the wrapper depends on. The stream formats are
// --print-only, which is why phase 1 wraps only children it spawns.
func buildArgs(id string, options SpawnOptions) []string {
	args := []string{
		"--print",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--session-id", id,
	}
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.PermissionMode != "" {
		args = append(args, "--permission-mode", options.PermissionMode)
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

// answer writes one control response to the child's stdin.
//
// The prompt is only reachable through this process, so the response is
// written to the same child that raised it — answering a store instead would
// answer nothing.
func (c *child) answer(ctx context.Context, requestID string, behavior string) error {
	c.mutex.RLock()
	state := c.session.State
	question := c.session.Question
	c.mutex.RUnlock()

	if state == StateExited {
		return errors.Wrapf(ctx, ErrSessionExited, "session %q exited", c.id)
	}
	if question == nil {
		return errors.Wrapf(ctx, ErrAnswerUnsupported, "session %q holds no open question", c.id)
	}
	if question.RequestID != requestID {
		return errors.Wrapf(ctx, ErrAnswerUnsupported,
			"session %q holds request %q, not %q", c.id, question.RequestID, requestID)
	}

	payload := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   map[string]any{"behavior": behavior},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal answer failed")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return errors.Wrapf(ctx, err, "write answer to session %q failed", c.id)
	}
	return nil
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
