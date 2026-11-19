// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// capturingPermissionLog records every row written, so a test can assert on the
// exact shape the manager path emits.
type capturingPermissionLog struct {
	records []PermissionRecord
}

func (l *capturingPermissionLog) Write(_ context.Context, record PermissionRecord) error {
	l.records = append(l.records, record)
	return nil
}

// answeringSupervisor overrides the two methods the roster fixture panics on: the
// manager answer path needs a session that carries a park, and an Answer that lands.
type answeringSupervisor struct {
	*rosterSupervisor
	session Session
}

func (s *answeringSupervisor) Get(context.Context, string) (*Session, error) {
	return &s.session, nil
}

func (s *answeringSupervisor) Answer(context.Context, string, string, Answer) error {
	return nil
}

// The manager's answer must carry `latency_ms`, exactly as the timeout path does.
//
// Without it the answer-speed reading has no producer: measured 2026-11-19, 0 of
// 2,024 `decided_by: manager` rows carried the field over 7 days while 877 of 877
// `timeout` rows did, because this call passed a literal nil. The assertion is
// `> 0`, not merely non-nil, so a stub write of zero cannot satisfy it either.
var _ = ginkgo.Describe("manager answer latency", func() {
	ginkgo.It("writes latency_ms as the park-to-answer interval", func() {
		parkedAt := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)
		answeredAt := parkedAt.Add(90 * time.Second)

		log := &capturingPermissionLog{}
		supervisor := &answeringSupervisor{
			rosterSupervisor: &rosterSupervisor{},
			session: Session{
				ID:  "wrapped-1",
				Cwd: "/tmp",
				Question: &Question{
					RequestID:   "req-1",
					ToolName:    "Bash",
					Input:       map[string]any{"command": "ls"},
					RequestedAt: parkedAt,
				},
			},
		}
		server := &RosterToolServer{
			Roster:     NewRoster(supervisor, nil, nil, nil, nil),
			Supervisor: supervisor,
			Decider:    &PermissionDecider{Log: log, Now: fixedNow(answeredAt)},
			Now:        fixedNow(answeredAt),
		}

		recorder := postRPC(
			server,
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"answer_permission",`+
				`"arguments":{"agent_id":"wrapped-1","request_id":"req-1","behavior":"allow"}}}`,
			"own-session",
		)
		gomega.Expect(recorder.Code).To(gomega.Equal(200))

		gomega.Expect(log.records).To(gomega.HaveLen(1))
		record := log.records[0]
		gomega.Expect(record.DecidedBy).To(gomega.Equal(DecidedByManager))
		gomega.Expect(record.LatencyMS).NotTo(gomega.BeNil(), "the manager row must carry latency_ms")
		gomega.Expect(*record.LatencyMS).To(gomega.BeNumerically(">", 0))
		gomega.Expect(*record.LatencyMS).To(gomega.Equal(90_000))
	})
})
