// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"net/http"
	"net/http/httptest"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("HTTPVaultAPIClient", func() {
	var (
		ctx      context.Context
		requests []string
		server   *httptest.Server
		client   HTTPVaultAPIClient
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		requests = nil
	})

	ginkgo.AfterEach(func() {
		if server != nil {
			server.Close()
			server = nil
		}
	})

	serve := func(status int, body string) {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests = append(requests, r.URL.Path+"?"+r.URL.RawQuery)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		client = HTTPVaultAPIClient{BaseURL: server.URL + "/api/v1"}
	}

	ginkgo.It("sends the flagged/open/due query for tasks and decodes the rows", func() {
		serve(http.StatusOK, `{"items":[{"id":"abc","title":"A Row","path":"25 Tasks/A Row.md",
			"status":"in_progress","flag":true,"vault":"private-personal",
			"claude_session_id":"11111111-1111-1111-1111-111111111111",
			"goals":["[[Some Goal]]"]}]}`)

		rows, err := client.List(ctx, "task")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Title).To(gomega.Equal("A Row"))
		gomega.Expect(rows[0].Flag).To(gomega.BeTrue())
		gomega.Expect(rows[0].SessionID).
			To(gomega.Equal("11111111-1111-1111-1111-111111111111"))
		gomega.Expect(rows[0].Goals).To(gomega.Equal([]string{"[[Some Goal]]"}))
		gomega.Expect(requests).To(gomega.Equal([]string{
			"/api/v1/tasks?flag=true&status=in_progress&due=now",
		}))
	})

	ginkgo.It("sends only the flag filter for goals and topics", func() {
		serve(http.StatusOK, `{"items":[]}`)

		_, err := client.List(ctx, "goal")
		gomega.Expect(err).To(gomega.BeNil())
		_, err = client.List(ctx, "topic")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(requests).To(gomega.Equal([]string{
			"/api/v1/goals?flag=true",
			"/api/v1/topics?flag=true",
		}))
	})

	ginkgo.It("answers an empty slice, never nil, when the index holds no rows", func() {
		serve(http.StatusOK, `{"items":[]}`)

		rows, err := client.List(ctx, "task")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(rows).ToNot(gomega.BeNil())
		gomega.Expect(rows).To(gomega.BeEmpty())
	})

	ginkgo.It("reports an outage as ErrVaultAPIUnavailable, never as no rows", func() {
		serve(http.StatusServiceUnavailable, `{"error":"down"}`)

		rows, err := client.List(ctx, "task")
		gomega.Expect(rows).To(gomega.BeNil())
		gomega.Expect(stderrors.Is(err, ErrVaultAPIUnavailable)).To(gomega.BeTrue())
	})

	ginkgo.It("treats an unreachable server as an outage", func() {
		server = httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base := server.URL
		server.Close()
		server = nil
		client = HTTPVaultAPIClient{BaseURL: base + "/api/v1"}

		_, err := client.List(ctx, "task")
		gomega.Expect(stderrors.Is(err, ErrVaultAPIUnavailable)).To(gomega.BeTrue())
	})

	ginkgo.It("treats an undecodable body as an outage", func() {
		serve(http.StatusOK, `not json`)

		_, err := client.List(ctx, "task")
		gomega.Expect(stderrors.Is(err, ErrVaultAPIUnavailable)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses an unknown kind rather than falling back to tasks", func() {
		serve(http.StatusOK, `{"items":[]}`)

		_, err := client.List(ctx, "widget")
		gomega.Expect(stderrors.Is(err, ErrVaultAPIUnavailable)).To(gomega.BeTrue())
		gomega.Expect(requests).To(gomega.BeEmpty())
	})

	ginkgo.It("decodes one task record's frontmatter", func() {
		serve(http.StatusOK, `{"id":"abc","frontmatter":{"role":"agent","mode":"headless",
			"claude_session_id":"22222222-2222-2222-2222-222222222222",
			"flag_set_by":"operator"},"body":"Body\n"}`)

		page, err := client.Page(ctx, "task", "abc")
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(page.Frontmatter["role"]).To(gomega.Equal("agent"))
		gomega.Expect(page.Frontmatter["mode"]).To(gomega.Equal("headless"))
		gomega.Expect(page.Frontmatter["flag_set_by"]).To(gomega.Equal("operator"))
		gomega.Expect(page.Body).To(gomega.Equal("Body\n"))
		gomega.Expect(requests).To(gomega.Equal([]string{"/api/v1/tasks/abc?"}))
	})

	ginkgo.It("refuses a page read for a kind with no frontmatter endpoint", func() {
		serve(http.StatusOK, `{}`)

		_, err := client.Page(ctx, "goal", "some-goal")
		gomega.Expect(stderrors.Is(err, ErrVaultAPIUnavailable)).To(gomega.BeTrue())
		gomega.Expect(requests).To(gomega.BeEmpty())
	})
})
