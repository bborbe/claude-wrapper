// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// managerWith builds a manager holding one agent, so the read-back gate can be
// exercised without a controller.
func managerWith(agent *Agent) *Manager {
	manager := NewManager(NewClient(NewID), nil, Config{})
	manager.agents[agent.AgentID] = agent
	manager.order = append(manager.order, agent.AgentID)
	return manager
}

func TestReadPushConfigReturnsWhatTheWorkerHolds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("authorization"); got != "Bearer worker-token" {
			t.Errorf("authorization = %q, want the worker token", got)
		}
		_, _ = w.Write([]byte(
			`{"jsonrpc":"2.0","id":1,"result":{"configs":[{"url":"http://push"}]}}`,
		))
	}))
	defer server.Close()

	manager := managerWith(&Agent{
		AgentID:          "agent_1",
		A2AURL:           server.URL,
		Token:            "worker-token",
		ProtocolVersion:  "1.0",
		PushRegistration: "attempted-ok",
		PushTaskID:       "task-1",
	})

	result, err := manager.ReadPushConfig(context.Background(), "agent_1")
	if err != nil {
		t.Fatalf("ReadPushConfig returned %v", err)
	}
	if result["task_id"] != "task-1" {
		t.Errorf("task_id = %v, want task-1", result["task_id"])
	}
	configs, ok := result["configs"].([]any)
	if !ok || len(configs) != 1 {
		t.Fatalf("configs = %#v, want one entry", result["configs"])
	}
}

func TestReadPushConfigRefusesWithoutAnAcceptedRegistration(t *testing.T) {
	// ⚠️ `attempted-error` is the state that matters: reading back a task the
	// worker never accepted would report a missing config as a channel fault.
	for _, state := range []string{"not-attempted", "attempted-error", ""} {
		manager := managerWith(&Agent{
			AgentID:          "agent_1",
			A2AURL:           "http://worker",
			Token:            "worker-token",
			PushRegistration: state,
		})
		_, err := manager.ReadPushConfig(context.Background(), "agent_1")
		if err == nil {
			t.Fatalf("state %q: expected a refusal", state)
		}
		if !strings.Contains(err.Error(), "no accepted push registration") {
			t.Errorf("state %q: error = %v, want the named refusal", state, err)
		}
	}
}

func TestReadPushConfigRefusesWithoutATaskID(t *testing.T) {
	// An accepted registration that never recorded its task id cannot be read
	// back either — the list is keyed by that id.
	manager := managerWith(&Agent{
		AgentID:          "agent_1",
		A2AURL:           "http://worker",
		Token:            "worker-token",
		PushRegistration: "attempted-ok",
	})
	if _, err := manager.ReadPushConfig(context.Background(), "agent_1"); err == nil {
		t.Fatal("expected a refusal when the task id is absent")
	}
}

func TestReadPushConfigRefusesWithoutAnEndpointOrToken(t *testing.T) {
	manager := managerWith(&Agent{
		AgentID:          "agent_1",
		PushRegistration: "attempted-ok",
		PushTaskID:       "task-1",
	})
	_, err := manager.ReadPushConfig(context.Background(), "agent_1")
	if err == nil {
		t.Fatal("expected a refusal when the endpoint and token are absent")
	}
	if !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("error = %v, want the re-adopted-row refusal", err)
	}
}

func TestReadPushConfigRefusesAnUnknownAgent(t *testing.T) {
	manager := managerWith(&Agent{AgentID: "agent_1"})
	if _, err := manager.ReadPushConfig(context.Background(), "agent_9"); err == nil {
		t.Fatal("expected a refusal for an unknown agent")
	}
}
