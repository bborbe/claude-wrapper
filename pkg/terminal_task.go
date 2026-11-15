// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bborbe/errors"
)

// AnchoredTask is what a resolver found for one session's anchored task.
type AnchoredTask struct {
	// Matches is how many task files carry the session id in their frontmatter.
	// Status, DeferDate and Path are only meaningful when it is exactly one.
	Matches   int
	Path      string
	Status    string
	DeferDate string
}

// AnchoredTaskResolver finds the task a session is anchored on.
type AnchoredTaskResolver interface {
	Resolve(ctx context.Context, sessionID string) (AnchoredTask, error)
}

// NewDirAnchoredTaskResolver scans each directory (non-recursive, `*.md`) for a
// file whose frontmatter names the session in `claude_session_id`.
func NewDirAnchoredTaskResolver(dirs []string) AnchoredTaskResolver {
	return &dirAnchoredTaskResolver{dirs: dirs}
}

type dirAnchoredTaskResolver struct {
	dirs []string
}

func (r *dirAnchoredTaskResolver) Resolve(
	ctx context.Context,
	sessionID string,
) (AnchoredTask, error) {
	var result AnchoredTask
	for _, dir := range r.dirs {
		paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
		if err != nil {
			return AnchoredTask{}, errors.Wrapf(ctx, err, "glob task dir %q failed", dir)
		}
		for _, path := range paths {
			fields, err := readFrontmatter(ctx, path)
			if err != nil {
				// A file renamed or deleted between the glob and the open (Obsidian
				// renames) must not abort the scan for every other task.
				if !errors.Is(err, os.ErrNotExist) {
					log.Printf("WARNING: read task %q failed, skipped: %v", path, err)
				}
				continue
			}
			if fields["claude_session_id"] != sessionID {
				continue
			}
			result.Matches++
			result.Path = path
			result.Status = fields["status"]
			result.DeferDate = fields["defer_date"]
		}
	}
	return result, nil
}

// readFrontmatter returns the top-level scalar keys of the block between the
// first two `---` lines. A file without frontmatter yields an empty map.
func readFrontmatter(ctx context.Context, path string) (map[string]string, error) {
	// #nosec G304 -- path comes from the operator's configured task dirs
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "open failed")
	}
	defer file.Close()
	fields := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return fields, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			return fields, nil
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = unquoteYAMLScalar(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrap(ctx, err, "scan failed")
	}
	// No closing delimiter: not a frontmatter block.
	return map[string]string{}, nil
}

func unquoteYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	if idx := strings.Index(value, " #"); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	return value
}

// terminalTaskDecision reports whether a resolved task allows the child to be
// closed, and why not when it does not. notable marks a keep worth logging at
// normal volume.
func terminalTaskDecision(status AnchoredTask) (closeChild bool, reason string, notable bool) {
	switch {
	case status.Matches == 0:
		return false, "no task file names this session", false
	case status.Matches > 1:
		return false, "multiple task files name this session", true
	}
	switch status.Status {
	case "completed", "aborted":
		return true, "task " + status.Path + " is " + status.Status, true
	default:
		return false, "task status is " + status.Status, false
	}
}

// checkTerminalTask is called at turn end under c.mutex; the decision runs on
// its own goroutine so the event loop never waits on a syscall or on disk.
//
// ⚠️ The HEADLESS WORKER path does not consult the task resolver. A headless
// worker is one-shot: its turn closing with nothing pending IS its end of life,
// whatever its anchored task says. That is the case `closeIfTaskTerminal` alone
// could not express — it fires only when the task is already terminal — and the
// reason an idle headless worker used to survive until the idle reaper's
// fifteen-minute floor. Managers and human-role sessions keep the old rule,
// because a headless manager going idle between rounds is not finished.
func (c *child) checkTerminalTask() {
	if c.ctx == nil {
		return
	}
	if c.endsAtTurnEnd() {
		go c.closeHeadlessTurnEnd(c.ctx)
		return
	}
	if c.taskResolver == nil {
		return
	}
	go c.closeIfTaskTerminal(c.ctx)
}

// endsAtTurnEnd reports whether this child is a headless worker, whose turn
// closing with nothing pending is its end of life. Managers and human-role
// sessions are exempt: a manager between rounds is waiting for its next wake,
// not finished, and stopping one would take the fleet's loop down with it.
// An empty role is the default role (`RoleAgent`), so it stops.
func (c *child) endsAtTurnEnd() bool {
	if c.session.Mode != SpawnModeHeadless {
		return false
	}
	switch c.session.Role {
	case RoleManager, RoleHuman:
		return false
	}
	return true
}

// closeHeadlessTurnEnd stops a headless worker whose turn has closed with
// nothing pending — the Go daemon's end of life for the headless spawn path.
// The node server's `endWorkerIfIdle` performs the same decision on the tab
// path; this is its daemon-side counterpart.
//
// ⚠️ EVERY keep arm is logged. A silent keep would make a stop line
// indistinguishable from a keep, so each of the three reasons
// `stopIfIdleAndUnparked` can return is reported.
func (c *child) closeHeadlessTurnEnd(ctx context.Context) {
	stopped, reason, err := c.stopIfIdleAndUnparked(ctx)
	if err != nil {
		log.Printf("WARNING: could not stop %s after its turn closed: %v", c.id, err)
		return
	}
	if !stopped {
		log.Printf("agent %s: keeping worker — %s", c.id, reason)
		return
	}
	log.Printf("agent %s: turn closed with nothing pending — stopping the session", c.id)
}

// closeIfTaskTerminal resolves the session's task and stops the child when the
// task is terminal and the child is idle with nothing parked. It reports
// whether the child was stopped.
func (c *child) closeIfTaskTerminal(ctx context.Context) bool {
	status, err := c.taskResolver.Resolve(ctx, c.id)
	if err != nil {
		log.Printf("close-on-terminal-task: session %s kept: resolve failed: %v", c.id, err)
		return false
	}
	closeChild, reason, notable := terminalTaskDecision(status)
	if !closeChild {
		if notable {
			log.Printf("close-on-terminal-task: session %s kept: %s", c.id, reason)
		}
		return false
	}
	stopped, keepReason, err := c.stopIfIdleAndUnparked(ctx)
	if err != nil {
		log.Printf("close-on-terminal-task: session %s stop failed: %v", c.id, err)
		return false
	}
	if !stopped {
		log.Printf("close-on-terminal-task: session %s kept (%s): %s", c.id, reason, keepReason)
		return false
	}
	log.Printf("close-on-terminal-task: session %s closed: %s", c.id, reason)
	return true
}

// stopIfIdleAndUnparked kills the child only if, under the lock apply() holds,
// no turn is open and no question is pending — a turn that started after the
// resolve must not be cut off. The kill is the same signal stop() sends, so
// the watcher's reap keeps registry, roster and ledger consistent.
func (c *child) stopIfIdleAndUnparked(ctx context.Context) (bool, string, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	switch {
	case c.session.State == StateExited:
		return false, "already exited", nil
	case c.pendingTurns > 0 || c.session.State == StateGenerating:
		return false, "a turn is open", nil
	case c.session.Question != nil:
		return false, "a question is pending", nil
	}
	if err := syscall.Kill(c.pid, syscall.SIGKILL); err != nil {
		return false, "", errors.Wrapf(ctx, err, "kill session %q failed", c.id)
	}
	return true, "", nil
}
