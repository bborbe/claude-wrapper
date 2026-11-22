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
	"time"

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
	// ⚠️ Trimmed: the spawn path stores the role verbatim, and the repo's own
	// manager gates compare `strings.TrimSpace(role)` (`roster_mcp.go`). Comparing
	// untrimmed would exempt `" manager"` at the gate and stop it here — a manager
	// killed by a whitespace typo.
	switch strings.TrimSpace(c.session.Role) {
	case RoleManager, RoleHuman:
		return false
	}
	return true
}

// The three reasons a child must not be stopped. They are constants, not
// literals, because they are the evidence a keep is logged against: the
// headless turn-end path and `stopIfIdleAndUnparked` both read them from
// `keepReasonLocked`, so the logged reason can never drift from the decision.
const (
	reasonAlreadyExited   = "already exited"
	reasonTurnOpen        = "a turn is open"
	reasonRetryPending    = "a zero-turn retry is in flight"
	reasonQuestionPending = "a question is pending"
)

// A store error keeps the session — not orphaning a card is the right instinct —
// but a keep with NO re-check converts a transient outage into a permanent leak.
// Measured 2026-11-16: the attention store was unreachable at 20:48:57, a
// finished headless worker was kept, nothing retried it, and it heartbeated for
// nine minutes until an external signal killed it. The store also flapped five
// times between 16:09 and 16:14, so this is a recurring shape, not a one-off.
//
// The budget is deliberately finite: after it, the session is stopped and the
// warning says an open card may be orphaned. A worker that can never be stopped
// because a store is down is worse than a card whose producer is gone — the
// card still reads, and the operator can still see it.
const (
	headlessCardRetryDelay  = 60 * time.Second
	headlessCardRetryBudget = 5
)

// keepReasonLocked reports why the child must not be stopped, or ok=false when
// it is idle and unparked. The caller must hold c.mutex.
func (c *child) keepReasonLocked() (string, bool) {
	switch {
	case c.session.State == StateExited:
		return reasonAlreadyExited, true
	case c.pendingTurns > 0 || c.session.State == StateGenerating:
		return reasonTurnOpen, true
	case c.retrying:
		// The re-send is decided and its budget spent, but its write has not yet
		// incremented pendingTurns. Treating this as an open turn is what closes
		// the window between the decision and the write: a duplicate turn end in
		// that gap would otherwise kill the child and lose the only attempt.
		return reasonRetryPending, true
	case c.session.Question != nil:
		return reasonQuestionPending, true
	}
	return "", false
}

// armOutcome is what arming a re-check decided.
type armOutcome int

const (
	// armArmed: a re-check is now pending, and this attempt consumed a slot.
	armArmed armOutcome = iota
	// armAlreadyPending: a re-check is already armed for this child.
	armAlreadyPending
	// armBudgetSpent: no slot is left, so the caller stops the child.
	armBudgetSpent
)

// armCardRetry arms one re-check and reports the attempt number it consumed and
// the interval it waits.
//
// ⚠️ **The budget is charged HERE, and only when a timer is actually armed.** It
// used to be charged in the read path, before the "is one already pending?" test —
// and a kept worker stays sendable, so every turn end re-runs this check. A
// duplicate turn end inside the retry window therefore burned a slot without
// arming anything, and a handful of turns could spend the whole `60s × 5` budget
// in seconds, stopping the worker far earlier than the bound promises.
func (c *child) armCardRetry(ctx context.Context) (int, time.Duration, armOutcome) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.cardRetryTimer != nil {
		return 0, 0, armAlreadyPending
	}
	if c.cardRetries >= headlessCardRetryBudget {
		return 0, 0, armBudgetSpent
	}
	c.cardRetries++
	// Read under the same lock the tests write it under, and hand the value back
	// so the log line cannot disagree with the interval actually armed.
	delay := c.cardRetryDelay()
	c.cardRetryTimer = time.AfterFunc(delay, func() { c.fireCardRetry(ctx) })
	return c.cardRetries, delay, armArmed
}

// resetCardRetry clears the budget after a successful store read, so a later
// outage on the same child starts from a full budget rather than a spent one.
func (c *child) resetCardRetry() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.cardRetries = 0
}

// cardRetryDelay is the re-check interval, overridable so a test can exercise
// the stop-after-budget path without waiting a real minute.
func (c *child) cardRetryDelay() time.Duration {
	if c.cardRetryDelayValue > 0 {
		return c.cardRetryDelayValue
	}
	return headlessCardRetryDelay
}

// fireCardRetry runs one armed re-check. It clears the handle first so a later
// failure can arm a fresh one, and it refuses to act on a child that has gone.
func (c *child) fireCardRetry(ctx context.Context) {
	c.mutex.Lock()
	c.cardRetryTimer = nil
	c.mutex.Unlock()
	// A cancelled context means the daemon is going away, so re-reading the store
	// for up to five more minutes would outlive the process it is meant to serve.
	if c.procGone.Load() || ctx.Err() != nil {
		return
	}
	c.closeHeadlessTurnEnd(ctx)
}

// stopCardRetryLocked cancels an armed re-check. The caller must hold c.mutex —
// `reap` calls it from inside its own critical section.
func (c *child) stopCardRetryLocked() {
	if c.cardRetryTimer != nil {
		c.cardRetryTimer.Stop()
		c.cardRetryTimer = nil
	}
}

// cardVerdict is what one attention-store read decided for a turn end.
type cardVerdict int

const (
	// cardProceed: no card names the child, or no store is configured — stop it.
	cardProceed cardVerdict = iota
	// cardKeepOpen: an open card names the child — keep it.
	cardKeepOpen
	// cardStoreUnreadable: the store could not be read. Arming the bounded
	// re-check — or stopping once its budget is spent — is the caller's call,
	// because the budget is charged when a timer arms, not when a read fails.
	cardStoreUnreadable
)

// cardVerdict reads the attention store once and reports what it decided. It is
// split out of closeHeadlessTurnEnd to keep that function readable: the store
// error, the retry budget and the card test nested inline produced a branch tree
// the linter refused, and the split names each outcome instead of nesting it.
func (c *child) cardVerdict(ctx context.Context) (cardVerdict, error) {
	cards, err := c.openCards.OpenSessionIDs(ctx)
	if err != nil {
		return cardStoreUnreadable, err
	}
	c.resetCardRetry()
	if _, open := cards[c.id]; open {
		return cardKeepOpen, nil
	}
	return cardProceed, nil
}

// closeHeadlessTurnEnd stops a headless worker whose turn has closed with
// nothing pending — the Go daemon's end of life for the headless spawn path.
// It is the daemon-side counterpart of the node server's `endWorkerIfIdle`,
// which makes the same stop decision for the spawn path it drives. The daemon
// adds the open-card keep and one zero-turn retry (see retryZeroTurn): a turn
// that produced no assistant message is re-sent once before the child is
// stopped, because that turn is a failure, not a finished turn.
//
// ⚠️ EVERY keep arm is logged. A silent keep would make a stop line
// indistinguishable from a keep.
func (c *child) closeHeadlessTurnEnd(ctx context.Context) {
	// A retry timer can outlive the child by up to one delay. Acting on a dead
	// child is not merely wasteful: the pid may already be REUSED, and this path
	// ends in a SIGKILL. The gate is the process-gone flag rather than the state,
	// because the flag is set by the watcher before `reap` settles the record —
	// the window where state still reads idle on a child that has already exited.
	if c.procGone.Load() {
		log.Printf("agent %s: turn closed — the process is already gone", c.id)
		return
	}
	// A turn that produced no assistant message is a FAILED turn, not a finished
	// one, and it is the one case where stopping the child destroys work that
	// still exists: the prompt has been consumed and nothing came back from it.
	// Re-sending it once is the remedy — a stream that died mid-flight is
	// transient, and the alternative is a worker that looks like it ran and wrote
	// nothing.
	if c.retryZeroTurn(ctx) {
		return
	}
	// The card check runs BEFORE the decision is committed, and before the lock
	// is taken: it is I/O, and holding c.mutex across a store round trip would
	// stall every event on the child. An open card means the worker is waiting on
	// the operator; stopping it would orphan exactly that card. A store that
	// cannot be read is retried on a bounded schedule and then stopped anyway —
	// see armCardRetry, which owns both the budget and the timer.
	//
	// ⚠️ **Accepted TOCTOU, and it is the store's own latency wide.** A card that
	// opens between this read and the kill is not seen. Closing it would mean
	// holding c.mutex across the store round trip, which trades a rare orphaned
	// card for a stall on every event of every child; the node server's
	// `endWorkerIfIdle` accepts the same window. The read is deliberately placed
	// as late as it can be — after the cheap in-process checks would have kept the
	// child anyway — so the window is only ever as long as one store call.
	if c.openCards != nil {
		switch verdict, err := c.cardVerdict(ctx); verdict {
		case cardKeepOpen:
			log.Printf("agent %s: keeping worker — open-card", c.id)
			return
		case cardStoreUnreadable:
			attempt, delay, outcome := c.armCardRetry(ctx)
			switch outcome {
			case armAlreadyPending:
				// A re-check is already armed for this child: this turn end is a
				// duplicate, not a new attempt. The armed timer owns the next step,
				// and no budget slot is charged for this one.
				return
			case armArmed:
				log.Printf(
					"WARNING: could not read the attention store for %s; keeping the session and re-checking in %s (attempt %d of %d): %v",
					c.id, delay, attempt, headlessCardRetryBudget, err,
				)
				return
			case armBudgetSpent:
				log.Printf(
					"WARNING: attention store still unreadable for %s after %d attempts — stopping the session anyway; an open card, if any, is orphaned: %v",
					c.id, headlessCardRetryBudget, err,
				)
				// Fall through to the stop: the budget is spent, and a worker that
				// can never be stopped because a store is down is the leak this path
				// exists to remove.
			}
		}
	}

	c.mutex.Lock()
	// Re-read under the lock: a turn may have been written while the card check
	// ran, and that turn must not be cut off.
	if reason, keep := c.keepReasonLocked(); keep {
		c.mutex.Unlock()
		if reason == reasonAlreadyExited {
			log.Printf("agent %s: turn closed — session already exited", c.id)
			return
		}
		log.Printf("agent %s: keeping worker — %s", c.id, reason)
		return
	}
	// ⚠️ The entry gate is NOT enough. A store read sits between it and this point,
	// and the watcher sets `procGone` before `reap` settles the record — so a child
	// that exited during that read still reads StateIdle here, while its pid may
	// already be REUSED. This path ends in a SIGKILL, so the gate is re-read inside
	// the critical section, immediately before the kill.
	if c.procGone.Load() {
		c.mutex.Unlock()
		log.Printf("agent %s: turn closed — the process is already gone", c.id)
		return
	}
	// Commit: from here `send` refuses a new turn, so the kill cannot lose one.
	c.ending = true
	turns := c.zeroTurns.turns
	killErr := c.killLocked("headless-turn-end")
	if killErr != nil {
		c.ending = false
	}
	c.mutex.Unlock()

	if killErr != nil {
		log.Printf("WARNING: could not stop %s after its turn closed: %v", c.id, killErr)
		return
	}
	// ⚠️ The zero-turn close gets its own line. "nothing pending" is true of both
	// cases and reads as a normal end, which is exactly how a worker that produced
	// nothing stayed invisible: the operator saw a clean stop and an empty result.
	if turns == 0 {
		log.Printf(
			"agent %s: turn closed with ZERO assistant turns — stopping the session; "+
				"it produced no output (retry budget spent)",
			c.id,
		)
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
	if reason, keep := c.keepReasonLocked(); keep {
		return false, reason, nil
	}
	if err := c.killLocked("close-on-terminal-task"); err != nil {
		return false, "", errors.Wrapf(ctx, err, "kill session %q failed", c.id)
	}
	return true, "", nil
}
