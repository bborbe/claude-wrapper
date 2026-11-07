// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// boardSupervisor is a Supervisor whose List and Answer are the only methods the
// delivery path reaches. The embedded interface supplies the rest and panics if
// a test ever drives a method it did not fake, which is the signal we want.
type boardSupervisor struct {
	Supervisor
	sessions  []Session
	listErr   error
	answerErr error
	answered  []Answer
}

func (b *boardSupervisor) List(ctx context.Context) ([]Session, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.sessions, nil
}

func (b *boardSupervisor) Answer(
	ctx context.Context,
	id string,
	requestID string,
	answer Answer,
) error {
	if b.answerErr != nil {
		return b.answerErr
	}
	b.answered = append(b.answered, answer)
	return nil
}

// boardStore is the attention store in memory.
type fakeBoardStore struct {
	items     []BoardItem
	getErr    error
	attempts  []string
	openCalls int
}

func (s *fakeBoardStore) Open(ctx context.Context) ([]BoardItem, error) {
	s.openCalls++
	return s.items, nil
}

func (s *fakeBoardStore) Get(ctx context.Context, itemID string) (BoardItem, error) {
	if s.getErr != nil {
		return BoardItem{}, s.getErr
	}
	for _, item := range s.items {
		if item.ItemID == itemID {
			return item, nil
		}
	}
	return BoardItem{}, stderrors.New("no such item " + itemID)
}

func (s *fakeBoardStore) RecordAttempt(
	ctx context.Context,
	itemID string,
	carrier string,
	outcome string,
) error {
	s.attempts = append(s.attempts, itemID+"="+outcome)
	return nil
}

// parkedSession is one headless child holding a permission park.
func parkedSession(mode string) *Session {
	return &Session{
		ID:        "session-1",
		Cwd:       "/tmp",
		Mode:      mode,
		StartedAt: time.Now(),
		Question: &Question{
			RequestID:   "req-1",
			ToolName:    "Bash",
			Kind:        AnswerKindPermission,
			RequestedAt: time.Now(),
		},
	}
}

func permissionItem(resolvedBy string) BoardItem {
	return BoardItem{
		ItemID:          "item-1",
		State:           "answered",
		AnswerMechanism: "permission",
		ProducerID:      "session-1",
		Decision:        "allow",
		ResolvedBy:      resolvedBy,
	}
}

var _ = ginkgo.Describe("board delivery", func() {
	ginkgo.Describe("the arm guard", func() {
		// ⚠️ A permission approval EXECUTES a command. This is the one class a
		// scripted click must not be able to take, so the refusal is pinned here
		// rather than left to the integration drill.
		ginkgo.It("refuses a permission answer for a non-headless producer with no resolved_by", func() {
			session := parkedSession("")
			answer, ok := answerFor(permissionItem(""), "permission", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeFalse())
			gomega.Expect(answer).To(gomega.Equal(Answer{}))
		})

		ginkgo.It("releases a headless producer's park", func() {
			session := parkedSession("headless")
			answer, ok := answerFor(permissionItem(""), "permission", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(answer.Behavior).To(gomega.Equal("allow"))
			gomega.Expect(answer.Kind).To(gomega.Equal(AnswerKindPermission))
		})

		ginkgo.It("releases a non-headless producer's park when the item carries resolved_by", func() {
			session := parkedSession("")
			answer, ok := answerFor(permissionItem("operator"), "permission", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(answer.Behavior).To(gomega.Equal("allow"))
		})

		ginkgo.It("refuses an item that carries no decision", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.Decision = "maybe"

			_, ok := answerFor(item, "permission", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeFalse())
		})
	})

	ginkgo.Describe("freshness", func() {
		ginkgo.It("refuses an answer that predates the park it would settle", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.CreatedAt = session.Question.RequestedAt.Add(-time.Hour).Format(time.RFC3339)

			_, ok := answerFor(item, "permission", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeFalse())
		})

		ginkgo.It("settles an answer given during the park", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.CreatedAt = session.Question.RequestedAt.Add(time.Second).Format(time.RFC3339)

			_, ok := answerFor(item, "permission", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeTrue())
		})
	})

	ginkgo.Describe("the message arm", func() {
		ginkgo.It("answers deny with the operator prefix, never allow", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.AnswerMechanism = "message"
			item.Answer = &BoardAnswer{Kind: "text", Value: "use the staging bucket"}

			answer, ok := answerFor(item, "message", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeTrue())
			// An allow would run an AskUserQuestion in a tty-less session, where it
			// waits and the worker exits unanswered.
			gomega.Expect(answer.Behavior).To(gomega.Equal("deny"))
			gomega.Expect(answer.Message).
				To(gomega.Equal(OperatorPrefix + " use the staging bucket"))
		})

		ginkgo.It("refuses a message card with no answer content", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.AnswerMechanism = "message"

			_, ok := answerFor(item, "message", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeFalse())
		})
	})

	ginkgo.Describe("tick", func() {
		ginkgo.It("drops a settled item from the watch set", func() {
			supervisor := &boardSupervisor{sessions: []Session{*parkedSession("headless")}}
			store := &fakeBoardStore{items: []BoardItem{permissionItem("")}}
			delivery := &BoardDelivery{Supervisor: supervisor, Store: store}
			watching := map[string]string{}

			delivery.tick(context.Background(), watching)

			gomega.Expect(watching).To(gomega.BeEmpty())
			gomega.Expect(supervisor.answered).To(gomega.HaveLen(1))
			gomega.Expect(store.attempts).To(gomega.Equal([]string{"item-1=delivered"}))
		})

		// ⚠️ Deleting before delivering would discard the operator's answer on a
		// transient failure and the park would fall through to its fail-open
		// timeout. The item must stay watched so the next tick retries it.
		ginkgo.It("keeps a failed delivery watched for the next tick", func() {
			supervisor := &boardSupervisor{
				sessions:  []Session{*parkedSession("headless")},
				answerErr: stderrors.New("transport hiccup"),
			}
			store := &fakeBoardStore{items: []BoardItem{permissionItem("")}}
			delivery := &BoardDelivery{Supervisor: supervisor, Store: store}
			watching := map[string]string{}

			delivery.tick(context.Background(), watching)

			gomega.Expect(watching).To(gomega.HaveKey("item-1"))
			gomega.Expect(store.attempts).To(gomega.Equal([]string{"item-1=failed"}))
		})

		ginkgo.It("leaves a park alone when the store cannot be read", func() {
			supervisor := &boardSupervisor{sessions: []Session{*parkedSession("headless")}}
			store := &fakeBoardStore{items: []BoardItem{permissionItem("")}, getErr: stderrors.New("gone")}
			delivery := &BoardDelivery{Supervisor: supervisor, Store: store}
			watching := map[string]string{}

			delivery.tick(context.Background(), watching)

			gomega.Expect(supervisor.answered).To(gomega.BeEmpty())
			gomega.Expect(watching).To(gomega.BeEmpty())
		})
	})
})
