// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// modeSupervisor is a Supervisor whose Get and SetPermissionMode are real enough
// to record what the tool asked for.
//
// The unused methods panic rather than return zero values: a switch that started
// spawning or answering would otherwise read as a passing test that did nothing.
type modeSupervisor struct {
	fakeDurable
	sessions map[string]*Session
	// answered is the mode the fake child reports back. Empty means "echo the
	// request", which is the shape a real child has when it accepts the change.
	answered string
	calls    []modeCall
	callErr  error
}

// modeCall is one recorded permission-mode request.
type modeCall struct {
	ID   string
	Mode string
}

func (s *modeSupervisor) Spawn(context.Context, SpawnOptions) (*Session, error) {
	panic("Spawn is not used by set_agent_permission_mode")
}

func (s *modeSupervisor) List(context.Context) ([]Session, error) {
	rows := make([]Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		rows = append(rows, *session)
	}
	return rows, nil
}

func (s *modeSupervisor) Get(_ context.Context, id string) (*Session, error) {
	session, ok := s.sessions[id]
	if !ok {
		return nil, errors.Wrapf(
			context.Background(), ErrSessionNotFound, "unknown session %q", id,
		)
	}
	return session, nil
}

func (s *modeSupervisor) Send(context.Context, string, string) error {
	panic("Send is not used by set_agent_permission_mode")
}

func (s *modeSupervisor) SetPermissionMode(_ context.Context, id, mode string) (string, error) {
	s.calls = append(s.calls, modeCall{ID: id, Mode: mode})
	if s.callErr != nil {
		return "", s.callErr
	}
	if s.answered != "" {
		return s.answered, nil
	}
	return mode, nil
}

func (s *modeSupervisor) Answer(context.Context, string, string, Answer) error {
	panic("Answer is not used by set_agent_permission_mode")
}

func (s *modeSupervisor) RegisterQuestion(context.Context, string, Question) error {
	panic("RegisterQuestion is not used by set_agent_permission_mode")
}

func (s *modeSupervisor) ForgetQuestion(context.Context, string, string) error {
	panic("ForgetQuestion is not used by set_agent_permission_mode")
}

func (s *modeSupervisor) PollAnswer(context.Context, string) (PendingAnswer, error) {
	panic("PollAnswer is not used by set_agent_permission_mode")
}

func (s *modeSupervisor) Stop(context.Context, string) error {
	panic("Stop is not used by set_agent_permission_mode")
}

func (s *modeSupervisor) Subscribe(context.Context, string) ([]Event, <-chan Event, func(), error) {
	panic("Subscribe is not used by set_agent_permission_mode")
}

// heldSession is one live headless child, the shape the supervisor's map holds.
func heldSession(id, mode string) *Session {
	return &Session{
		ID:             id,
		State:          StateIdle,
		Mode:           SpawnModeHeadless,
		PermissionMode: mode,
		Cwd:            "/tmp",
		StartedAt:      time.Now(),
	}
}

// modeServer builds a roster server whose only populated arm is the mode switch.
func modeServer(supervisor *modeSupervisor, ledger *rosterLedger, log PermissionModeLog) *RosterToolServer {
	if ledger == nil {
		ledger = &rosterLedger{}
	}
	return &RosterToolServer{
		Roster: NewRoster(
			supervisor, ledger, &rosterLiveness{live: map[string]struct{}{}}, nil, nil,
		),
		Supervisor:        supervisor,
		Ledger:            ledger,
		Locks:             NewSessionLocks(),
		PermissionModeLog: log,
	}
}

// setModeResult is what one set_agent_permission_mode call answered.
type setModeResult struct {
	IsError bool
	Text    string
}

// callSetMode invokes the tool and decodes the MCP result envelope.
func callSetMode(server *RosterToolServer, agentID, mode string) setModeResult {
	arguments, err := json.Marshal(map[string]string{"agent_id": agentID, "mode": mode})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"set_agent_permission_mode","arguments":%s}}`,
		arguments,
	)
	recorder := postRPC(server, body, "caller-session")
	gomega.Expect(recorder.Code).To(gomega.Equal(http.StatusOK))
	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	gomega.Expect(json.Unmarshal(recorder.Body.Bytes(), &envelope)).To(gomega.Succeed())
	gomega.Expect(envelope.Result.Content).NotTo(gomega.BeEmpty())
	return setModeResult{IsError: envelope.Result.IsError, Text: envelope.Result.Content[0].Text}
}

// decodeSetModeReply decodes a successful reply body.
func decodeSetModeReply(text string) map[string]any {
	var reply map[string]any
	gomega.Expect(json.Unmarshal([]byte(text), &reply)).To(gomega.Succeed())
	return reply
}

// failingModeLog is a recorder whose write always fails, which is the disk-full
// case: the switch has landed in the child and the audit line has not.
type failingModeLog struct{}

func (failingModeLog) Write(context.Context, PermissionModeChange) error {
	return errors.Errorf(context.Background(), "disk is full")
}

var _ = ginkgo.Describe("set_agent_permission_mode", func() {
	ginkgo.It("accepts auto as a permission mode", func() {
		// The closed list gates the mode-change routes, so a worker switched away
		// from `auto` could never be switched back before this. `manual` and
		// `dontAsk` are the current CLI's other choices and stay out.
		gomega.Expect(isPermissionMode(string(PermissionModeAuto))).To(gomega.BeTrue())
		gomega.Expect(AvailablePermissionModes()).To(gomega.ContainElement(PermissionModeAuto))
		gomega.Expect(isPermissionMode("manual")).To(gomega.BeFalse())
		gomega.Expect(isPermissionMode("dontAsk")).To(gomega.BeFalse())
	})

	ginkgo.It("set_agent_permission_mode allow-list", func() {
		cases := []struct {
			mode      string
			forwarded bool
		}{
			{mode: "default"},
			{mode: "acceptEdits", forwarded: true},
			{mode: "auto", forwarded: true},
			{mode: "bypassPermissions"},
			{mode: "plan"},
			{mode: "dontAsk"},
			{mode: "manual"},
			{mode: "ACCEPTEDITS"},
			{mode: ""},
		}
		for _, testCase := range cases {
			supervisor := &modeSupervisor{sessions: map[string]*Session{
				"worker-1": heldSession("worker-1", "auto"),
			}}
			result := callSetMode(modeServer(supervisor, nil, nil), "worker-1", testCase.mode)
			ginkgo.GinkgoWriter.Printf(
				"case=%s forwarded=%t\n", testCase.mode, len(supervisor.calls) > 0,
			)
			if testCase.forwarded {
				gomega.Expect(result.IsError).To(gomega.BeFalse())
				gomega.Expect(supervisor.calls).To(gomega.HaveLen(1))
				gomega.Expect(supervisor.calls[0].Mode).To(gomega.Equal(testCase.mode))
				continue
			}
			// A refused mode never reaches the supervisor, whatever the agent is.
			gomega.Expect(result.IsError).To(gomega.BeTrue())
			gomega.Expect(supervisor.calls).To(gomega.BeEmpty())
			gomega.Expect(result.Text).To(gomega.ContainSubstring("acceptEdits, auto"))
			if testCase.mode != "" {
				gomega.Expect(result.Text).To(gomega.ContainSubstring(testCase.mode))
			}
		}
	})

	ginkgo.It("set_agent_permission_mode eligibility", func() {
		type eligibilityCase struct {
			name     string
			server   func() (*RosterToolServer, *modeSupervisor)
			agentID  string
			mode     string
			forward  bool
			contains []string
		}
		held := func(sessions map[string]*Session) func() (*RosterToolServer, *modeSupervisor) {
			return func() (*RosterToolServer, *modeSupervisor) {
				supervisor := &modeSupervisor{sessions: sessions}
				return modeServer(supervisor, nil, nil), supervisor
			}
		}
		cases := []eligibilityCase{
			{
				name:    "headless",
				server:  held(map[string]*Session{"worker-1": heldSession("worker-1", "auto")}),
				agentID: "worker-1", mode: "acceptEdits", forward: true,
			},
			{
				// An adopted child is in the same map: the daemon holds its stdin,
				// which is the whole reason a switch reaches it.
				name:    "adopted",
				server:  held(map[string]*Session{"adopted-1": heldSession("adopted-1", "auto")}),
				agentID: "adopted-1", mode: "acceptEdits", forward: true,
			},
			{
				// ⚠️ Reached through the ledger, not the A2A manager: `a2a.Manager`
				// records a worker only inside Spawn, so this package has no seam to
				// seed one. The refusal text is the same one the manager branch emits
				// (local headless + shipping), which is the contract a caller sees.
				name: "a2a",
				server: func() (*RosterToolServer, *modeSupervisor) {
					supervisor := &modeSupervisor{sessions: map[string]*Session{}}
					ledger := &rosterLedger{rows: []LedgerRow{{
						SessionID: "a2a-1", AgentID: "a2a-1", Mode: "cluster", Location: "a2a",
					}}}
					return modeServer(supervisor, ledger, nil), supervisor
				},
				agentID: "a2a-1", mode: "acceptEdits",
				contains: []string{"local headless", "shipping"},
			},
			{
				name: "tab",
				server: func() (*RosterToolServer, *modeSupervisor) {
					supervisor := &modeSupervisor{sessions: map[string]*Session{}}
					ledger := &rosterLedger{rows: []LedgerRow{{
						SessionID: "tab-1", AgentID: "tab-1", Mode: "interactive", PaneID: "42",
					}}}
					return modeServer(supervisor, ledger, nil), supervisor
				},
				agentID: "tab-1", mode: "acceptEdits",
				contains: []string{"local headless", "pane"},
			},
			{
				name: "foreign",
				server: func() (*RosterToolServer, *modeSupervisor) {
					supervisor := &modeSupervisor{sessions: map[string]*Session{}}
					ledger := &rosterLedger{rows: []LedgerRow{{
						SessionID: "foreign-1", AgentID: "foreign-1", Mode: "headless",
					}}}
					return modeServer(supervisor, ledger, nil), supervisor
				},
				agentID: "foreign-1", mode: "acceptEdits",
				contains: []string{"local headless", "shipping"},
			},
			{
				name:    "unknown",
				server:  held(map[string]*Session{}),
				agentID: "nobody", mode: "acceptEdits",
				contains: []string{"local headless"},
			},
			{
				name: "exited",
				server: func() (*RosterToolServer, *modeSupervisor) {
					exited := heldSession("gone-1", "auto")
					exited.State = StateExited
					supervisor := &modeSupervisor{sessions: map[string]*Session{"gone-1": exited}}
					return modeServer(supervisor, nil, nil), supervisor
				},
				agentID: "gone-1", mode: "acceptEdits",
				contains: []string{"exited"},
			},
			{
				name: "busy",
				server: func() (*RosterToolServer, *modeSupervisor) {
					supervisor := &modeSupervisor{sessions: map[string]*Session{
						"busy-1": heldSession("busy-1", "auto"),
					}}
					server := modeServer(supervisor, nil, nil)
					// Held by a sequence the way a compaction holds it.
					release, ok := server.Locks.TryAcquire("busy-1")
					gomega.Expect(ok).To(gomega.BeTrue())
					ginkgo.DeferCleanup(release)
					return server, supervisor
				},
				agentID: "busy-1", mode: "acceptEdits",
				contains: []string{"running a sequence"},
			},
			{
				name:    "missing-id",
				server:  held(map[string]*Session{"worker-1": heldSession("worker-1", "auto")}),
				agentID: "", mode: "acceptEdits",
				contains: []string{"agent_id"},
			},
		}
		for _, testCase := range cases {
			server, supervisor := testCase.server()
			result := callSetMode(server, testCase.agentID, testCase.mode)
			ginkgo.GinkgoWriter.Printf(
				"case=%s forwarded=%t\n", testCase.name, len(supervisor.calls) > 0,
			)
			if testCase.forward {
				gomega.Expect(result.IsError).To(gomega.BeFalse())
				gomega.Expect(supervisor.calls).To(gomega.HaveLen(1))
				continue
			}
			gomega.Expect(result.IsError).To(gomega.BeTrue())
			gomega.Expect(supervisor.calls).To(gomega.BeEmpty())
			for _, fragment := range testCase.contains {
				gomega.Expect(result.Text).To(gomega.ContainSubstring(fragment))
			}
		}
	})

	ginkgo.It("reports the mode the child answers with", func() {
		// The child answers `default` to a request for `acceptEdits`. A reply that
		// echoed the request would read as a success the child never granted.
		supervisor := &modeSupervisor{
			sessions: map[string]*Session{"worker-1": heldSession("worker-1", "auto")},
			answered: "default",
		}
		result := callSetMode(modeServer(supervisor, nil, nil), "worker-1", "acceptEdits")
		gomega.Expect(result.IsError).To(gomega.BeFalse())
		reply := decodeSetModeReply(result.Text)
		gomega.Expect(reply).To(gomega.HaveKeyWithValue("permission_mode", "default"))
		gomega.Expect(reply).To(gomega.HaveKeyWithValue("requested", "acceptEdits"))
		gomega.Expect(reply).To(gomega.HaveKeyWithValue("previous_mode", "auto"))
	})

	ginkgo.It("records each permission mode switch", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "permission-mode-changes.jsonl")
		clock := fixedClock(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
		log := NewPermissionModeLog(path, clock)
		supervisor := &modeSupervisor{sessions: map[string]*Session{
			"worker-1": heldSession("worker-1", "auto"),
		}}
		server := modeServer(supervisor, nil, log)

		result := callSetMode(server, "worker-1", "acceptEdits")
		gomega.Expect(result.IsError).To(gomega.BeFalse())
		gomega.Expect(decodeSetModeReply(result.Text)).
			To(gomega.HaveKeyWithValue("recorded", true))

		lines := readLines(path)
		gomega.Expect(lines).To(gomega.HaveLen(1))
		var record map[string]any
		gomega.Expect(json.Unmarshal([]byte(lines[0]), &record)).To(gomega.Succeed())
		// The six keys are the frozen interface: a reader joins a switch to the
		// worker and the caller by them, and nothing else is written.
		gomega.Expect(record).To(gomega.HaveLen(6))
		for _, key := range []string{
			"ts", "agent_id", "caller_session_id", "from", "requested", "to",
		} {
			gomega.Expect(record).To(gomega.HaveKey(key))
		}
		gomega.Expect(record["agent_id"]).To(gomega.Equal("worker-1"))
		gomega.Expect(record["caller_session_id"]).To(gomega.Equal("caller-session"))
		gomega.Expect(record["from"]).To(gomega.Equal("auto"))
		gomega.Expect(record["requested"]).To(gomega.Equal("acceptEdits"))
		gomega.Expect(record["to"]).To(gomega.Equal("acceptEdits"))

		// A refused call changes nothing, so it records nothing.
		refused := callSetMode(server, "worker-1", "bypassPermissions")
		gomega.Expect(refused.IsError).To(gomega.BeTrue())
		gomega.Expect(readLines(path)).To(gomega.HaveLen(1))

		// A failed write does not undo the switch: it already reached the child.
		failing := modeServer(supervisor, nil, failingModeLog{})
		failed := callSetMode(failing, "worker-1", "auto")
		gomega.Expect(failed.IsError).To(gomega.BeFalse())
		gomega.Expect(decodeSetModeReply(failed.Text)).
			To(gomega.HaveKeyWithValue("recorded", false))
	})
})

// readLines reads a file's non-empty lines, or an empty slice when it is absent.
func readLines(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := make([]string, 0, 1)
	for _, line := range splitLines(string(raw)) {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// splitLines splits on newlines without pulling in another import in the test.
func splitLines(text string) []string {
	lines := make([]string, 0, 4)
	current := ""
	for _, char := range text {
		if char == '\n' {
			lines = append(lines, current)
			current = ""
			continue
		}
		current += string(char)
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
