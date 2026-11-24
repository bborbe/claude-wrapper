// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
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
	openErr   error
	attempts  []string
	openCalls int
	answers   []string
	// answerRequestIDs records the request id each close named, so a spec can
	// assert the card was stamped with the gate it settled rather than only that
	// something answered it.
	answerRequestIDs []string
	answerErr        error
}

func (s *fakeBoardStore) Open(ctx context.Context) ([]BoardItem, error) {
	s.openCalls++
	return s.items, s.openErr
}

func (s *fakeBoardStore) Answer(
	ctx context.Context,
	itemID string,
	answeredBy string,
	resolvedBy string,
	decision string,
	requestID string,
) error {
	s.answers = append(s.answers, itemID+"="+answeredBy+":"+decision)
	s.answerRequestIDs = append(s.answerRequestIDs, requestID)
	return s.answerErr
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

		ginkgo.It("answers a manager's answer in the manager's voice, never the operator's", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.AnswerMechanism = "message"
			item.Answer = &BoardAnswer{
				Kind:     "text",
				Value:    "Manager answer, via supervisor: Approve",
				Answerer: "manager",
			}

			answer, ok := answerFor(item, "message", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(answer.Behavior).To(gomega.Equal("deny"))
			gomega.Expect(answer.Message).
				To(gomega.Equal("Manager answer, via supervisor: Approve"))
			gomega.Expect(answer.Message).NotTo(gomega.ContainSubstring("Operator answer"))
		})

		ginkgo.It("prefixes a bare manager label exactly once", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.AnswerMechanism = "message"
			item.Answer = &BoardAnswer{Kind: "option", Value: "Approve", Answerer: "manager"}

			answer, ok := answerFor(item, "message", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(answer.Message).
				To(gomega.Equal("Manager answer, via supervisor: Approve"))
		})

		ginkgo.It("answers an explicit operator answer exactly as before", func() {
			session := parkedSession("headless")
			item := permissionItem("")
			item.AnswerMechanism = "message"
			item.Answer = &BoardAnswer{Kind: "text", Value: "use the staging bucket", Answerer: "operator"}

			answer, ok := answerFor(item, "message", *session.Question, session)

			gomega.Expect(ok).To(gomega.BeTrue())
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

var _ = ginkgo.Describe("answer close", func() {
	ginkgo.Describe("settledCards", func() {
		park := &Question{
			RequestID:   "req-1",
			Kind:        AnswerKindPermission,
			ToolName:    "Bash",
			RequestedAt: time.Now(),
		}

		ginkgo.It("returns the one open permission card for the answered park", func() {
			cards := settledCards([]BoardItem{
				{ItemID: "a", State: "open", AnswerMechanism: "permission", ProducerID: "s1", RequestID: "req-1"},
				{ItemID: "b", State: "answered", AnswerMechanism: "permission", ProducerID: "s1", RequestID: "req-1"},
			}, "s1", park)
			gomega.Expect(cards).To(gomega.HaveLen(1))
			gomega.Expect(cards[0].ItemID).To(gomega.Equal("a"))
		})

		ginkgo.It("returns both when two open cards match the park", func() {
			cards := settledCards([]BoardItem{
				{ItemID: "a", State: "open", AnswerMechanism: "permission", ProducerID: "s1", RequestID: "req-1"},
				{ItemID: "b", State: "open", AnswerMechanism: "permission", ProducerID: "s1", RequestID: "req-1"},
			}, "s1", park)
			gomega.Expect(cards).To(gomega.HaveLen(2))
		})

		ginkgo.It("ignores another producer's card", func() {
			cards := settledCards([]BoardItem{
				{ItemID: "a", State: "open", AnswerMechanism: "permission", ProducerID: "s2", RequestID: "req-1"},
			}, "s1", park)
			gomega.Expect(cards).To(gomega.BeEmpty())
		})

		ginkgo.It("ignores a card that names a different request", func() {
			cards := settledCards([]BoardItem{
				{ItemID: "a", State: "open", AnswerMechanism: "permission", ProducerID: "s1", RequestID: "other"},
			}, "s1", park)
			gomega.Expect(cards).To(gomega.BeEmpty())
		})
	})

	ginkgo.Describe("boardStore.Answer", func() {
		ginkgo.It("posts answered_by, resolved_by, decision and the settled request id", func() {
			var got map[string]string
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			store := NewBoardStore(server.URL, nil)
			gomega.Expect(store.Answer(context.Background(), "item-1", "supervisor", "mgr-1", "allow", "req-1")).To(gomega.Succeed())
			gomega.Expect(path).To(gomega.Equal("/api/1.0/attention/item-1/answer"))
			gomega.Expect(got).To(gomega.Equal(map[string]string{
				"answered_by": "supervisor",
				"resolved_by": "mgr-1",
				"decision":    "allow",
				"request_id":  "req-1",
			}))
		})

		// A card class with no park behind it omits the field rather than sending
		// an empty string: the store's own "no gate recorded" state is the absent
		// key, and "" would read as a recorded value.
		ginkgo.It("omits request_id when no gate is named", func() {
			var got map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			store := NewBoardStore(server.URL, nil)
			gomega.Expect(store.Answer(context.Background(), "item-1", "supervisor", "mgr-1", "allow", "")).To(gomega.Succeed())
			gomega.Expect(got).To(gomega.Equal(map[string]string{
				"answered_by": "supervisor",
				"resolved_by": "mgr-1",
				"decision":    "allow",
			}))
		})

		ginkgo.It("treats 409 as already closed", func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusConflict)
			}))
			defer server.Close()

			store := NewBoardStore(server.URL, nil)
			gomega.Expect(store.Answer(context.Background(), "item-1", "supervisor", "mgr-1", "allow", "")).To(gomega.Succeed())
		})

		ginkgo.It("fails on a server error", func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()

			store := NewBoardStore(server.URL, nil)
			gomega.Expect(store.Answer(context.Background(), "item-1", "supervisor", "mgr-1", "allow", "")).ToNot(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("closeSettledCard", func() {
	// settledPark is the park the answer settles: the current one, so a legacy
	// card raised for it is not stale.
	settledPark := func() *Question {
		return &Question{
			RequestID:   "req-1",
			Kind:        AnswerKindPermission,
			ToolName:    "Bash",
			RequestedAt: time.Now(),
		}
	}
	// freshCard is a legacy hook card — no request id — raised for settledPark.
	freshCard := func(itemID string) BoardItem {
		return BoardItem{
			ItemID:          itemID,
			State:           "open",
			AnswerMechanism: "permission",
			ProducerID:      "s1",
			Payload:         "Bash: ls -la",
			CreatedAt:       time.Now().Format(time.RFC3339),
		}
	}

	ginkgo.It("closes the card that names the answered request id", func() {
		store := &fakeBoardStore{items: []BoardItem{
			{
				ItemID: "a", State: "open", AnswerMechanism: "permission",
				ProducerID: "s1", RequestID: "req-1",
			},
		}}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.Equal([]string{"a=supervisor:allow"}))
		gomega.Expect(store.answerRequestIDs).To(gomega.Equal([]string{"req-1"}))
	})

	// SC2 — the defect this task exists to fix. The producer holds ONE open
	// operator-tier card, and a benign answer for a DIFFERENT request arrives. A
	// close keyed on the producer alone stamped that card answered; it must stay
	// open. The single-card fixture is what discriminates: with two open cards the
	// pre-change code already skipped, so a two-card fixture would pass untouched.
	ginkgo.It("leaves an operator-tier card open when a different request is answered", func() {
		store := &fakeBoardStore{items: []BoardItem{
			{
				ItemID: "operator-card", State: "open", AnswerMechanism: "permission",
				ProducerID: "s1", RequestID: "operator-req",
			},
		}}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.BeEmpty())
	})

	// The live shape: the hook's permission card carries no request id, so the join
	// is the park it was raised for. A card that predates this park belongs to an
	// earlier gate and must not be stamped by this answer.
	ginkgo.It("leaves a stale card open even when it names the same tool", func() {
		stale := freshCard("old")
		stale.CreatedAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
		store := &fakeBoardStore{items: []BoardItem{stale}}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.BeEmpty())
	})

	ginkgo.It("leaves a card open when it names a different tool", func() {
		card := freshCard("write-card")
		card.Payload = "Write: /etc/hosts"
		store := &fakeBoardStore{items: []BoardItem{card}}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.BeEmpty())
	})

	ginkgo.It("closes a fresh legacy card raised for this park", func() {
		store := &fakeBoardStore{items: []BoardItem{freshCard("a")}}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.Equal([]string{"a=supervisor:allow"}))
	})

	ginkgo.It("closes nothing when two cards match the park", func() {
		first := freshCard("a")
		second := freshCard("b")
		store := &fakeBoardStore{items: []BoardItem{first, second}}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.BeEmpty())
	})

	ginkgo.It("does nothing when the board is not wired", func() {
		server := &RosterToolServer{}
		gomega.Expect(func() {
			server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")
		}).ToNot(gomega.Panic())
	})

	ginkgo.It("does nothing when the answer settles no park", func() {
		store := &fakeBoardStore{items: []BoardItem{freshCard("a")}}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", nil, "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.BeEmpty())
	})

	ginkgo.It("does nothing when the store cannot be read", func() {
		store := &fakeBoardStore{openErr: stderrors.New("store down")}
		server := &RosterToolServer{Board: store}

		server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")

		gomega.Expect(store.answers).To(gomega.BeEmpty())
	})

	ginkgo.It("swallows an answer error without panicking", func() {
		store := &fakeBoardStore{
			items:     []BoardItem{freshCard("a")},
			answerErr: stderrors.New("store rejected"),
		}
		server := &RosterToolServer{Board: store}

		gomega.Expect(func() {
			server.closeSettledCard(context.Background(), "s1", settledPark(), "allow", "mgr-1")
		}).ToNot(gomega.Panic())
	})
})
