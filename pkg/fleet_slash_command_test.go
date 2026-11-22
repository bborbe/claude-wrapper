// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// fleetStatusRegistry is a SessionRegistry whose Status can fail per session, so
// the unreadable-status skip is reachable from a test.
type fleetStatusRegistry struct {
	reloadRegistry
	statusErr map[string]error
}

func (f *fleetStatusRegistry) Status(ctx context.Context, id string) (*string, error) {
	if err := f.statusErr[id]; err != nil {
		return nil, err
	}
	return f.reloadRegistry.Status(ctx, id)
}

// outcomes maps a report onto session id → outcome, so a test can assert the
// whole vocabulary at once.
func outcomes(results []FleetCommandResult) map[string]FleetOutcome {
	byID := make(map[string]FleetOutcome, len(results))
	for _, result := range results {
		byID[result.SessionID] = result.Outcome
	}
	return byID
}

var _ = ginkgo.Describe("allowedSlashCommands", func() {
	ginkgo.It("accepts /mcp reconnect tts and refuses every near miss", func() {
		gomega.Expect(allowedSlashCommands.Contains("/mcp reconnect tts")).To(gomega.BeTrue())
		// Exact match, no arguments: a prefix or an argument-bearing spelling is a
		// different command and must stay refused.
		for _, nearMiss := range []string{
			"/mcp reconnect",
			"/mcp reconnect other",
			"/mcp reconnect tts --force",
			"/mcp reconnect tts; rm -rf /",
		} {
			gomega.Expect(allowedSlashCommands.Contains(nearMiss)).
				To(gomega.BeFalse(), "%q must not be allowlisted", nearMiss)
		}
	})
})

var _ = ginkgo.Describe("RunFleetCommand", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("delivers to every idle session and reports delivered", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}, {PID: 11, SessionID: "s2"}},
			status:  map[string]*string{"s1": statusPtr("idle"), "s2": statusPtr("idle")},
		}
		slash := &reloadSlash{}
		report, err := RunFleetCommand(
			ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}),
			"/mcp reconnect tts", nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(outcomes(report.Results)).To(gomega.Equal(map[string]FleetOutcome{
			"s1": FleetDelivered,
			"s2": FleetDelivered,
		}))
		gomega.Expect(slash.calls).To(gomega.Equal(map[string]int{"s1": 1, "s2": 1}))
		gomega.Expect(report.Command).To(gomega.Equal("/mcp reconnect tts"))
	})

	ginkgo.It("skips a busy or waiting session without typing into it", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}, {PID: 11, SessionID: "s2"}},
			status:  map[string]*string{"s1": statusPtr("busy"), "s2": statusPtr("waiting")},
		}
		slash := &reloadSlash{}
		report, err := RunFleetCommand(
			ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}),
			"/mcp reconnect tts", nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(outcomes(report.Results)).To(gomega.Equal(map[string]FleetOutcome{
			"s1": FleetBusy,
			"s2": FleetBusy,
		}))
		gomega.Expect(slash.calls).To(gomega.BeEmpty(), "a busy session must never be typed into")
	})

	ginkgo.It("reports no-pane for a shell", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}},
			status:  map[string]*string{"s1": statusPtr("shell")},
		}
		slash := &reloadSlash{}
		report, err := RunFleetCommand(
			ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}),
			"/mcp reconnect tts", nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(outcomes(report.Results)).To(gomega.Equal(map[string]FleetOutcome{
			"s1": FleetNoPane,
		}))
		gomega.Expect(slash.calls).To(gomega.BeEmpty())
	})

	ginkgo.It("reports refused when the composer never became ready", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}},
			status:  map[string]*string{"s1": statusPtr("idle")},
		}
		slash := &reloadSlash{errByID: map[string]error{
			"s1": errors.New("pane 7 showed no empty composer within 8s, so nothing was typed"),
		}}
		report, err := RunFleetCommand(
			ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}),
			"/mcp reconnect tts", nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(outcomes(report.Results)).To(gomega.Equal(map[string]FleetOutcome{
			"s1": FleetRefused,
		}))
	})

	ginkgo.It("reports failed for an unclassifiable delivery error", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}},
			status:  map[string]*string{"s1": statusPtr("idle")},
		}
		slash := &reloadSlash{errByID: map[string]error{"s1": errors.New("stdin write failed")}}
		report, err := RunFleetCommand(
			ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}),
			"/mcp reconnect tts", nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(outcomes(report.Results)).To(gomega.Equal(map[string]FleetOutcome{
			"s1": FleetFailed,
		}))
	})

	ginkgo.It("reports failed when a session's status cannot be read", func() {
		registry := &fleetStatusRegistry{
			reloadRegistry: reloadRegistry{entries: []RegistryEntry{{PID: 10, SessionID: "s1"}}},
			statusErr:      map[string]error{"s1": errors.New("registry down")},
		}
		slash := &reloadSlash{}
		report, err := RunFleetCommand(
			ctx,
			ReloadFleetDeps{
				Registry: registry,
				Roster:   &reloadRoster{},
				Slash:    slash,
				Markers:  &reloadMarkers{},
			},
			"/mcp reconnect tts", nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(outcomes(report.Results)).To(gomega.Equal(map[string]FleetOutcome{
			"s1": FleetFailed,
		}))
		gomega.Expect(slash.calls).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a command outside the allowlist without touching a session", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}},
			status:  map[string]*string{"s1": statusPtr("idle")},
		}
		slash := &reloadSlash{}
		_, err := RunFleetCommand(
			ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}),
			"/mcp reconnect other", nil,
		)
		gomega.Expect(errors.Is(err, ErrSlashCommandNotAllowed)).To(gomega.BeTrue())
		gomega.Expect(slash.calls).To(gomega.BeEmpty())
	})

	ginkgo.It("scopes the run to the named sessions", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}, {PID: 11, SessionID: "s2"}},
			status:  map[string]*string{"s1": statusPtr("idle"), "s2": statusPtr("idle")},
		}
		slash := &reloadSlash{}
		report, err := RunFleetCommand(
			ctx, reloadDeps(registry, &reloadRoster{}, slash, &reloadMarkers{}),
			"/mcp reconnect tts", []string{"s2"},
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(outcomes(report.Results)).To(gomega.Equal(map[string]FleetOutcome{
			"s2": FleetDelivered,
		}))
		gomega.Expect(slash.calls).To(gomega.Equal(map[string]int{"s2": 1}))
	})

	ginkgo.It("reports the population diff between the registry and the roster", func() {
		registry := &reloadRegistry{
			entries: []RegistryEntry{{PID: 10, SessionID: "s1"}},
			status:  map[string]*string{"s1": statusPtr("idle")},
		}
		roster := &reloadRoster{rows: []AgentRow{{SessionID: "s2"}}}
		report, err := RunFleetCommand(
			ctx, reloadDeps(registry, roster, &reloadSlash{}, &reloadMarkers{}),
			"/mcp reconnect tts", nil,
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(report.Missing).To(gomega.ConsistOf("s1"))
		gomega.Expect(report.Extra).To(gomega.ConsistOf("s2"))
	})
})
