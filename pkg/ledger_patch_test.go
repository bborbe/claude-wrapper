// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/http/httptest"

	libtime "github.com/bborbe/time"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// ledgerPatchSuite pins the wire shape of the terminal write: a PATCH to the row
// the path names, the fields the store declares, and a 404 that is reported
// rather than folded into success.
var _ = ginkgo.Describe("ledgerClient.Patch", func() {
	var (
		ctx      context.Context
		server   *httptest.Server
		client   LedgerReader
		lastReq  *http.Request
		lastBody map[string]json.RawMessage
		answer   int
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		lastReq = nil
		lastBody = nil
		answer = http.StatusOK
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lastReq = r
			body, err := io.ReadAll(r.Body)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(json.Unmarshal(body, &lastBody)).To(gomega.Succeed())
			w.WriteHeader(answer)
		}))
		client = NewLedgerClient(server.URL, nil, libtime.NewCurrentDateTime())
	})

	ginkgo.AfterEach(func() {
		server.Close()
	})

	ginkgo.It("PATCHes the row named by the path with the verdict's fields", func() {
		err := client.Patch(ctx, "abc-123", TerminalLedgerPatch(
			"done",
			json.RawMessage(`{"subtype":"success","is_error":false}`),
			"2026-11-09T12:00:00Z",
		))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lastReq.Method).To(gomega.Equal(http.MethodPatch))
		// The id travels in the path and NOT in the body: the store refuses a
		// patch that names its own row, so a body copy could only ever disagree.
		gomega.Expect(lastReq.URL.Path).To(gomega.Equal(LedgerPath + "/abc-123"))
		gomega.Expect(lastBody).NotTo(gomega.HaveKey("session_id"))
		gomega.Expect(string(lastBody["status"])).To(gomega.Equal(`"done"`))
		gomega.Expect(string(lastBody["ended_at"])).To(gomega.Equal(`"2026-11-09T12:00:00Z"`))
		gomega.Expect(string(lastBody["result"])).To(gomega.Equal(`{"subtype":"success","is_error":false}`))
	})

	ginkgo.It("clears ended_at and result with a null, which is how a new turn re-opens a row", func() {
		gomega.Expect(client.Patch(ctx, "abc-123", TurnStartedLedgerPatch())).
			To(gomega.Succeed())
		gomega.Expect(string(lastBody["status"])).To(gomega.Equal(`"running"`))
		// `null` deletes the key on the store side; an OMITTED key would leave the
		// old verdict in place, which is the difference the map exists to keep.
		gomega.Expect(string(lastBody["ended_at"])).To(gomega.Equal("null"))
		gomega.Expect(string(lastBody["result"])).To(gomega.Equal("null"))
	})

	ginkgo.It("reports an absent row as ErrLedgerRowAbsent rather than as a write", func() {
		answer = http.StatusNotFound
		err := client.Patch(ctx, "missing", TurnStartedLedgerPatch())
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(stderrors.Is(err, ErrLedgerRowAbsent)).To(gomega.BeTrue())
	})

	ginkgo.It("reports any other refusal as an error", func() {
		answer = http.StatusInternalServerError
		err := client.Patch(ctx, "abc-123", TurnStartedLedgerPatch())
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(stderrors.Is(err, ErrLedgerRowAbsent)).To(gomega.BeFalse())
	})

	ginkgo.It("refuses an empty patch instead of sending a no-op that reads as a write", func() {
		err := client.Patch(ctx, "abc-123", LedgerPatch{})
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(lastReq).To(gomega.BeNil())
	})

	ginkgo.It("refuses a patch with no session id", func() {
		err := client.Patch(ctx, "", TurnStartedLedgerPatch())
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(lastReq).To(gomega.BeNil())
	})
})

// ledgerPostShapeSuite pins that a spawn row still carries NO verdict.
//
// The spawn POST and the terminal PATCH are two writes to one row, and the spawn
// row must keep the shape it has always had: `status` present and EMPTY (the
// field has never carried `omitempty`), `result` and `ended_at` absent. An empty
// status is non-terminal to the heartbeat poster, which is the correct reading of
// a worker that has not closed a turn yet; a spawn row carrying `done` would mark
// a worker finished before its first turn.
var _ = ginkgo.Describe("ledgerClient.Post", func() {
	var (
		ctx      context.Context
		server   *httptest.Server
		client   LedgerReader
		lastBody map[string]json.RawMessage
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		lastBody = nil
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(json.Unmarshal(body, &lastBody)).To(gomega.Succeed())
			w.WriteHeader(http.StatusOK)
		}))
		client = NewLedgerClient(server.URL, nil, libtime.NewCurrentDateTime())
	})

	ginkgo.AfterEach(func() {
		server.Close()
	})

	ginkgo.It("omits the terminal fields a spawn has no answer for", func() {
		gomega.Expect(client.Post(ctx, "spawner", LedgerRow{SessionID: "s1"})).To(gomega.Succeed())
		gomega.Expect(string(lastBody["status"])).To(gomega.Equal(`""`))
		gomega.Expect(lastBody).NotTo(gomega.HaveKey("result"))
		gomega.Expect(lastBody).NotTo(gomega.HaveKey("ended_at"))
	})
})
