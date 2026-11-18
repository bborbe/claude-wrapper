// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

// stubRegistry answers Live from a fixed set. Every other method is unused by
// the liveness path and returns a zero value, so a spec that starts depending on
// one fails loudly here rather than silently reading a fabricated registry.
type stubRegistry struct {
	live    []pkg.RegistryEntry
	liveErr error
}

func (s *stubRegistry) Status(context.Context, string) (*string, error) {
	return nil, nil
}

func (s *stubRegistry) Cwd(context.Context, string) (*string, error) {
	return nil, nil
}

func (s *stubRegistry) Live(context.Context) ([]pkg.RegistryEntry, error) {
	if s.liveErr != nil {
		return nil, s.liveErr
	}
	return s.live, nil
}

func (s *stubRegistry) Entries(context.Context) ([]pkg.RegistryEntry, error) {
	return s.live, nil
}

func (s *stubRegistry) Named(context.Context, string) ([]string, error) {
	return nil, nil
}

func (s *stubRegistry) NamesForSession(context.Context, string) ([]string, error) {
	return nil, nil
}

// stubHeartbeats answers Rows from a fixed set, and can be made to fail so the
// corroboration-absent path is exercised.
type stubHeartbeats struct {
	rows []pkg.HeartbeatRow
	err  error
}

func (s *stubHeartbeats) Rows(context.Context) ([]pkg.HeartbeatRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

// serveLiveness runs one request through the handler and returns the recorder.
func serveLiveness(
	method string,
	registry pkg.SessionRegistry,
	supervisor pkg.Supervisor,
	heartbeats pkg.HeartbeatReader,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/1.0/liveness", nil)
	rec := httptest.NewRecorder()
	handler.Liveness(registry, supervisor, heartbeats).ServeHTTP(rec, req)
	return rec
}

var _ = Describe("Liveness", func() {
	var registry *stubRegistry
	var heartbeats *stubHeartbeats
	var supervisor *fakeSupervisor

	BeforeEach(func() {
		registry = &stubRegistry{
			live: []pkg.RegistryEntry{{SessionID: "live-one", PID: 4242}},
		}
		heartbeats = &stubHeartbeats{
			rows: []pkg.HeartbeatRow{{SessionID: "live-one", Live: true}},
		}
		supervisor = &fakeSupervisor{}
	})

	It("answers 200 with one verdict per session", func() {
		rec := serveLiveness(http.MethodGet, registry, supervisor, heartbeats)

		Expect(rec.Code).To(Equal(http.StatusOK))
		var verdicts []pkg.LivenessVerdict
		Expect(json.Unmarshal(rec.Body.Bytes(), &verdicts)).To(Succeed())
		Expect(verdicts).To(HaveLen(1))
		Expect(verdicts[0].SessionID).To(Equal("live-one"))
		Expect(verdicts[0].Liveness).To(Equal(pkg.LivenessLive))
		Expect(verdicts[0].Source).To(Equal("registry"))
	})

	// The whole point of the endpoint: a session the registry proves live is
	// live whether or not the store corroborates it.
	It("answers live with no heartbeat row at all", func() {
		heartbeats.rows = nil

		rec := serveLiveness(http.MethodGet, registry, supervisor, heartbeats)

		Expect(rec.Code).To(Equal(http.StatusOK))
		var verdicts []pkg.LivenessVerdict
		Expect(json.Unmarshal(rec.Body.Bytes(), &verdicts)).To(Succeed())
		Expect(verdicts).To(HaveLen(1))
		Expect(verdicts[0].Liveness).To(Equal(pkg.LivenessLive))
		Expect(verdicts[0].Heartbeat).To(BeNil())
	})

	// A store outage must NOT hide a live worker. This is the defect the review
	// caught: returning 500 here would answer "nothing is live" during exactly
	// the outage the primary sources can survive.
	It("answers 200 from the primary sources when the heartbeat store fails", func() {
		heartbeats.err = errors.Errorf(context.Background(), "store unreachable")

		rec := serveLiveness(http.MethodGet, registry, supervisor, heartbeats)

		Expect(rec.Code).To(Equal(http.StatusOK))
		var verdicts []pkg.LivenessVerdict
		Expect(json.Unmarshal(rec.Body.Bytes(), &verdicts)).To(Succeed())
		Expect(verdicts).To(HaveLen(1))
		Expect(verdicts[0].Liveness).To(Equal(pkg.LivenessLive))
		Expect(verdicts[0].Heartbeat).To(BeNil())
	})

	It("answers 405 for a method other than GET", func() {
		rec := serveLiveness(http.MethodPost, registry, supervisor, heartbeats)

		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
	})

	// A PRIMARY source failing is different from corroboration failing: an empty
	// set would read as "nothing is live", so it must be an error.
	It("answers 500 when the child table cannot be read", func() {
		supervisor.listErr = errors.Errorf(context.Background(), "child table unreadable")

		rec := serveLiveness(http.MethodGet, registry, supervisor, heartbeats)

		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})

	It("answers 500 when the registry cannot be read", func() {
		registry.liveErr = errors.Errorf(context.Background(), "registry unreadable")

		rec := serveLiveness(http.MethodGet, registry, supervisor, heartbeats)

		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})
})
