// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/a2a"
)

// controlReply wraps a ControlResponse in the one-shot JSON-RPC envelope the
// controller serves, with the data part nested under a task artifact.
func controlReply(response map[string]any) string {
	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"task": map[string]any{
				"id": "task-1",
				"artifacts": []any{
					map[string]any{"parts": []any{map[string]any{"data": response}}},
				},
			},
		},
	}
	encoded, err := json.Marshal(envelope)
	Expect(err).NotTo(HaveOccurred())
	return string(encoded)
}

var _ = Describe("Client control plane", func() {
	ctx := context.Background()
	var (
		server      *httptest.Server
		sawVersion  string
		sawAuth     string
		replyBody   string
		replyStatus int
		cardAuthOK  bool
	)

	BeforeEach(func() {
		sawVersion, sawAuth = "", ""
		replyStatus = http.StatusOK
		cardAuthOK = false
		replyBody = controlReply(map[string]any{
			"operation": "create",
			"create": map[string]any{
				"worker": map[string]any{
					"worker_id": "worker-1",
					"task":      "some task",
					"a2a_url":   "http://127.0.0.1:1/a2a",
					"state":     "running",
				},
				"token": "worker-token",
			},
		})
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == a2a.AgentCardPath {
				if r.Header.Get("authorization") == "Bearer controller-token" {
					cardAuthOK = true
				}
				_, _ = w.Write([]byte(`{"supportedInterfaces":[{"url":"` +
					server.URL + `/a2a","protocolVersion":"1.0"}]}`))
				return
			}
			sawVersion = r.Header.Get(a2a.VersionHeader)
			sawAuth = r.Header.Get("authorization")
			w.WriteHeader(replyStatus)
			_, _ = w.Write([]byte(replyBody))
		}))
	})

	AfterEach(func() {
		server.Close()
	})

	cfg := func() a2a.ControllerConfig {
		return a2a.ControllerConfig{
			URL:    server.URL + "/a2a",
			Token:  "controller-token",
			Target: a2a.Target{Kind: a2a.KindDocker},
		}
	}

	It("reads the card with the bearer and sends the version it advertises", func() {
		_, err := a2a.NewClient(a2a.NewID).CreateWorker(ctx, cfg(), "some task")
		Expect(err).NotTo(HaveOccurred())
		Expect(cardAuthOK).To(BeTrue(), "the controller card is authenticated")
		Expect(sawVersion).To(Equal("1.0"))
		Expect(sawAuth).To(Equal("Bearer controller-token"))
	})

	It("returns the worker id, url and token from a one-shot reply", func() {
		response, err := a2a.NewClient(a2a.NewID).CreateWorker(ctx, cfg(), "some task")
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Create).NotTo(BeNil())
		Expect(response.Create.Worker.WorkerID).To(Equal("worker-1"))
		Expect(response.Create.Token).To(Equal("worker-token"))
	})

	It("reports a FAILED state as an unknown outcome, not a no-op", func() {
		replyBody = `{"jsonrpc":"2.0","id":1,"result":{"task":{"id":"t","status":{"state":"TASK_STATE_FAILED"}}}}`
		_, err := a2a.NewClient(a2a.NewID).CreateWorker(ctx, cfg(), "some task")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("does NOT mean the operation did not happen"))
	})

	It("refuses a reply whose operation does not match the call", func() {
		replyBody = controlReply(map[string]any{"operation": "list"})
		_, err := a2a.NewClient(a2a.NewID).CreateWorker(ctx, cfg(), "some task")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("does not belong to this call"))
	})

	It("refuses an HTTP error from the controller", func() {
		replyStatus = http.StatusUnauthorized
		_, err := a2a.NewClient(a2a.NewID).CreateWorker(ctx, cfg(), "some task")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("HTTP 401"))
	})
})

var _ = Describe("Client turn plane", func() {
	ctx := context.Background()

	It("collects the reply and the terminal state from SSE frames", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "data: {\"task\":{\"id\":\"t1\",\"contextId\":\"ctx-1\"}}\n\n")
			flusher.Flush()
			_, _ = fmt.Fprintf(w, "data: {\"artifactUpdate\":{\"artifact\":{\"parts\":[{\"text\":\"done\"}]}}}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"statusUpdate\":{\"status\":{\"state\":\"TASK_STATE_COMPLETED\"},\"taskId\":\"t1\"}}\n\n")
			flusher.Flush()
		}))
		defer server.Close()

		var announced string
		result, err := a2a.NewClient(a2a.NewID).SendTurn(
			ctx, server.URL, "worker-token", "ctx-1", "hello", "1.0",
			func(taskID string) { announced = taskID },
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(announced).To(Equal("t1"))
		Expect(result.State).To(Equal("TASK_STATE_COMPLETED"))
		Expect(result.Reply).To(Equal("done"))
		Expect(result.ContextID).To(Equal("ctx-1"))
	})

	It("refuses a turn that ran under a different contextId", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"task\":{\"id\":\"t1\",\"contextId\":\"other\"}}\n\n")
			_, _ = fmt.Fprintf(w, "data: {\"statusUpdate\":{\"status\":{\"state\":\"TASK_STATE_COMPLETED\"}}}\n\n")
		}))
		defer server.Close()

		_, err := a2a.NewClient(a2a.NewID).SendTurn(
			ctx, server.URL, "worker-token", "ctx-1", "hello", "1.0", nil,
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("the conversation is not the one intended"))
	})

	It("treats an error frame as fatal even inside an HTTP 200 stream", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"error\":{\"code\":-32009,\"message\":\"bad version\"}}\n\n")
		}))
		defer server.Close()

		_, err := a2a.NewClient(a2a.NewID).SendTurn(
			ctx, server.URL, "worker-token", "ctx-1", "hello", "", nil,
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("refused the turn"))
	})

	It("refuses a stream that produced no frames", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "text/event-stream")
		}))
		defer server.Close()

		_, err := a2a.NewClient(a2a.NewID).SendTurn(
			ctx, server.URL, "worker-token", "ctx-1", "hello", "", nil,
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no frames"))
	})
})

var _ = Describe("RegisterPush", func() {
	ctx := context.Background()

	It("sends the flat params the controller binding expects", func() {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}))
		defer server.Close()

		Expect(a2a.NewClient(a2a.NewID).RegisterPush(
			ctx, server.URL, "worker-token", "task-1", "http://push", "push-token", "1.0",
		)).To(Succeed())

		params, ok := body["params"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(params["taskId"]).To(Equal("task-1"))
		Expect(params["url"]).To(Equal("http://push"))
		Expect(params["token"]).To(Equal("push-token"))
		// ⚠️ Not the wrapped spelling: a wrapped body is refused as an unmarshal error.
		Expect(params).NotTo(HaveKey("pushNotificationConfig"))
		Expect(strings.TrimSpace(fmt.Sprint(body["method"]))).To(Equal(a2a.PushConfigMethod))
	})
})
