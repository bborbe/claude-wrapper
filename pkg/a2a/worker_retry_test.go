// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// cardBody is a minimal Agent Card carrying the one field Endpoint needs.
const cardBody = `{"supportedInterfaces":[{"url":"http://worker/a2a","protocolVersion":"1.0"}]}`

// retryManager is a Manager whose retry window is small enough to test.
func retryManager(attempts int) *Manager {
	m := NewManager(NewClient(func() string { return "id" }), NewPushRegistry("host.docker.internal"), Config{})
	m.CardReadAttempts = attempts
	m.CardReadDelay = time.Millisecond
	return m
}

// ⚠️ THE CASE THIS EXISTS FOR: a k8s worker's ingress is created after the
// create call returns, so the first card read is a 404/503 while the pod is
// already Running. A single read loses the status channel permanently.
func TestReadCardWithRetryWaitsForTheIngress(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 4 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(cardBody))
	}))
	defer server.Close()

	card, err := retryManager(6).readCardWithRetry(context.Background(), &Agent{A2AURL: server.URL})
	if err != nil {
		t.Fatalf("expected the retry to reach the card, got %v", err)
	}
	if len(card.SupportedInterfaces) == 0 {
		t.Fatalf("expected a card carrying interfaces, got %#v", card)
	}
	if calls != 4 {
		t.Fatalf("expected 3 failures then a success (4 calls), got %d", calls)
	}
}

func TestReadCardWithRetryGivesUpAfterTheWindow(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := retryManager(3).readCardWithRetry(context.Background(), &Agent{A2AURL: server.URL})
	if err == nil {
		t.Fatal("expected an error once the attempts are spent")
	}
	if calls != 3 {
		t.Fatalf("expected exactly 3 attempts, got %d", calls)
	}
}

func TestReadCardWithRetryStopsOnCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	m := retryManager(1000)
	m.CardReadDelay = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.readCardWithRetry(ctx, &Agent{A2AURL: server.URL})
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the context is cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected the retry to stop within 2 s of cancellation")
	}
}
