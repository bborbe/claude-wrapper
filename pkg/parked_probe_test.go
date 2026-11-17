// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// parkedProbeCards is this file's fake OpenCardReader.
//
// The name is deliberately specific so it cannot collide with the card fake a
// later prompt adds.
type parkedProbeCards struct {
	ids map[string]struct{}
	err error
}

func (c *parkedProbeCards) OpenSessionIDs(context.Context) (map[string]struct{}, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.ids, nil
}

var _ = ginkgo.Describe("ParkedProbe", func() {
	ginkgo.It("records the pull request command from the declaration", func() {
		ctx := context.Background()
		out := []byte(`{"headRefOid":"abc123","reviewDecision":"APPROVED","state":"OPEN"}`)
		cases := []struct {
			name string
			ref  PullRequestRef
			want string
		}{
			{
				name: "claude-wrapper",
				ref:  PullRequestRef{Owner: "bborbe", Repo: "claude-wrapper", Number: 412},
				want: "pr view 412 --repo bborbe/claude-wrapper --json " +
					"headRefOid,reviewDecision,state",
			},
			{
				name: "other-repo",
				ref:  PullRequestRef{Owner: "bborbe", Repo: "other-repo", Number: 9},
				want: "pr view 9 --repo bborbe/other-repo --json " +
					"headRefOid,reviewDecision,state",
			},
		}
		for _, tc := range cases {
			runner := &recordingRunner{out: out}
			observation, err := PRProbe{Runner: runner}.Probe(
				ctx, FlaggedRow{}, ParkedDeclaration{PR: tc.ref},
			)
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(runner.argv[0]).To(gomega.Equal("gh"))
			argv := strings.Join(runner.argv[1:], " ")
			fmt.Printf("argv=%s\n", argv)
			gomega.Expect(argv).To(gomega.Equal(tc.want))
			gomega.Expect(observation.Fingerprint).To(gomega.Equal("abc123|APPROVED|OPEN"))
			gomega.Expect(observation.Satisfied).To(gomega.BeFalse())
		}

		failures := []struct {
			name   string
			runner CommandRunner
		}{
			{
				name:   "runner error",
				runner: &recordingRunner{err: stderrors.New("exit status 1")},
			},
			{
				name:   "non-zero-shaped failure",
				runner: &recordingRunner{out: []byte("exit status 1\n")},
			},
		}
		for _, tc := range failures {
			observation, err := PRProbe{Runner: tc.runner}.Probe(
				ctx, FlaggedRow{}, ParkedDeclaration{PR: PullRequestRef{
					Owner: "bborbe", Repo: "claude-wrapper", Number: 412,
				}},
			)
			fmt.Printf("failure=%s err=%t\n", tc.name, err != nil)
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(observation).To(gomega.Equal(ParkedObservation{}))
		}
	})

	ginkgo.It("reports the card observable and fails closed", func() {
		ctx := context.Background()
		row := FlaggedRow{SessionID: "session-1"}
		cases := []struct {
			name string
			// cards is the reader; nil means an unconfigured store.
			cards OpenCardReader
			row   FlaggedRow
		}{
			{name: "nil store", cards: nil, row: row},
			{
				name:  "store error",
				cards: &parkedProbeCards{err: stderrors.New("store unavailable")},
				row:   row,
			},
			{
				name:  "card open",
				cards: &parkedProbeCards{ids: map[string]struct{}{"session-1": {}}},
				row:   row,
			},
			{
				name:  "card answered",
				cards: &parkedProbeCards{ids: map[string]struct{}{"other": {}}},
				row:   row,
			},
			{name: "no session id", cards: &parkedProbeCards{}, row: FlaggedRow{}},
		}
		for _, tc := range cases {
			observation, err := CardProbe{Cards: tc.cards}.Probe(
				ctx, tc.row, ParkedDeclaration{Class: ParkedOnCard},
			)
			fmt.Printf(
				"case=%s satisfied=%t fingerprint=%s err=%t\n",
				tc.name, observation.Satisfied, observation.Fingerprint, err != nil,
			)
			switch tc.name {
			case "nil store", "store error", "no session id":
				gomega.Expect(err).NotTo(gomega.BeNil())
				gomega.Expect(observation).To(gomega.Equal(ParkedObservation{}))
			case "card open":
				gomega.Expect(err).To(gomega.BeNil())
				gomega.Expect(observation.Satisfied).To(gomega.BeFalse())
				gomega.Expect(observation.Fingerprint).To(gomega.Equal("open"))
			case "card answered":
				gomega.Expect(err).To(gomega.BeNil())
				gomega.Expect(observation.Satisfied).To(gomega.BeTrue())
				gomega.Expect(observation.Fingerprint).To(gomega.Equal("answered"))
			}
		}
	})

	ginkgo.It("dials the declared host and reads the declared clock", func() {
		ctx := context.Background()

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		gomega.Expect(err).To(gomega.BeNil())
		defer func() { _ = listener.Close() }()
		listenAddr, ok := listener.Addr().(*net.TCPAddr)
		gomega.Expect(ok).To(gomega.BeTrue())

		deadListener, err := net.Listen("tcp", "127.0.0.1:0")
		gomega.Expect(err).To(gomega.BeNil())
		deadAddr, ok := deadListener.Addr().(*net.TCPAddr)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(deadListener.Close()).To(gomega.Succeed())

		base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		clock := fixedNow(base)

		hostCases := []struct {
			name    string
			port    int
			reach   bool
			wantErr bool
		}{
			{name: "reachable", port: listenAddr.Port, reach: true},
			{name: "unreachable", port: deadAddr.Port, reach: false},
		}
		for _, tc := range hostCases {
			observation, probeErr := (HostProbe{}).Probe(
				ctx, FlaggedRow{},
				ParkedDeclaration{Class: ParkedOnHost, Host: "127.0.0.1", Port: tc.port},
			)
			fmt.Printf(
				"case=%s reachable=%t satisfied=%t err=%t\n",
				tc.name, observation.Fingerprint == "reachable",
				observation.Satisfied, probeErr != nil,
			)
			gomega.Expect(probeErr).To(gomega.BeNil())
			gomega.Expect(observation.Satisfied).To(gomega.Equal(tc.reach))
			gomega.Expect(observation.Fingerprint == "reachable").To(gomega.Equal(tc.reach))
		}

		future := base.Add(time.Hour)
		past := base.Add(-time.Hour)
		clockCases := []struct {
			name     string
			deadline *time.Time
			satisfy  bool
		}{
			{name: "deadline in the future", deadline: &future, satisfy: false},
			{name: "deadline in the past", deadline: &past, satisfy: true},
		}
		for _, tc := range clockCases {
			observation, probeErr := ClockProbe{Now: clock}.Probe(
				ctx, FlaggedRow{},
				ParkedDeclaration{Class: ParkedUntil, Deadline: tc.deadline},
			)
			fmt.Printf(
				"case=%s reachable=false satisfied=%t err=%t\n",
				tc.name, observation.Satisfied, probeErr != nil,
			)
			gomega.Expect(probeErr).To(gomega.BeNil())
			gomega.Expect(observation.Satisfied).To(gomega.Equal(tc.satisfy))
		}
	})
})
