// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/a2a"
)

// pullItem renders one relay pull item carrying a statusUpdate envelope, in the
// shape the relay's queue hands back.
func pullItem(token, state string) string {
	return fmt.Sprintf(
		`{"token":%q,"body":{"statusUpdate":{"status":{"state":%q}}}}`, token, state,
	)
}

var _ = Describe("Tunnel", func() {
	ctx := context.Background()
	var registry *a2a.PushRegistry

	BeforeEach(func() {
		registry = a2a.NewPushRegistry("host.docker.internal")
	})

	AfterEach(func() {
		_ = registry.Close()
	})

	It("refuses a relay URL with no secret", func() {
		_, err := a2a.NewTunnel("http://relay.example", "", registry)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("non-empty tunnel secret"))
	})

	It("refuses an empty relay URL", func() {
		_, err := a2a.NewTunnel("", "s3cret", registry)
		Expect(err).To(HaveOccurred())
	})

	It("files a status carried by a pull", func() {
		token, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())

		var seenSecret, seenQuery string
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenSecret = r.Header.Get(a2a.TunnelSecretHeader)
			seenQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"items":[` + pullItem(token, "working") + `]}`))
		}))
		defer relay.Close()

		tunnel, err := a2a.NewTunnel(relay.URL, "s3cret", registry)
		Expect(err).NotTo(HaveOccurred())

		delivered, err := tunnel.PullOnce(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(delivered).To(Equal(1))
		Expect(seenSecret).To(Equal("s3cret"))
		Expect(seenQuery).To(Equal("wait=25s"))

		entries := registry.StatusMessages(token)
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Source).To(Equal("push"))
		Expect(entries[0].State).To(Equal("working"))
	})

	It("drops a pull item whose token this registry never minted", func() {
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"items":[` + pullItem("not-ours", "working") + `]}`))
		}))
		defer relay.Close()

		tunnel, err := a2a.NewTunnel(relay.URL, "s3cret", registry)
		Expect(err).NotTo(HaveOccurred())

		delivered, err := tunnel.PullOnce(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(delivered).To(Equal(0))
	})

	It("reports a non-2xx pull as an error rather than a delivery", func() {
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer relay.Close()

		tunnel, err := a2a.NewTunnel(relay.URL, "s3cret", registry)
		Expect(err).NotTo(HaveOccurred())

		delivered, err := tunnel.PullOnce(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("HTTP 500"))
		Expect(delivered).To(Equal(0))
	})

	It("keeps running through failures and delivers once the relay returns", func() {
		token, _, err := registry.BeginCreate(ctx, "")
		Expect(err).NotTo(HaveOccurred())

		var calls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch calls.Add(1) {
			case 1, 2:
				w.WriteHeader(http.StatusServiceUnavailable)
			case 3:
				_, _ = w.Write([]byte(`{"items":[` + pullItem(token, "done") + `]}`))
			default:
				_, _ = w.Write([]byte(`{"items":[]}`))
			}
		}))
		defer relay.Close()

		tunnel, err := a2a.NewTunnel(relay.URL, "s3cret", registry)
		Expect(err).NotTo(HaveOccurred())
		tunnel.RetryDelay = 5 * time.Millisecond
		tunnel.MaxRetryDelay = 10 * time.Millisecond

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			tunnel.Run(runCtx)
		}()

		Eventually(func() int {
			return len(registry.StatusMessages(token))
		}, 3*time.Second, 10*time.Millisecond).Should(Equal(1))
		Expect(registry.StatusMessages(token)[0].State).To(Equal("done"))

		// Two failed pulls must not have ended the loop: it is still running.
		Consistently(stopped, 100*time.Millisecond).ShouldNot(BeClosed())

		cancel()
		Eventually(stopped, 2*time.Second).Should(BeClosed())
	})

	It("ends the loop within 2 s of a cancelled context", func() {
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer relay.Close()

		tunnel, err := a2a.NewTunnel(relay.URL, "s3cret", registry)
		Expect(err).NotTo(HaveOccurred())
		tunnel.RetryDelay = time.Second
		tunnel.MaxRetryDelay = 30 * time.Second

		runCtx, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			tunnel.Run(runCtx)
		}()

		Consistently(stopped, 100*time.Millisecond).ShouldNot(BeClosed())
		cancel()
		Eventually(stopped, 2*time.Second).Should(BeClosed())
	})
})
