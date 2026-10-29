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

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

type fakeSupervisor struct {
	sessions []pkg.Session
	getErr   error
	spawnErr error
	events   []pkg.Event
}

func (f *fakeSupervisor) Spawn(_ context.Context, _ pkg.SpawnOptions) (*pkg.Session, error) {
	if f.spawnErr != nil {
		return nil, f.spawnErr
	}
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

func (f *fakeSupervisor) Send(_ context.Context, _, _ string) error {
	return f.getErr
}

func (f *fakeSupervisor) RegisterQuestion(_ context.Context, _ string, _ pkg.Question) error {
	return f.getErr
}

func (f *fakeSupervisor) PollAnswer(_ context.Context, _ string) (string, bool, error) {
	return "", false, f.getErr
}

func (f *fakeSupervisor) Stop(_ context.Context, _ string) error {
	return f.getErr
}

func (f *fakeSupervisor) Subscribe(_ context.Context, _ string) ([]pkg.Event, <-chan pkg.Event, func(), error) {
	if f.getErr != nil {
		return nil, nil, nil, f.getErr
	}
	live := make(chan pkg.Event)
	close(live)
	return f.events, live, func() {}, nil
}

var _ = Describe("handler", func() {
	It("reports healthy", func() {
		rec := httptest.NewRecorder()
		handler.Healthz().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	// Pins the mapping the README's status table advertises. Without this, a
	// revert of `writeError(resp, statusFor(err), err)` to a hardcoded 500 keeps
	// every other spec green while a refused spawn starts reporting itself as a
	// server fault.
	It("reports a refused spawn as 400", func() {
		fake := &fakeSupervisor{
			spawnErr: errors.Wrapf(context.Background(), pkg.ErrInvalidSpawnOptions, "bad env"),
		}
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"env":{"":"x"}}`)
		handler.Sessions(fake).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/1.0/sessions", body))

		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		var payload map[string]any
		Expect(json.Unmarshal(rec.Body.Bytes(), &payload)).To(Succeed())
		Expect(payload).To(HaveKey("error"))
	})

	It("lists every session", func() {
		sup := &fakeSupervisor{sessions: []pkg.Session{
			{ID: "a", State: pkg.StateGenerating, Model: "m1", ModelSource: "assistant"},
			{ID: "b", State: pkg.StateIdle, Model: "m2", ModelSource: "assistant"},
		}}
		rec := httptest.NewRecorder()
		handler.Sessions(sup).ServeHTTP(
			rec, httptest.NewRequest(http.MethodGet, "/api/1.0/sessions", nil),
		)
		Expect(rec.Code).To(Equal(http.StatusOK))
		var got []pkg.Session
		Expect(json.Unmarshal(rec.Body.Bytes(), &got)).To(Succeed())
		Expect(got).To(HaveLen(2))
	})

	// A missing session must be 404, not 500 — the status contract is part of
	// the API, and a blanket 500 would make a caller retry something that
	// cannot change.
	It("returns 404 for an unknown session", func() {
		sup := &fakeSupervisor{getErr: pkg.ErrSessionNotFound}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/sessions/nope", nil)
		handler.Session(sup, nil).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("decodes an answer body", func() {
		sup := &fakeSupervisor{}
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"request_id":"r1","behavior":"allow"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/sessions/a/answer", body)
		handler.Session(sup, nil).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	// The replay is the whole point of the route: a subscriber that arrives
	// after a turn must still see that turn, not join at an arbitrary point.
	It("streams the replay before the live channel and ends explicitly", func() {
		sup := &fakeSupervisor{events: []pkg.Event{
			{"type": "assistant"},
			{"type": "result"},
		}}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/sessions/a/events", nil)
		handler.Session(sup, nil).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(Equal("text/event-stream"))
		// Assert the payloads, not a frame count: the closing `event: end` frame
		// carries a `data:` line of its own, so counting them proves nothing about
		// the replay.
		Expect(rec.Body.String()).To(ContainSubstring(`data: {"type":"assistant"}`))
		Expect(rec.Body.String()).To(ContainSubstring(`data: {"type":"result"}`))
		Expect(rec.Body.String()).To(ContainSubstring("event: end"))
	})

	It("refuses a non-GET on the events route", func() {
		sup := &fakeSupervisor{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/sessions/a/events", nil)
		handler.Session(sup, nil).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
	})

	// A macro that can end a child is a remote kill switch: the request is
	// refused outright rather than started and reported on later.
	It("refuses a production-touching macro with no confirmation", func() {
		sup := &fakeSupervisor{}
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"macro":"compact"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/sessions/a/commands", body)
		handler.Session(sup, nil).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusConflict))
	})

	It("refuses an unknown macro", func() {
		sup := &fakeSupervisor{}
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"macro":"nope","confirm":"yes"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/sessions/a/commands", body)
		handler.Session(sup, nil).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("refuses a non-POST on the commands route", func() {
		sup := &fakeSupervisor{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/sessions/a/commands", nil)
		handler.Session(sup, nil).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
	})
})
