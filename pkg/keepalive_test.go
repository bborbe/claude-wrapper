// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// stubProcedure is a KeepaliveProcedure whose answer the test states outright.
// It exists so the job's own contract — the line shape, the error path and the
// continue-on-error rule — is tested without a store behind it; the procedures
// themselves get their own tests against their real collaborators.
type stubProcedure struct {
	name   string
	effect KeepaliveEffect
	err    error
}

func (s stubProcedure) Name() string { return s.name }

func (s stubProcedure) Run(context.Context) (KeepaliveEffect, error) {
	return s.effect, s.err
}

var _ = ginkgo.Describe("KeepaliveJob", func() {
	ctx := context.Background()

	// job returns a job whose lines are captured instead of printed, plus the
	// slice the test reads them from.
	job := func(procedures ...KeepaliveProcedure) (*KeepaliveJob, *[]string) {
		lines := &[]string{}
		return &KeepaliveJob{
			Procedures: procedures,
			Logf: func(line string) {
				*lines = append(*lines, line)
			},
		}, lines
	}

	ginkgo.Describe("TickAndLog", func() {
		ginkgo.It("renders one line per procedure with its effect and session id", func() {
			j, lines := job(
				stubProcedure{
					name:   "dead-worker",
					effect: KeepaliveEffect{Result: KeepaliveResultResumed, SessionID: "abc123"},
				},
				stubProcedure{
					name:   "lapsed-topic-manager",
					effect: KeepaliveEffect{Result: KeepaliveResultKept, SessionID: "def456", Reason: "working"},
				},
			)

			j.TickAndLog(ctx)

			gomega.Expect(*lines).To(gomega.Equal([]string{
				"job=keepalive action=dead-worker result=resumed id=abc123",
				"job=keepalive action=lapsed-topic-manager result=kept id=def456 reason=working",
			}))
		})

		ginkgo.It("renders a quiet procedure as result=none and no id", func() {
			j, lines := job(stubProcedure{
				name:   "due-park-watch",
				effect: KeepaliveEffect{Result: KeepaliveResultNone},
			})

			j.TickAndLog(ctx)

			gomega.Expect(*lines).To(gomega.Equal([]string{
				"job=keepalive action=due-park-watch result=none",
			}))
		})

		// ⚠️ This is the stub the acceptance criterion rules out. Eight `none`
		// lines look identical to a working fleet in the log, so the only thing
		// that separates them is the effect word — which is why a `resumed`
		// without an id is refused rather than logged as work.
		ginkgo.It("refuses an effect that names no session as an error, not as work", func() {
			j, lines := job(stubProcedure{
				name:   "dead-worker",
				effect: KeepaliveEffect{Result: KeepaliveResultResumed},
			})

			j.TickAndLog(ctx)

			gomega.Expect(*lines).To(gomega.HaveLen(1))
			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("action=dead-worker"))
			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("result=error"))
			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("without a session id"))
		})

		ginkgo.It("refuses a result word the job does not know", func() {
			j, lines := job(stubProcedure{
				name:   "park-nudge",
				effect: KeepaliveEffect{Result: KeepaliveResult("nudged"), SessionID: "abc123"},
			})

			j.TickAndLog(ctx)

			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("result=error"))
			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("unknown result nudged"))
		})

		// ⚠️ The independence rule: one broken procedure must not stop the other
		// seven. A tick that aborted on the first error would let one unreadable
		// store hold the whole fleet down.
		ginkgo.It("logs a failing procedure and still runs the next one", func() {
			j, lines := job(
				stubProcedure{name: "lapsed-topic-manager", err: errors.New(ctx, "store unreadable")},
				stubProcedure{
					name:   "dead-worker",
					effect: KeepaliveEffect{Result: KeepaliveResultResumed, SessionID: "abc123"},
				},
			)

			j.TickAndLog(ctx)

			gomega.Expect(*lines).To(gomega.HaveLen(2))
			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("action=lapsed-topic-manager"))
			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("result=error"))
			gomega.Expect((*lines)[0]).To(gomega.ContainSubstring("store unreadable"))
			gomega.Expect((*lines)[1]).To(gomega.Equal(
				"job=keepalive action=dead-worker result=resumed id=abc123",
			))
		})

		ginkgo.It("writes nothing when there is no procedure configured", func() {
			j, lines := job()

			j.TickAndLog(ctx)

			gomega.Expect(*lines).To(gomega.BeEmpty())
		})
	})

	ginkgo.Describe("Tick", func() {
		ginkgo.It("returns the effects in procedure order", func() {
			j, _ := job(
				stubProcedure{name: "a", effect: KeepaliveEffect{Result: KeepaliveResultNone}},
				stubProcedure{name: "b", effect: KeepaliveEffect{Result: KeepaliveResultClosed, SessionID: "x"}},
			)

			effects, err := j.Tick(ctx)

			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(effects).To(gomega.Equal([]KeepaliveEffect{
				{Result: KeepaliveResultNone},
				{Result: KeepaliveResultClosed, SessionID: "x"},
			}))
		})

		ginkgo.It("returns an error naming the failing procedure", func() {
			j, _ := job(stubProcedure{name: "dead-worker", err: errors.New(ctx, "boom")})

			_, err := j.Tick(ctx)

			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(strings.TrimSpace(err.Error())).To(gomega.ContainSubstring("dead-worker"))
		})

		ginkgo.It("refuses a job with no procedures", func() {
			j, _ := job()

			_, err := j.Tick(ctx)

			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})

	ginkgo.Describe("KeepaliveResult", func() {
		ginkgo.It("knows every declared word and no other", func() {
			for _, known := range KeepaliveEffects {
				gomega.Expect(known.Known()).To(gomega.BeTrue(), string(known))
			}
			gomega.Expect(KeepaliveResult("nudged").Known()).To(gomega.BeFalse())
		})
	})
})
