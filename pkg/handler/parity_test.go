// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
	"github.com/bborbe/claude-wrapper/pkg/handler"
)

// sessionActions is the literal expected action set, and it is deliberately a
// list of its own rather than something derived from handler.Routes. A row
// deleted from the table while its action stays reachable is exactly the drift
// this file exists to catch, and a check that reads the table cannot see it.
var sessionActions = []string{
	"answer",
	"message",
	"question",
	"events",
	"commands",
	"model",
	"permission-mode",
}

// sequencerProbes carries, per sequencer-backed action, the request that
// reaches the row's own work rather than stopping at a validation refusal.
// The specs assert it covers every NeedsSequencer and MultiStep row, so a new
// such row cannot land without one.
var sequencerProbes = map[string]string{
	"commands": `{"macro":"compact","confirm":"yes"}`,
	"model":    `{"model":"deepseek-v4-flash"}`,
}

var _ = Describe("session route parity", func() {
	// A route registered twice, or a row whose action no longer matches the
	// dispatcher, both read as "every route has a test" while one of them runs
	// against the wrong handler. The literal list is what makes the omission
	// visible; the length check is what catches a stray extra row.
	It("registers every session action exactly once", func() {
		routes := handler.Routes(&fakeSupervisor{}, &fakeSequencer{}, pkg.NewSessionLocks())

		seen := map[string]int{}
		for _, route := range routes {
			seen[route.Action]++
		}
		for _, action := range sessionActions {
			Expect(seen).To(HaveKey(action), "action %q has no row in the route table", action)
			Expect(seen[action]).To(Equal(1), "action %q is registered %d times", action, seen[action])
		}
		Expect(routes).To(HaveLen(len(sessionActions)),
			"the table carries %d rows for %d expected actions", len(routes), len(sessionActions))
	})

	// "Does not need a sequencer" is a decision, not an oversight, so it has to
	// be stated. Without this a row can silently drop an invariant by declaring
	// the flag false and saying nothing.
	It("explains every row that does not need a sequencer", func() {
		routes := handler.Routes(&fakeSupervisor{}, &fakeSequencer{}, pkg.NewSessionLocks())
		for _, route := range routes {
			if route.NeedsSequencer {
				continue
			}
			Expect(strings.TrimSpace(route.SequencerFree)).NotTo(BeEmpty(),
				"action %q declares no sequencer need and gives no reason", route.Action)
		}
	})

	// The floors are what stop the two invariant specs below from passing on an
	// empty population: a table where every row declares false runs neither case
	// and stays green.
	It("keeps the invariant populations non-empty", func() {
		routes := handler.Routes(&fakeSupervisor{}, &fakeSequencer{}, pkg.NewSessionLocks())

		var needsSequencer, multiStep int
		for _, route := range routes {
			if route.NeedsSequencer {
				needsSequencer++
			}
			if route.MultiStep {
				multiStep++
			}
		}
		Expect(needsSequencer).To(BeNumerically(">=", 2),
			"only %d rows declare needsSequencer; the population cannot shrink to zero", needsSequencer)
		Expect(multiStep).To(BeNumerically(">=", 1),
			"no row declares multiStep, so the partial-reporting invariant covers nothing")
	})

	// The server's WriteTimeout is a deadline applied to the connection when the
	// request is read, so a handler that does not clear it cannot write a single
	// byte once it has passed. A recorder cannot express this — it reports
	// ErrNotSupported, which clearWriteDeadline deliberately swallows — so the
	// probe runs a real server with a deadline that has already expired and
	// asserts that *some* response still arrives.
	//
	// The request is a bare GET to every action: each handler clears the deadline
	// before it validates anything, so even a 405 proves the route honours the
	// invariant, and no per-row payload is needed.
	It("clears the write deadline on every route", func() {
		routes := handler.Routes(&fakeSupervisor{}, &fakeSequencer{}, pkg.NewSessionLocks())
		for _, route := range routes {
			By("action " + route.Action)
			srv := httptest.NewUnstartedServer(
				handler.Session(&fakeSupervisor{}, &fakeSequencer{}, pkg.NewSessionLocks()))
			srv.Config.WriteTimeout = time.Nanosecond
			srv.Start()

			conn, err := net.Dial("tcp", srv.Listener.Addr().String())
			Expect(err).NotTo(HaveOccurred())
			// Connection: close, so a completed response ends the connection and
			// the read returns at once. Without it HTTP/1.1 keep-alive leaves the
			// socket open and every row would cost the full read deadline.
			_, err = fmt.Fprintf(conn,
				"GET /api/1.0/sessions/a/%s HTTP/1.1\r\nHost: probe\r\nConnection: close\r\n\r\n",
				route.Action)
			Expect(err).NotTo(HaveOccurred())

			// Bounded read: the event stream never ends, so the read deadline is
			// what stops it, and the bytes already received are the assertion.
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			body, _ := io.ReadAll(conn)
			_ = conn.Close()
			srv.Close()

			Expect(string(body)).To(ContainSubstring("HTTP/1.1 "),
				"action %q wrote no response — its handler does not clear the write deadline",
				route.Action)
		}
	})

	// A nil sequencer must read as "not configured": 503, never a panic and never
	// a block. The bounded goroutine is what makes "never a block" assertable —
	// a handler that waits would otherwise hang the whole suite.
	It("answers 503 within 2s when a sequencer-backed row has no sequencer", func() {
		routes := handler.Routes(&fakeSupervisor{}, nil, pkg.NewSessionLocks())
		for _, route := range routes {
			if !route.NeedsSequencer {
				continue
			}
			By("action " + route.Action)
			payload, ok := sequencerProbes[route.Action]
			Expect(ok).To(BeTrue(), "no probe for sequencer-backed action %q", route.Action)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost,
				"/api/1.0/sessions/a/"+route.Action, strings.NewReader(payload))

			done := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(done)
				handler.Session(&fakeSupervisor{}, nil, pkg.NewSessionLocks()).ServeHTTP(rec, req)
			}()
			Eventually(done).WithTimeout(2*time.Second).
				Should(BeClosed(), "action %q blocked instead of failing fast", route.Action)

			Expect(rec.Code).To(Equal(http.StatusServiceUnavailable),
				"action %q answered %d for a nil sequencer, want 503", route.Action, rec.Code)
		}
	})

	// A run that stopped early is reported, never swallowed. The signal field
	// differs by route — ModelChange carries `partial`, SequenceResult carries
	// `aborted` — so the assertion reads the decoded body rather than a Go type,
	// and requires the steps report alongside it either way.
	It("reports a partial run on every multi-step row", func() {
		routes := handler.Routes(&fakeSupervisor{}, &fakeSequencer{}, pkg.NewSessionLocks())
		for _, route := range routes {
			if !route.MultiStep {
				continue
			}
			By("action " + route.Action)
			payload, ok := sequencerProbes[route.Action]
			Expect(ok).To(BeTrue(), "no probe for multi-step action %q", route.Action)

			seq := &fakeSequencer{
				result: &pkg.SequenceResult{
					Macro:   "probe",
					Steps:   []pkg.StepResult{{Index: 1, Command: "/probe", Status: pkg.StepAborted}},
					Aborted: true,
				},
				err: errors.Errorf(context.Background(), "probe turn never closed"),
			}
			sup := &fakeSupervisor{getModels: []string{"old-model", "old-model"}}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost,
				"/api/1.0/sessions/a/"+route.Action, strings.NewReader(payload))
			handler.Session(sup, seq, pkg.NewSessionLocks()).ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK),
				"action %q answered %d for a partial run", route.Action, rec.Code)

			var decoded map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &decoded)).To(Succeed())

			partial, _ := decoded["partial"].(bool)
			aborted, _ := decoded["aborted"].(bool)
			Expect(partial || aborted).To(BeTrue(),
				"action %q reported a partial run as a bare success: %s", route.Action, rec.Body.String())

			steps, _ := decoded["steps"].([]any)
			Expect(steps).NotTo(BeEmpty(),
				"action %q reported a partial run with no steps", route.Action)
		}
	})
})
