// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type fakeAnchoredTaskResolver struct {
	result AnchoredTask
	err    error
	calls  int
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

	ginkgo.It("ignores non-md files and subdirectories", func() {
		write("a.txt", "---\nclaude_session_id: "+sid+"\n---\n")
		gomega.Expect(os.Mkdir(filepath.Join(dir, "sub"), 0o700)).To(gomega.Succeed())
		write("sub/a.md", "---\nclaude_session_id: "+sid+"\n---\n")
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
