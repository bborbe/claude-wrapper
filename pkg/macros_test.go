// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// scriptedSupervisor answers each Send with the child's own turn events, so a
// spec observes the sequencer's advance rule without a real child.
type scriptedSupervisor struct {
	mutex sync.Mutex
	sent  []string
	live  chan Event
	// emitted is the replay a real Subscribe hands back. Without it a second
	// run would restart its turn counter at zero while the child is already
	// three turns in.
	emitted []Event
	// closeOnSend models a child that leaves: the stream ends instead of the
	// turn closing.
	closeOnSend bool
	// state is what Get reports; empty means idle.
	state State
	// pendingTurns is what Get reports as in flight but not yet observed.
	pendingTurns int
	// omitIndex models a child whose result envelope carries no result_index,
	// which is the shape the fallback path has to handle.
	omitIndex bool
	// neverClose models a child whose turn never ends, which is what the step
	// timeout exists for.
	neverClose bool
}

func (s *scriptedSupervisor) Spawn(context.Context, SpawnOptions) (*Session, error) {
	return nil, nil
}

func (s *scriptedSupervisor) List(context.Context) ([]Session, error) { return nil, nil }

func (s *scriptedSupervisor) Get(context.Context, string) (*Session, error) {
	state := s.state
	if state == "" {
		state = StateIdle
	}
	return &Session{ID: "s1", State: state, PendingTurns: s.pendingTurns}, nil
}

func (s *scriptedSupervisor) Answer(context.Context, string, string, string) error { return nil }

func (s *scriptedSupervisor) RegisterQuestion(context.Context, string, Question) error {
	return nil
}

func (s *scriptedSupervisor) PollAnswer(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (s *scriptedSupervisor) Stop(context.Context, string) error { return nil }

// Send records the write and immediately emits the turn it closes, which is
// what a child does from the sequencer's point of view.
func (s *scriptedSupervisor) Send(_ context.Context, _ string, text string) error {
	s.mutex.Lock()
	index := len(s.sent)
	s.sent = append(s.sent, text)
	s.mutex.Unlock()
	if s.closeOnSend {
		close(s.live)
		return nil
	}
	s.emit(Event{"type": "assistant"})
	if s.neverClose {
		return nil
	}
	if s.omitIndex {
		s.emit(Event{"type": "result"})
		return nil
	}
	s.emit(Event{"type": "result", "result_index": float64(index)})
	return nil
}

// emit publishes one event to the live channel and keeps it for replay.
func (s *scriptedSupervisor) emit(event Event) {
	s.mutex.Lock()
	s.emitted = append(s.emitted, event)
	s.mutex.Unlock()
	s.live <- event
}

func (s *scriptedSupervisor) Subscribe(
	context.Context, string,
) ([]Event, <-chan Event, func(), error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	replay := make([]Event, len(s.emitted))
	copy(replay, s.emitted)
	return replay, s.live, func() {}, nil
}

var _ = ginkgo.Describe("Sequencer", func() {
	var supervisor *scriptedSupervisor
	var sequencer Sequencer

	ginkgo.BeforeEach(func() {
		supervisor = &scriptedSupervisor{live: make(chan Event, 64)}
		sequencer = NewSequencer(supervisor, libtime.NewCurrentDateTime(), NewSessionLocks(), 5*time.Second)
	})

	ginkgo.It("writes every step, in order, and closes each turn", func() {
		result, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Aborted).To(gomega.BeFalse())
		gomega.Expect(supervisor.sent).To(gomega.Equal(AvailableMacros()[0].Steps))
		for _, step := range result.Steps {
			gomega.Expect(step.Status).To(gomega.Equal(StepDone))
			gomega.Expect(step.WroteAt).NotTo(gomega.BeNil())
			gomega.Expect(step.ClosedAt).NotTo(gomega.BeNil())
		}
	})

	// The abort contract: a child that leaves mid-run stops the sequence, and
	// the response names every step that never ran.
	ginkgo.It("aborts and names the un-run steps when the child leaves", func() {
		supervisor.closeOnSend = true

		result, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrSessionExited)).To(gomega.BeTrue())
		gomega.Expect(result.Aborted).To(gomega.BeTrue())
		gomega.Expect(supervisor.sent).To(gomega.HaveLen(1))
		gomega.Expect(result.Steps[0].Status).To(gomega.Equal(StepAborted))
		gomega.Expect(result.Steps[1].Status).To(gomega.Equal(StepNotRun))
		gomega.Expect(result.Steps[2].Status).To(gomega.Equal(StepNotRun))
	})

	// Admission control: a run started mid-turn would read the in-flight turn's
	// result as step 1's close and fire every later step one turn early.
	ginkgo.It("refuses a child that is mid-turn", func() {
		supervisor.state = StateGenerating

		result, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(errors.Is(err, ErrSessionNotIdle)).To(gomega.BeTrue())
		gomega.Expect(result).To(gomega.BeNil())
		gomega.Expect(supervisor.sent).To(gomega.BeEmpty())
	})

	// A truncated replay must not undercount: the last retained result's own
	// index is the truth, because the earliest results are dropped once the
	// buffer is full.
	ginkgo.It("derives the next turn from the last retained result index", func() {
		replay := []Event{
			{"type": "result", "result_index": float64(5000)},
			{"type": "result", "result_index": float64(5001)},
		}

		gomega.Expect(expectedTurn(replay)).To(gomega.Equal(5002))
	})

	// The window State cannot see: a turn written a moment ago still reads
	// idle until the child's message_start arrives, so the counter decides.
	ginkgo.It("refuses a child with a turn already pending", func() {
		supervisor.pendingTurns = 1

		result, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(errors.Is(err, ErrSessionNotIdle)).To(gomega.BeTrue())
		gomega.Expect(result).To(gomega.BeNil())
		gomega.Expect(supervisor.sent).To(gomega.BeEmpty())
	})

	// A freshly spawned child sits in `starting` until its first turn: it has
	// no turn in flight, so refusing it would refuse the normal first run.
	ginkgo.It("accepts a child that has not started a turn", func() {
		supervisor.state = StateStarting

		result, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Aborted).To(gomega.BeFalse())
		gomega.Expect(supervisor.sent).To(gomega.Equal(AvailableMacros()[0].Steps))
	})

	// The fallback path: a child whose result envelope carries no result_index
	// still closes a turn, which is safe only because the run started idle.
	ginkgo.It("closes a turn when the child omits result_index", func() {
		supervisor.omitIndex = true

		result, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Aborted).To(gomega.BeFalse())
		gomega.Expect(supervisor.sent).To(gomega.Equal(AvailableMacros()[0].Steps))
		for _, step := range result.Steps {
			gomega.Expect(step.Status).To(gomega.Equal(StepDone))
		}
	})

	// A turn that never closes must abort rather than hold the run forever.
	ginkgo.It("aborts a step whose turn never closes", func() {
		supervisor.neverClose = true
		short := NewSequencer(
			supervisor, libtime.NewCurrentDateTime(), NewSessionLocks(), 50*time.Millisecond)

		result, err := short.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(result.Aborted).To(gomega.BeTrue())
		gomega.Expect(result.Steps[0].Status).To(gomega.Equal(StepAborted))
		gomega.Expect(result.Steps[1].Status).To(gomega.Equal(StepNotRun))
	})

	// The per-session lock: two runs on one session must serialize, so their
	// steps never interleave into a duplicated production-touching command.
	ginkgo.It("serializes two runs on the same session", func() {
		steps := AvailableMacros()[0].Steps
		var wait sync.WaitGroup
		for range 2 {
			wait.Add(1)
			go func() {
				defer ginkgo.GinkgoRecover()
				defer wait.Done()
				_, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}()
		}
		wait.Wait()

		supervisor.mutex.Lock()
		defer supervisor.mutex.Unlock()
		gomega.Expect(supervisor.sent).To(gomega.HaveLen(2 * len(steps)))
		gomega.Expect(supervisor.sent[:len(steps)]).To(gomega.Equal(steps))
		gomega.Expect(supervisor.sent[len(steps):]).To(gomega.Equal(steps))
	})

	// An advance latency far below the timeout is the whole point: the next
	// write follows the previous turn's close, not a fixed interval.
	ginkgo.It("advances with a gap far below the step timeout", func() {
		result, err := sequencer.Run(context.Background(), "s1", AvailableMacros()[0])

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for index := 0; index+1 < len(result.Steps); index++ {
			closed := result.Steps[index].ClosedAt.Time()
			wrote := result.Steps[index+1].WroteAt.Time()
			gomega.Expect(wrote).NotTo(gomega.BeTemporally("<", closed))
			gomega.Expect(wrote.Sub(closed)).To(gomega.BeNumerically("<", time.Second))
		}
	})
})

var _ = ginkgo.Describe("LookupMacro", func() {
	ginkgo.It("finds a defined macro", func() {
		macro, err := LookupMacro("compact")

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(macro.Steps).To(gomega.HaveLen(3))
		gomega.Expect(macro.Steps[1]).To(gomega.Equal("/compact"))
	})

	ginkgo.It("refuses an undefined macro", func() {
		_, err := LookupMacro("nope")

		gomega.Expect(errors.Is(err, ErrUnknownMacro)).To(gomega.BeTrue())
	})

	// The production-touching macros may not run fire-and-forget; the probe
	// macros carry no such step and are the endpoint's smoke test.
	ginkgo.It("requires confirmation only for a macro with a touching step", func() {
		for _, macro := range AvailableMacros() {
			want := macro.Name == "compact" || macro.Name == "terminal"
			gomega.Expect(macro.RequiresConfirmation()).To(gomega.Equal(want), macro.Name)
		}
	})

	ginkgo.It("defines no empty step", func() {
		for _, macro := range AvailableMacros() {
			for _, step := range macro.Steps {
				gomega.Expect(step).NotTo(gomega.BeEmpty(), macro.Name)
			}
		}
	})
})
