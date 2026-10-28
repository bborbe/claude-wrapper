// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

type fakeSupervisor struct {
	sessions []pkg.Session
	getErr   error
}

func (f *fakeSupervisor) Spawn(_ context.Context, _ pkg.SpawnOptions) (*pkg.Session, error) {
	return &pkg.Session{ID: "spawned", State: pkg.StateStarting}, nil
}

func (f *fakeSupervisor) List(_ context.Context) ([]pkg.Session, error) {
	return f.sessions, nil
}

func (f *fakeSupervisor) Get(_ context.Context, id string) (*pkg.Session, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &pkg.Session{ID: id, State: pkg.StateIdle}, nil
}

func (f *fakeSupervisor) Answer(_ context.Context, _, _, _ string) error {
	return f.getErr
}

func (f *fakeSupervisor) Stop(_ context.Context, _ string) error {
	return f.getErr
}

func TestHealthzReturnsOK(t *testing.T) {
	rec := httptest.NewRecorder()
	handler.Healthz().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestListReturnsSessions(t *testing.T) {
	sup := &fakeSupervisor{sessions: []pkg.Session{
		{ID: "a", State: pkg.StateGenerating, Model: "m1", ModelSource: "assistant"},
		{ID: "b", State: pkg.StateIdle, Model: "m2", ModelSource: "assistant"},
	}}
	rec := httptest.NewRecorder()
	handler.Sessions(sup).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/1.0/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []pkg.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

// A missing session must be 404, not 500 — the status contract is part of the
// API, and a blanket 500 would make a caller retry something that cannot change.
func TestUnknownSessionIsNotFound(t *testing.T) {
	sup := &fakeSupervisor{getErr: pkg.ErrSessionNotFound}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/1.0/sessions/nope", nil)
	handler.Session(sup).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAnswerBodyIsDecoded(t *testing.T) {
	sup := &fakeSupervisor{}
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"request_id":"r1","behavior":"allow"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/1.0/sessions/a/answer", body)
	handler.Session(sup).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
