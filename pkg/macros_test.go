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
	// closeOnSend models a child that leaves: the stream ends instead of the
	// turn closing.
	closeOnSend bool
}

func (s *scriptedSupervisor) Spawn(context.Context, SpawnOptions) (*Session, error) {
	return nil, nil
}

func (s *scriptedSupervisor) List(context.Context) ([]Session, error) { return nil, nil }

func (s *scriptedSupervisor) Get(context.Context, string) (*Session, error) { return nil, nil }

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
	s.live <- Event{"type": "assistant"}
	s.live <- Event{"type": "result", "result_index": float64(index)}
	return nil
}

func (s *scriptedSupervisor) Subscribe(
	context.Context, string,
) ([]Event, <-chan Event, func(), error) {
	return nil, s.live, func() {}, nil
}

var _ = ginkgo.Describe("Sequencer", func() {
	var supervisor *scriptedSupervisor
	var sequencer Sequencer

	ginkgo.BeforeEach(func() {
		supervisor = &scriptedSupervisor{live: make(chan Event, 64)}
		sequencer = NewSequencer(supervisor, libtime.NewCurrentDateTime(), 5*time.Second)
	})

	ginkgo.It("writes every step, in order, and closes each turn", func() {
		result, err := sequencer.Run(context.Background(), "s1", Macros[0])

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Aborted).To(gomega.BeFalse())
		gomega.Expect(supervisor.sent).To(gomega.Equal(Macros[0].Steps))
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

		result, err := sequencer.Run(context.Background(), "s1", Macros[0])

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrSessionExited)).To(gomega.BeTrue())
		gomega.Expect(result.Aborted).To(gomega.BeTrue())
		gomega.Expect(supervisor.sent).To(gomega.HaveLen(1))
		gomega.Expect(result.Steps[0].Status).To(gomega.Equal(StepAborted))
		gomega.Expect(result.Steps[1].Status).To(gomega.Equal(StepNotRun))
		gomega.Expect(result.Steps[2].Status).To(gomega.Equal(StepNotRun))
	})

	// An advance latency far below the timeout is the whole point: the next
	// write follows the previous turn's close, not a fixed interval.
	ginkgo.It("advances with a gap far below the step timeout", func() {
		result, err := sequencer.Run(context.Background(), "s1", Macros[0])

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

	// Every macro the wrapper ships can rewrite a context or end a child, so
	// none of them may run fire-and-forget.
	ginkgo.It("requires confirmation for every defined macro", func() {
		for _, macro := range Macros {
			gomega.Expect(macro.RequiresConfirmation()).To(gomega.BeTrue(), macro.Name)
		}
	})

	ginkgo.It("defines no empty step", func() {
		for _, macro := range Macros {
			for _, step := range macro.Steps {
				gomega.Expect(step).NotTo(gomega.BeEmpty(), macro.Name)
			}
		}
	})
})
