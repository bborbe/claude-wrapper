// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Pending", func() {
	var (
		ctx     context.Context
		subject Pending
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		subject = NewPending()
	})

	ginkgo.It("reports a registered request as unanswered with its payload", func() {
		subject.Register("r1", Question{
			Kind:     AnswerKindQuestion,
			ToolName: "AskUserQuestion",
			Questions: []QuestionPrompt{
				{Question: "Pick one", Header: "h", Options: []QuestionOption{{Label: "a"}}},
			},
		})

		pending, err := subject.Peek("r1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(pending.Answered).To(gomega.BeFalse())
		gomega.Expect(pending.Question.Kind).To(gomega.Equal(AnswerKindQuestion))
		gomega.Expect(pending.Question.Questions).To(gomega.HaveLen(1))
		gomega.Expect(pending.Question.Questions[0].Options).To(gomega.HaveLen(1))
	})

	ginkgo.It("records a matching answer and reports it", func() {
		subject.Register("r1", Question{Kind: AnswerKindPermission, ToolName: "Bash"})

		err := subject.Resolve(ctx, "r1", Answer{Kind: AnswerKindPermission, Behavior: "deny"})
		gomega.Expect(err).To(gomega.BeNil())

		pending, err := subject.Peek("r1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(pending.Answered).To(gomega.BeTrue())
		gomega.Expect(pending.Answer.Behavior).To(gomega.Equal("deny"))
	})

	// The recorded-but-not-delivered failure of 2026-10-29 was an answer that
	// landed in a registry of the wrong shape and reported success. Both
	// mismatch directions must refuse and leave the request unanswered.
	ginkgo.It("refuses a selection aimed at a permission request", func() {
		subject.Register("r1", Question{Kind: AnswerKindPermission, ToolName: "Bash"})

		err := subject.Resolve(ctx, "r1", Answer{
			Kind:    AnswerKindQuestion,
			Answers: map[string]string{"Pick one": "a"},
		})
		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(err).To(gomega.MatchError(ErrAnswerShapeMismatch))

		pending, err := subject.Peek("r1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(pending.Answered).To(gomega.BeFalse())
	})

	ginkgo.It("refuses a behavior aimed at a question", func() {
		subject.Register("r1", Question{Kind: AnswerKindQuestion, ToolName: "AskUserQuestion"})

		err := subject.Resolve(ctx, "r1", Answer{Kind: AnswerKindPermission, Behavior: "allow"})
		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(err).To(gomega.MatchError(ErrAnswerShapeMismatch))

		pending, err := subject.Peek("r1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(pending.Answered).To(gomega.BeFalse())
	})

	ginkgo.It("reports an unknown request as unsupported, not as a shape mismatch", func() {
		err := subject.Resolve(ctx, "nope", Answer{Kind: AnswerKindPermission, Behavior: "allow"})
		gomega.Expect(err).ToNot(gomega.BeNil())
		gomega.Expect(err).To(gomega.MatchError(ErrAnswerUnsupported))
	})

	ginkgo.It("refuses a second answer to the same request", func() {
		subject.Register("r1", Question{Kind: AnswerKindPermission})

		first := subject.Resolve(ctx, "r1", Answer{Kind: AnswerKindPermission, Behavior: "deny"})
		gomega.Expect(first).To(gomega.BeNil())

		second := subject.Resolve(ctx, "r1", Answer{Kind: AnswerKindPermission, Behavior: "allow"})
		gomega.Expect(second).ToNot(gomega.BeNil())
		gomega.Expect(second).To(gomega.MatchError(ErrAnswerUnsupported))

		pending, err := subject.Peek("r1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(pending.Answer.Behavior).To(gomega.Equal("deny"))
	})

	// A retried tool call must not reset a decision already made.
	ginkgo.It("keeps the first payload when a request is registered twice", func() {
		subject.Register("r1", Question{Kind: AnswerKindPermission, ToolName: "Bash"})
		gomega.Expect(subject.Resolve(ctx, "r1", Answer{
			Kind:     AnswerKindPermission,
			Behavior: "deny",
		})).To(gomega.BeNil())

		subject.Register("r1", Question{Kind: AnswerKindQuestion, ToolName: "AskUserQuestion"})

		pending, err := subject.Peek("r1")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(pending.Answered).To(gomega.BeTrue())
		gomega.Expect(pending.Question.Kind).To(gomega.Equal(AnswerKindPermission))
	})
})
