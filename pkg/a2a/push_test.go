// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/a2a"
)

func statusBody(text string) []byte {
	return []byte(fmt.Sprintf(
		`{"statusUpdate":{"status":{"state":"TASK_STATE_WORKING","message":{"messageId":"m1","parts":[{"text":%q}]}}}}`,
		text,
	))
}

var _ = Describe("PushRegistry", func() {
	ctx := context.Background()
	var registry *a2a.PushRegistry

	BeforeEach(func() {
		registry = a2a.NewPushRegistry("host.docker.internal")
	})

	AfterEach(func() {
		_ = registry.Close()
	})

	post := func(token string, body []byte) int {
		port := registry.Port()
		req, err := http.NewRequest(
			http.MethodPost,
			fmt.Sprintf("http://127.0.0.1:%d%s", port, a2a.PushPath),
			bytes.NewReader(body),
		)
		Expect(err).NotTo(HaveOccurred())
		if token != "" {
			req.Header.Set(a2a.TokenHeader, token)
		}
		res, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode
	}

	It("refuses a push with no token", func() {
		_, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(post("", statusBody("hello"))).To(Equal(http.StatusUnauthorized))
	})

	It("refuses an unknown token", func() {
		_, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(post("not-a-token", statusBody("hello"))).To(Equal(http.StatusUnauthorized))
	})

	It("files a status under the token before bind, then moves it to the worker id", func() {
		token, pushURL, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(pushURL).To(ContainSubstring(a2a.PushPath))

		Expect(post(token, statusBody("buffered"))).To(Equal(http.StatusOK))
		Expect(registry.StatusMessages(token)).To(HaveLen(1))

		Expect(registry.Bind(ctx, token, "worker-1")).To(Succeed())
		entries := registry.StatusMessages("worker-1")
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Source).To(Equal("push"))
		Expect(entries[0].Text).NotTo(BeNil())
		Expect(*entries[0].Text).To(Equal("buffered"))
	})

	It("bounds the channel at 50 entries, keeping the newest", func() {
		token, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(registry.Bind(ctx, token, "worker-2")).To(Succeed())

		for i := 0; i < a2a.PushMaxMessages+5; i++ {
			Expect(post(token, statusBody(fmt.Sprintf("entry-%d", i)))).To(Equal(http.StatusOK))
		}
		entries := registry.StatusMessages("worker-2")
		Expect(entries).To(HaveLen(a2a.PushMaxMessages))
		Expect(*entries[len(entries)-1].Text).To(Equal(
			fmt.Sprintf("entry-%d", a2a.PushMaxMessages+4),
		))
	})

	It("accepts a non-status envelope without storing it", func() {
		token, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(post(token, []byte(`{"task":{"id":"t1"}}`))).To(Equal(http.StatusOK))
		Expect(registry.StatusMessages(token)).To(BeEmpty())
	})

	It("rejects a body with more than one envelope key", func() {
		token, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(post(token, []byte(`{"task":{},"message":{}}`))).To(Equal(http.StatusBadRequest))
	})

	It("releases only an unbound token", func() {
		token, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(registry.Release(token)).To(BeTrue())

		bound, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(registry.Bind(ctx, bound, "worker-3")).To(Succeed())
		Expect(registry.Release(bound)).To(BeFalse())
	})

	It("exposes the push config a follow-up must re-register", func() {
		token, pushURL, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(registry.Bind(ctx, token, "worker-4")).To(Succeed())

		url, gotToken, ok := registry.PushFor("worker-4")
		Expect(ok).To(BeTrue())
		Expect(url).To(Equal(pushURL))
		Expect(gotToken).To(Equal(token))
	})
})
