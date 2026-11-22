// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	stderrors "errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// syncBuffer is a race-free log sink. The stop path logs from its own goroutine
// while the spec reads what was captured, so a bare bytes.Buffer would race.
type syncBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

// recordWriter is a stdin stand-in that keeps what send wrote, so a spec can
// assert the retry really re-sent the prompt instead of only logging that it
// would have.
type recordWriter struct {
	mutex  sync.Mutex
	writes []string
}

func (w *recordWriter) Write(p []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *recordWriter) Close() error { return nil }

func (w *recordWriter) count() int {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return len(w.writes)
}

type fakeAnchoredTaskResolver struct {
	result AnchoredTask
	err    error
	calls  int
}

// fakeOpenCards stands in for the attention store: ids is the open-card set and
// err is a store that cannot be read.
type fakeOpenCards struct {
	ids map[string]struct{}
	err error
}

func (f *fakeOpenCards) OpenSessionIDs(_ context.Context) (map[string]struct{}, error) {
	return f.ids, f.err
}

// flipOpenCards is a store that fails the first `fail` reads and then answers, so
// a spec can exercise the re-check recovering rather than only the give-up.
type flipOpenCards struct {
	mutex sync.Mutex
	fail  int
	ids   map[string]struct{}
}

func (f *flipOpenCards) OpenSessionIDs(context.Context) (map[string]struct{}, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.fail > 0 {
		f.fail--
		return nil, stderrors.New("store down")
	}
	return f.ids, nil
}

func (f *fakeAnchoredTaskResolver) Resolve(
	_ context.Context,
	_ string,
) (AnchoredTask, error) {
	f.calls++
	return f.result, f.err
}

var _ = ginkgo.Describe("DirAnchoredTaskResolver", func() {
	const sid = "11111111-2222-3333-4444-555555555555"
	var (
		ctx context.Context
		dir string
	)
	write := func(name, content string) {
		gomega.Expect(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)).To(gomega.Succeed())
	}
	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir = ginkgo.GinkgoT().TempDir()
	})

	ginkgo.It("matches an unquoted session id in frontmatter", func() {
		write("a.md", "---\nstatus: completed\ndefer_date: 2026-11-14\nclaude_session_id: "+sid+"\n---\nbody\n")
		write("b.md", "---\nstatus: in_progress\nclaude_session_id: other\n---\n")
		result, err := NewDirAnchoredTaskResolver([]string{dir}).Resolve(ctx, sid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Matches).To(gomega.Equal(1))
		gomega.Expect(result.Status).To(gomega.Equal("completed"))
		gomega.Expect(result.DeferDate).To(gomega.Equal("2026-11-14"))
		gomega.Expect(result.Path).To(gomega.Equal(filepath.Join(dir, "a.md")))
	})

	ginkgo.It("matches quoted values", func() {
		write("a.md", "---\nstatus: \"aborted\"\nclaude_session_id: '"+sid+"'\n---\n")
		result, err := NewDirAnchoredTaskResolver([]string{dir}).Resolve(ctx, sid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Matches).To(gomega.Equal(1))
		gomega.Expect(result.Status).To(gomega.Equal("aborted"))
	})

	ginkgo.It("ignores the id in the body", func() {
		write("a.md", "---\nstatus: completed\n---\nclaude_session_id: "+sid+"\n")
		result, err := NewDirAnchoredTaskResolver([]string{dir}).Resolve(ctx, sid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Matches).To(gomega.Equal(0))
	})

	ginkgo.It("ignores non-md files", func() {
		write("a.txt", "---\nclaude_session_id: "+sid+"\n---\n")
		result, err := NewDirAnchoredTaskResolver([]string{dir}).Resolve(ctx, sid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Matches).To(gomega.Equal(0))
	})

	// ⚠️ This assertion is the boundary, and it is separate from the non-md case on
	// purpose. The glob is `*.md` and NON-recursive (`pkg/terminal_task.go`), so a task
	// file in a subdirectory is invisible to close-at-turn-end — `private-personal/25
	// Tasks/.sun` holds 12 such files today. A later "improvement" to a recursive glob
	// would change which sessions the fleet closes, and this is the test that stops that
	// happening silently.
	ginkgo.It("ignores a task file in a subdirectory", func() {
		gomega.Expect(os.Mkdir(filepath.Join(dir, "sub"), 0o700)).To(gomega.Succeed())
		write("sub/a.md", "---\nstatus: completed\nclaude_session_id: "+sid+"\n---\n")
		result, err := NewDirAnchoredTaskResolver([]string{dir}).Resolve(ctx, sid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Matches).To(gomega.Equal(0))
	})

	ginkgo.It("counts two matches across dirs", func() {
		other := ginkgo.GinkgoT().TempDir()
		write("a.md", "---\nclaude_session_id: "+sid+"\n---\n")
		gomega.Expect(os.WriteFile(filepath.Join(other, "b.md"),
			[]byte("---\nclaude_session_id: "+sid+"\n---\n"), 0o600)).To(gomega.Succeed())
		result, err := NewDirAnchoredTaskResolver([]string{dir, other}).Resolve(ctx, sid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Matches).To(gomega.Equal(2))
	})

	// ⚠️ The scoping boundary, and it is the counterpart of "counts two matches across
	// dirs" on purpose: that case configures BOTH dirs and expects 2, this one
	// configures ONE and puts the task file in the other. `private-agent/tasks` is
	// deliberately absent from the launcher's `-close-on-terminal-task-dirs` list, so a
	// task anchored there must not be resolved — otherwise widening the list would
	// silently become a no-op, and close-at-turn-end would stop sessions the operator
	// excluded. A resolver that globbed beyond its configured set fails here.
	ginkgo.It("ignores a task file in an unconfigured directory", func() {
		unconfigured := ginkgo.GinkgoT().TempDir()
		write("a.md", "---\nstatus: completed\nclaude_session_id: other\n---\n")
		gomega.Expect(os.WriteFile(filepath.Join(unconfigured, "b.md"),
			[]byte("---\nstatus: completed\nclaude_session_id: "+sid+"\n---\n"), 0o600)).To(gomega.Succeed())
		result, err := NewDirAnchoredTaskResolver([]string{dir}).Resolve(ctx, sid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Matches).To(gomega.Equal(0))
	})
})

var _ = ginkgo.Describe("close on terminal task", func() {
	var (
		ctx      context.Context
		resolver *fakeAnchoredTaskResolver
		cmd      *exec.Cmd
		c        *child
		exited   chan struct{}
	)
	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		resolver = &fakeAnchoredTaskResolver{}
		cmd = exec.Command("sleep", "30")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		exited = make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		c = &child{
			id:           "sid",
			pid:          cmd.Process.Pid,
			ctx:          ctx,
			taskResolver: resolver,
			session:      Session{ID: "sid", State: StateIdle},
		}
	})
	ginkgo.AfterEach(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	expectKilled := func() { gomega.Eventually(exited, 5*time.Second).Should(gomega.BeClosed()) }
	expectAlive := func() { gomega.Consistently(exited, 200*time.Millisecond).ShouldNot(gomega.BeClosed()) }

	ginkgo.It("stops on completed", func() {
		resolver.result = AnchoredTask{Matches: 1, Status: "completed"}
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeTrue())
		expectKilled()
	})
	ginkgo.It("stops on aborted", func() {
		resolver.result = AnchoredTask{Matches: 1, Status: "aborted"}
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeTrue())
		expectKilled()
	})
	ginkgo.It("keeps in_progress", func() {
		resolver.result = AnchoredTask{Matches: 1, Status: "in_progress"}
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeFalse())
		expectAlive()
	})
	ginkgo.It("keeps hold", func() {
		resolver.result = AnchoredTask{Matches: 1, Status: "hold"}
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeFalse())
		expectAlive()
	})
	ginkgo.It("keeps on multiple matches", func() {
		resolver.result = AnchoredTask{Matches: 2, Status: "completed"}
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeFalse())
		expectAlive()
	})
	ginkgo.It("keeps when a turn reopened", func() {
		resolver.result = AnchoredTask{Matches: 1, Status: "completed"}
		c.pendingTurns = 1
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeFalse())
		c.pendingTurns = 0
		c.session.State = StateGenerating
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeFalse())
		expectAlive()
	})
	ginkgo.It("keeps while a question is pending", func() {
		resolver.result = AnchoredTask{Matches: 1, Status: "completed"}
		c.session.Question = &Question{RequestID: "r1"}
		gomega.Expect(c.closeIfTaskTerminal(ctx)).To(gomega.BeFalse())
		expectAlive()
	})
	ginkgo.It("never resolves when the feature is off", func() {
		c.taskResolver = nil
		c.checkTerminalTask()
		expectAlive()
		gomega.Expect(resolver.calls).To(gomega.Equal(0))
	})
	ginkgo.It("resolves asynchronously at turn end when on", func() {
		resolver.result = AnchoredTask{Matches: 1, Status: "completed"}
		c.mutex.Lock()
		c.checkTerminalTask()
		c.mutex.Unlock()
		expectKilled()
	})
})

// stopIfIdleAndUnparked's reason strings are the evidence a keep is logged
// against, so each arm is pinned here. A silent or renamed arm would make a
// stop line indistinguishable from a keep.
var _ = ginkgo.Describe("stopIfIdleAndUnparked reasons", func() {
	var (
		ctx    context.Context
		cmd    *exec.Cmd
		c      *child
		exited chan struct{}
	)
	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		cmd = exec.Command("sleep", "30")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		exited = make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		c = &child{
			id:      "sid",
			pid:     cmd.Process.Pid,
			ctx:     ctx,
			session: Session{ID: "sid", State: StateIdle},
		}
	})
	ginkgo.AfterEach(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	ginkgo.It("reports already exited", func() {
		c.session.State = StateExited
		stopped, reason, err := c.stopIfIdleAndUnparked(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stopped).To(gomega.BeFalse())
		gomega.Expect(reason).To(gomega.Equal("already exited"))
	})
	ginkgo.It("reports a turn is open when a turn is pending", func() {
		c.pendingTurns = 1
		stopped, reason, err := c.stopIfIdleAndUnparked(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stopped).To(gomega.BeFalse())
		gomega.Expect(reason).To(gomega.Equal("a turn is open"))
	})
	ginkgo.It("reports a turn is open when the session is generating", func() {
		c.session.State = StateGenerating
		stopped, reason, err := c.stopIfIdleAndUnparked(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stopped).To(gomega.BeFalse())
		gomega.Expect(reason).To(gomega.Equal("a turn is open"))
	})
	ginkgo.It("reports a question is pending", func() {
		c.session.Question = &Question{RequestID: "r1"}
		stopped, reason, err := c.stopIfIdleAndUnparked(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stopped).To(gomega.BeFalse())
		gomega.Expect(reason).To(gomega.Equal("a question is pending"))
	})
	ginkgo.It("stops with no reason when idle and unparked", func() {
		stopped, reason, err := c.stopIfIdleAndUnparked(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stopped).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.BeEmpty())
		gomega.Eventually(exited, 5*time.Second).Should(gomega.BeClosed())
	})
})

// The headless worker's turn end is its end of life. Managers and interactive
// sessions must never be stopped by it.
var _ = ginkgo.Describe("headless turn end", func() {
	var (
		ctx    context.Context
		cmd    *exec.Cmd
		c      *child
		exited chan struct{}
	)
	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		cmd = exec.Command("sleep", "30")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		exited = make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		c = &child{
			id:      "sid",
			pid:     cmd.Process.Pid,
			ctx:     ctx,
			session: Session{ID: "sid", State: StateIdle, Mode: SpawnModeHeadless, Role: RoleAgent},
			// A short interval so the stop-after-budget path is exercised without
			// waiting a real minute per attempt.
			cardRetryDelayValue: 20 * time.Millisecond,
		}
	})
	ginkgo.AfterEach(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	checkTurnEnd := func() {
		c.mutex.Lock()
		c.checkTerminalTask()
		c.mutex.Unlock()
	}
	expectKilled := func() { gomega.Eventually(exited, 5*time.Second).Should(gomega.BeClosed()) }
	expectAlive := func() { gomega.Consistently(exited, 200*time.Millisecond).ShouldNot(gomega.BeClosed()) }
	captureLog := func() *syncBuffer {
		sink := &syncBuffer{}
		previous := log.Writer()
		log.SetOutput(sink)
		ginkgo.DeferCleanup(func() { log.SetOutput(previous) })
		return sink
	}

	ginkgo.It("stops a headless agent whose turn closed with nothing pending", func() {
		c.zeroTurns.closed = &zeroTurn{text: "do the thing", turns: 3}
		checkTurnEnd()
		expectKilled()
	})
	// The defect this change exists for: a resumed worker's first turn came back
	// with no assistant message at all (the child's stream died mid-flight), and
	// the daemon read the resulting `result` as a normal turn end. The task file
	// was never touched and the work looked like a session that ran and produced
	// nothing. The turn is re-sent once instead.
	ginkgo.It("re-sends a turn that closed with no assistant message", func() {
		sink := captureLog()
		stdin := &recordWriter{}
		c.stdin = stdin
		c.zeroTurns.closed = &zeroTurn{text: "do the thing"}
		checkTurnEnd()
		expectAlive()
		gomega.Eventually(stdin.count, "2s").Should(gomega.Equal(1))
		gomega.Eventually(sink.String, "2s").Should(gomega.And(
			gomega.ContainSubstring("turn closed after 0 assistant turns"),
			gomega.ContainSubstring("re-sending the same turn"),
		))
	})
	ginkgo.It("does not re-send a turn that produced an assistant message", func() {
		stdin := &recordWriter{}
		c.stdin = stdin
		c.zeroTurns.closed = &zeroTurn{text: "do the thing", turns: 2}
		checkTurnEnd()
		expectKilled()
		gomega.Consistently(stdin.count, 200*time.Millisecond).Should(gomega.Equal(0))
	})
	ginkgo.It("stops and names the zero-turn close once the retry budget is spent", func() {
		sink := captureLog()
		c.stdin = &recordWriter{}
		c.zeroTurns.closed = &zeroTurn{text: "do the thing"}
		c.zeroTurns.retries = zeroTurnRetryBudget
		checkTurnEnd()
		expectKilled()
		// "nothing pending" is true of both closes and reads as a normal end —
		// which is how a worker that produced nothing stayed invisible.
		gomega.Eventually(sink.String, "2s").Should(gomega.ContainSubstring("ZERO assistant turns"))
	})
	ginkgo.It("stops a headless agent with no role (the default role)", func() {
		c.session.Role = ""
		checkTurnEnd()
		expectKilled()
	})
	ginkgo.It("never stops an interactive session", func() {
		c.session.Mode = SpawnModeInteractive
		checkTurnEnd()
		expectAlive()
	})
	ginkgo.It("never stops a headless manager", func() {
		c.session.Role = RoleManager
		checkTurnEnd()
		expectAlive()
	})
	ginkgo.It("never stops a headless human-role session", func() {
		c.session.Role = RoleHuman
		checkTurnEnd()
		expectAlive()
	})
	ginkgo.It("keeps a headless agent while a turn is open", func() {
		c.pendingTurns = 1
		checkTurnEnd()
		expectAlive()
	})
	ginkgo.It("keeps a headless agent while a question is pending", func() {
		c.session.Question = &Question{RequestID: "r1"}
		checkTurnEnd()
		expectAlive()
	})
	ginkgo.It("keeps a headless agent that has an open attention card", func() {
		c.openCards = &fakeOpenCards{ids: map[string]struct{}{"sid": {}}}
		checkTurnEnd()
		expectAlive()
	})
	ginkgo.It("keeps a headless agent when the attention store cannot be read", func() {
		sink := captureLog()
		// The re-check must be armed but never fire: this spec asserts the child is
		// KEPT. At the suite's 20ms delay the whole budget elapses inside the
		// assertion window and the child is stopped instead.
		c.mutex.Lock()
		c.cardRetryDelayValue = time.Hour
		c.mutex.Unlock()
		c.openCards = &fakeOpenCards{err: stderrors.New("store down")}
		checkTurnEnd()
		expectAlive()
		gomega.Eventually(func() int {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			return c.cardRetries
		}, "2s").Should(gomega.Equal(1))
		// The retry must be observable, not inferred: a reader has to be able to
		// tell "the store was unreadable and it is retrying" from a silent keep.
		gomega.Eventually(sink.String, "2s").Should(gomega.And(
			gomega.ContainSubstring("keeping the session and re-checking in"),
			gomega.ContainSubstring("(attempt 1 of 5)"),
		))
	})
	ginkgo.It("stops anyway once the store-error retry budget is spent", func() {
		sink := captureLog()
		c.openCards = &fakeOpenCards{err: stderrors.New("store down")}
		c.mutex.Lock()
		c.cardRetries = headlessCardRetryBudget
		c.mutex.Unlock()
		// A real turn produced output, so this is not the zero-turn path: the
		// store-budget stop is what this spec is measuring.
		c.zeroTurns.closed = &zeroTurn{text: "do the thing", turns: 2}
		checkTurnEnd()
		expectKilled()
		// The bound being applied is the fix's whole point, and both halves of it
		// are asserted: the give-up warning, and the stop line itself.
		gomega.Eventually(sink.String, "2s").Should(gomega.And(
			gomega.ContainSubstring("stopping the session anyway"),
			gomega.ContainSubstring("turn closed with nothing pending — stopping the session"),
		))
	})
	ginkgo.It("stops once the store-error retry budget is exhausted", func() {
		c.openCards = &fakeOpenCards{err: stderrors.New("store down")}
		checkTurnEnd()
		// The budget is 5 retries; each armed re-check re-reads the store and
		// re-arms, so the sixth read exhausts it and the child is stopped. The
		// stop is the assertion — asserting "still alive" first would race the
		// 20ms re-check interval this suite runs with.
		gomega.Eventually(exited, 10*time.Second).Should(gomega.BeClosed())
	})
	ginkgo.It("cancels an armed re-check when the child is reaped", func() {
		// A long interval keeps the timer ARMED: at the suite's 20ms delay it fires
		// and clears its own handle before the assertion reads it.
		c.mutex.Lock()
		c.cardRetryDelayValue = time.Hour
		c.mutex.Unlock()
		c.openCards = &fakeOpenCards{err: stderrors.New("store down")}
		checkTurnEnd()
		// closeHeadlessTurnEnd runs on its own goroutine, so the handle is nil until
		// that goroutine arms it — poll for the armed state instead of reading it
		// once, which races the goroutine and fails intermittently.
		gomega.Eventually(func() bool {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			return c.cardRetryTimer != nil
		}, "2s").Should(gomega.BeTrue())
		c.reap(nil)
		c.mutex.Lock()
		defer c.mutex.Unlock()
		gomega.Expect(c.cardRetryTimer).To(gomega.BeNil())
	})
	ginkgo.It("does not arm a second re-check while one is pending", func() {
		c.openCards = &fakeOpenCards{err: stderrors.New("store down")}
		c.mutex.Lock()
		c.cardRetryDelayValue = time.Hour
		c.mutex.Unlock()
		checkTurnEnd()
		checkTurnEnd()
		// A duplicate turn end inside the retry window must NOT charge a second
		// budget slot: the kept worker stays sendable, so a handful of turns would
		// otherwise spend the whole budget without a single re-check firing.
		// closeHeadlessTurnEnd runs on its own goroutine, so poll for the count.
		gomega.Eventually(func() int {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			return c.cardRetries
		}, "2s").Should(gomega.Equal(1))
		c.mutex.Lock()
		defer c.mutex.Unlock()
		gomega.Expect(c.cardRetryTimer).NotTo(gomega.BeNil())
	})
	ginkgo.It("stops on the re-check once the store recovers", func() {
		// The defect this row exists for: a store unreadable at turn end and
		// readable a moment later must still end with a stopped worker.
		c.openCards = &flipOpenCards{fail: 1, ids: map[string]struct{}{}}
		c.mutex.Lock()
		c.cardRetryDelayValue = 10 * time.Millisecond
		c.mutex.Unlock()
		checkTurnEnd()
		expectKilled()
	})
	ginkgo.It("refuses to stop a child whose process is already gone", func() {
		// The pid may already be REUSED, and this path ends in a SIGKILL — so a
		// child the watcher has already seen exit must never be signalled.
		c.openCards = &fakeOpenCards{ids: map[string]struct{}{}}
		c.procGone.Store(true)
		checkTurnEnd()
		expectAlive()
	})
	ginkgo.It("resets the store-error budget after a successful read", func() {
		c.openCards = &fakeOpenCards{ids: map[string]struct{}{}}
		c.mutex.Lock()
		c.cardRetries = 3
		c.mutex.Unlock()
		checkTurnEnd()
		expectKilled()
		c.mutex.Lock()
		defer c.mutex.Unlock()
		gomega.Expect(c.cardRetries).To(gomega.Equal(0))
	})
	ginkgo.It("stops when the attention store holds no card for it", func() {
		c.openCards = &fakeOpenCards{ids: map[string]struct{}{"other": {}}}
		checkTurnEnd()
		expectKilled()
	})
	ginkgo.It("refuses a turn once the headless stop has committed", func() {
		c.mutex.Lock()
		c.ending = true
		c.mutex.Unlock()
		err := c.send(ctx, "hello")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is ending"))
	})
	ginkgo.It("trims the role before exempting a manager", func() {
		c.session.Role = " manager "
		checkTurnEnd()
		expectAlive()
	})
})
