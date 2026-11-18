// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("reads the session registry", func() {
	ctx := context.Background()

	ginkgo.It("reads a live record and probes the pid", func() {
		dir := ginkgo.GinkgoT().TempDir()
		body := `{"sessionId":"s1","pid":` + itoa(os.Getpid()) +
			`,"status":"waiting","name":"worker"}`
		gomega.Expect(os.WriteFile(filepath.Join(dir, "1.json"), []byte(body), 0o600)).
			To(gomega.Succeed())
		registry, err := ReadRegistry(ctx, dir)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		record, ok := registry["s1"]
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(record.Alive).To(gomega.BeTrue())
		gomega.Expect(record.Status).To(gomega.Equal("waiting"))
	})

	ginkgo.It("degrades an unreadable registry record to no record", func() {
		dir := ginkgo.GinkgoT().TempDir()
		gomega.Expect(os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{half"), 0o600)).
			To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(dir, "nosession.json"),
			[]byte(`{"pid":1}`), 0o600)).To(gomega.Succeed())
		registry, err := ReadRegistry(ctx, dir)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(registry).To(gomega.BeEmpty())
	})

	ginkgo.It("reads a missing registry directory as empty", func() {
		registry, err := ReadRegistry(ctx, filepath.Join(ginkgo.GinkgoT().TempDir(), "nope"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(registry).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("reads the heartbeat store", func() {
	ctx := context.Background()

	ginkgo.It("reports a fresh stamp live", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "s1.json")
		gomega.Expect(os.WriteFile(path, []byte(`{}`), 0o600)).To(gomega.Succeed())
		now := time.Now()
		gomega.Expect(os.Chtimes(path, now, now)).To(gomega.Succeed())
		live, readable := HeartbeatLive(ctx, dir, "s1", now)
		gomega.Expect(live).To(gomega.BeTrue())
		gomega.Expect(readable).To(gomega.BeTrue())
	})

	ginkgo.It("reports a stale stamp not live", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "s1.json")
		gomega.Expect(os.WriteFile(path, []byte(`{}`), 0o600)).To(gomega.Succeed())
		now := time.Now()
		old := now.Add(-2 * time.Minute)
		gomega.Expect(os.Chtimes(path, old, old)).To(gomega.Succeed())
		live, readable := HeartbeatLive(ctx, dir, "s1", now)
		gomega.Expect(live).To(gomega.BeFalse())
		gomega.Expect(readable).To(gomega.BeTrue())
	})

	ginkgo.It("degrades an unreadable heartbeat store to not live", func() {
		blocker := filepath.Join(ginkgo.GinkgoT().TempDir(), "blocker")
		gomega.Expect(os.WriteFile(blocker, []byte("x"), 0o600)).To(gomega.Succeed())
		live, readable := HeartbeatLive(ctx, blocker, "s1", time.Now())
		gomega.Expect(live).To(gomega.BeFalse())
		gomega.Expect(readable).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("reads the attention feed", func() {
	ctx := context.Background()

	fallbackConfig := func() Config {
		return Config{
			FeedDir:  filepath.Join("..", "..", "testdata", "sweepgate", "inputs", "feed"),
			StoreURL: "http://127.0.0.1:1",
		}
	}

	ginkgo.It("degrades an unreadable feed to the event-log fallback", func() {
		feed, err := ReadFeed(ctx, fallbackConfig(), fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed).To(gomega.HaveKey("feed-session-permission"))
		gomega.Expect(IsOpenGate(feed["feed-session-permission"])).To(gomega.BeTrue())
	})

	ginkgo.It("degrades an unreachable store to a replayed feed", func() {
		feed, err := ReadFeed(ctx, fallbackConfig(), fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed["feed-session-permission"].Replayed).To(gomega.BeTrue())
		gomega.Expect(feed["feed-session-stop"].Replayed).To(gomega.BeTrue())
	})

	ginkgo.It("degrades a malformed feed line to a skipped line", func() {
		dir := ginkgo.GinkgoT().TempDir()
		body := "{not json\n" +
			`{"type":"open","item_id":"i1","session_id":"s1","kind":"permission",` +
			`"detail":"Allow?","ts":1700000000}` + "\n"
		gomega.Expect(os.WriteFile(filepath.Join(dir, "s1.events.jsonl"),
			[]byte(body), 0o600)).To(gomega.Succeed())
		feed, err := ReadFeed(ctx, Config{FeedDir: dir, StoreURL: "http://127.0.0.1:1"},
			fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed).To(gomega.HaveKey("s1"))
		gomega.Expect(IsOpenGate(feed["s1"])).To(gomega.BeTrue())
	})

	ginkgo.It("degrades a malformed JSON record to no record", func() {
		dir := ginkgo.GinkgoT().TempDir()
		gomega.Expect(os.WriteFile(filepath.Join(dir, "s1.needs.json"),
			[]byte("{half"), 0o600)).To(gomega.Succeed())
		feed, err := ReadFeed(ctx, Config{FeedDir: dir, StoreURL: "http://127.0.0.1:1"},
			fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed).To(gomega.BeEmpty())
	})

	ginkgo.It("wins with the attention store when it answers", func() {
		server := httptest.NewServer(http.HandlerFunc(func(
			w http.ResponseWriter, _ *http.Request,
		) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"item_id":"store-1","dedup_key":"i1",` +
				`"producer_id":"s1","answer_mechanism":"permission",` +
				`"payload":"Allow?","state":"open"}]`))
		}))
		defer server.Close()
		feed, err := ReadFeed(ctx, Config{
			FeedDir: ginkgo.GinkgoT().TempDir(), StoreURL: server.URL,
		}, fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed).To(gomega.HaveKey("s1"))
		gomega.Expect(feed["s1"].ItemID).To(gomega.Equal("i1"))
		gomega.Expect(feed["s1"].Replayed).To(gomega.BeFalse())
		gomega.Expect(IsOpenGate(feed["s1"])).To(gomega.BeTrue())
	})

	ginkgo.It("drops a store item whose mechanism has no kind", func() {
		server := httptest.NewServer(http.HandlerFunc(func(
			w http.ResponseWriter, _ *http.Request,
		) {
			_, _ = w.Write([]byte(`[{"item_id":"a","producer_id":"s1",` +
				`"answer_mechanism":"ack","state":"open"}]`))
		}))
		defer server.Close()
		feed, err := ReadFeed(ctx, Config{
			FeedDir: ginkgo.GinkgoT().TempDir(), StoreURL: server.URL,
		}, fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed).To(gomega.BeEmpty())
	})

	ginkgo.It("treats a non-200 store answer as unreachable", func() {
		server := httptest.NewServer(http.HandlerFunc(func(
			w http.ResponseWriter, _ *http.Request,
		) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		feed, err := ReadFeed(ctx, Config{
			FeedDir: ginkgo.GinkgoT().TempDir(), StoreURL: server.URL,
		}, fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed).To(gomega.BeEmpty())
	})

	ginkgo.It("folds an open item with a matching close to answered", func() {
		dir := ginkgo.GinkgoT().TempDir()
		body := `{"type":"open","item_id":"i1","session_id":"s1","kind":"permission"}` +
			"\n" + `{"type":"close","item_id":"i1"}` + "\n"
		gomega.Expect(os.WriteFile(filepath.Join(dir, "s1.events.jsonl"),
			[]byte(body), 0o600)).To(gomega.Succeed())
		feed, err := ReadFeed(ctx, Config{FeedDir: dir, StoreURL: "http://127.0.0.1:1"},
			fixedNow)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(feed["s1"].State).To(gomega.Equal("answered"))
		gomega.Expect(IsOpenGate(feed["s1"])).To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("classifies a feed record", func() {
	ginkgo.It("refuses an answered record as a gate", func() {
		gomega.Expect(IsOpenGate(FeedRecord{State: "answered", Kind: "permission"})).
			To(gomega.BeFalse())
	})

	ginkgo.It("refuses an idle record as a gate", func() {
		gomega.Expect(IsOpenGate(FeedRecord{State: "open", Kind: "idle"})).
			To(gomega.BeFalse())
	})

	ginkgo.It("refuses a rendered Stop panel as a gate", func() {
		gomega.Expect(IsOpenGate(FeedRecord{
			State: "open", Kind: "question", Event: "Stop",
		})).To(gomega.BeFalse())
	})

	ginkgo.It("refuses a later-on closer as a gate", func() {
		gomega.Expect(IsOpenGate(FeedRecord{
			State: "open", Kind: "question", Detail: "later (on CI)",
		})).To(gomega.BeFalse())
	})

	ginkgo.It("normalises entities before the parked-verb test", func() {
		gomega.Expect(IsOpenGate(FeedRecord{
			State: "open", Kind: "question", Detail: "later&nbsp;(on CI)",
		})).To(gomega.BeFalse())
	})

	ginkgo.It("accepts a raised gate", func() {
		gomega.Expect(IsOpenGate(FeedRecord{
			State: "open", Kind: "permission", Detail: "Allow?",
		})).To(gomega.BeTrue())
	})

	ginkgo.It("detects an external wait only on a Stop closer", func() {
		gomega.Expect(IsExternalWait(FeedRecord{
			Event: "Stop", Detail: "⏰ Ends: CI",
		})).To(gomega.BeTrue())
		gomega.Expect(IsExternalWait(FeedRecord{
			Event: "Notification", Detail: "⏰ Ends: CI",
		})).To(gomega.BeFalse())
		gomega.Expect(IsExternalWait(FeedRecord{Event: "Stop", Detail: "review: x"})).
			To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("resolves liveness", func() {
	registry := map[string]RegistryRecord{
		"live":    {Status: "busy", Alive: true},
		"waiting": {Status: "waiting", Alive: true},
		"gated":   {Status: "busy", Alive: true},
		"dead":    {Status: "busy", Alive: false},
	}
	feed := map[string]FeedRecord{
		"gated": {State: "open", Kind: "permission"},
	}
	noBeat := func(string) (bool, bool) { return false, true }
	beat := func(string) (bool, bool) { return true, true }

	ginkgo.It("reports an empty id as none", func() {
		gomega.Expect(LivenessOf("", registry, feed, noBeat)).To(gomega.Equal(LivenessNone))
	})

	ginkgo.It("reports a live registry record live", func() {
		gomega.Expect(LivenessOf("live", registry, feed, noBeat)).
			To(gomega.Equal(LivenessLive))
	})

	ginkgo.It("reports a waiting registry record parked", func() {
		gomega.Expect(LivenessOf("waiting", registry, feed, noBeat)).
			To(gomega.Equal(LivenessParked))
	})

	ginkgo.It("reports a gated registry record parked", func() {
		gomega.Expect(LivenessOf("gated", registry, feed, beat)).
			To(gomega.Equal(LivenessParked))
	})

	ginkgo.It("falls through to the heartbeat for a headless worker", func() {
		gomega.Expect(LivenessOf("headless", registry, feed, beat)).
			To(gomega.Equal(LivenessLive))
		gomega.Expect(LivenessOf("headless", registry, feed, noBeat)).
			To(gomega.Equal(LivenessNone))
	})

	ginkgo.It("prefers live over parked over none across the id set", func() {
		ids := []string{"dead", "waiting"}
		gomega.Expect(LivenessOfSet(ids, registry, feed, noBeat)).
			To(gomega.Equal(LivenessParked))
		ids = []string{"dead", "live"}
		gomega.Expect(LivenessOfSet(ids, registry, feed, noBeat)).
			To(gomega.Equal(LivenessLive))
		gomega.Expect(LivenessOfSet([]string{"dead"}, registry, feed, noBeat)).
			To(gomega.Equal(LivenessNone))
	})
})

// itoa is a tiny int-to-string helper for fixture JSON.
func itoa(value int) string {
	return formatUnit(value, "")
}
