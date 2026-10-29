// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The delivery path is where the recorded-but-not-delivered failure lived, so
// these specs assert the payload the CHILD receives, not that a call returned.
var _ = ginkgo.Describe("question verdict", func() {
	params := json.RawMessage(`{"name":"permission_prompt","arguments":{
		"tool_name":"AskUserQuestion",
		"tool_use_id":"tu-1",
		"input":{"questions":[{"question":"Which nonce?","header":"Nonce",
			"options":[{"label":"QUESTION-NONCE-9T2K","description":"the nonce"}],
			"multiSelect":false}]}}}`)

	ginkgo.It("echoes the original questions back beside the answers", func() {
		// updatedInput must satisfy the tool's own input schema, so `questions`
		// is required. Returning answers alone makes the child reject the answer
		// with `The required parameter "questions" is missing`.
		verdict := questionVerdict(params, map[string]string{"Which nonce?": "QUESTION-NONCE-9T2K"})

		var decoded struct {
			Behavior     string         `json:"behavior"`
			UpdatedInput map[string]any `json:"updatedInput"`
		}
		gomega.Expect(json.Unmarshal([]byte(verdict), &decoded)).To(gomega.Succeed())
		gomega.Expect(decoded.Behavior).To(gomega.Equal("allow"))
		gomega.Expect(decoded.UpdatedInput).To(gomega.HaveKey("questions"))
		gomega.Expect(decoded.UpdatedInput).To(gomega.HaveKey("answers"))
		gomega.Expect(decoded.UpdatedInput["answers"]).
			To(gomega.Equal(map[string]any{"Which nonce?": "QUESTION-NONCE-9T2K"}))
	})

	ginkgo.It("refuses rather than answering unreadably", func() {
		verdict := questionVerdict(json.RawMessage(`not json`), map[string]string{"q": "a"})

		var decoded struct {
			Behavior string `json:"behavior"`
		}
		gomega.Expect(json.Unmarshal([]byte(verdict), &decoded)).To(gomega.Succeed())
		gomega.Expect(decoded.Behavior).To(gomega.Equal("deny"))
	})

	ginkgo.It("reads the tool name out of a permission-prompt call", func() {
		gomega.Expect(toolNameOf(params)).To(gomega.Equal("AskUserQuestion"))
		gomega.Expect(toolNameOf(json.RawMessage(`nope`))).To(gomega.BeEmpty())
	})

	ginkgo.It("routes each answer kind to its own verdict", func() {
		server := &MCPServer{Allow: true}

		question := server.verdictForAnswer(
			pollResult{Kind: string(AnswerKindQuestion), Answers: map[string]string{"q": "a"}},
			params,
		)
		gomega.Expect(question).To(gomega.ContainSubstring(`"updatedInput"`))

		permission := server.verdictForAnswer(
			pollResult{Kind: string(AnswerKindPermission), Behavior: "deny"},
			params,
		)
		gomega.Expect(permission).To(gomega.Equal(`{"behavior":"deny","message":"denied by claude-wrapper"}`))
	})
})
