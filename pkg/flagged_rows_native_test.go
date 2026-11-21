// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type nativeSupervisor struct {
	children []Session
	err      error
}

func (n nativeSupervisor) List(context.Context) ([]Session, error) { return n.children, n.err }

type nativeVaults struct {
	vaults []Vault
	err    error
}

func (n nativeVaults) List(context.Context) ([]Vault, error) { return n.vaults, n.err }

// writePage writes one vault page, creating its folder.
func writePage(dir, folder, name, body string) {
	gomega.Expect(os.MkdirAll(filepath.Join(dir, folder), 0o750)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(dir, folder, name+".md"), []byte(body), 0o600)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("NativeFlaggedRowsSource", func() {
	var dir string

	ginkgo.BeforeEach(func() {
		dir = ginkgo.GinkgoT().TempDir()
	})

	source := func(registry SessionRegistry) NativeFlaggedRowsSource {
		return NativeFlaggedRowsSource{
			Vaults:     nativeVaults{vaults: []Vault{{Name: "private-personal", Path: dir}}},
			Supervisor: nativeSupervisor{},
			Registry:   registry,
		}
	}

	ginkgo.It("reads an operator-flagged task as a row that would CREATE", func() {
		writePage(dir, "25 Tasks", "Do It", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\nrole: agent\n---\n")
		rows, err := source(&fakeFleetRegistry{}).Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Name).To(gomega.Equal("Do It"))
		gomega.Expect(rows[0].Kind).To(gomega.Equal("task"))
		gomega.Expect(rows[0].Vault).To(gomega.Equal("private-personal"))
		gomega.Expect(rows[0].Branch).To(gomega.Equal("create"))
		gomega.Expect(rows[0].Refused).To(gomega.BeEmpty())
	})

	ginkgo.It("carries the provenance refusal for an agent-set flag", func() {
		// The row is still a row — it is refused, not dropped. Dropping it would
		// hide the write this rule exists to surface.
		writePage(dir, "25 Tasks", "Do It", "---\nstatus: in_progress\nflag: true\nflag_set_by: agent\n---\n")
		rows, err := source(&fakeFleetRegistry{}).Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Refused).To(gomega.Equal([]string{RefusalAgentSetFlag}))
	})

	ginkgo.It("skips an unflagged page entirely", func() {
		writePage(dir, "25 Tasks", "Do It", "---\nstatus: in_progress\n---\n")
		rows, err := source(&fakeFleetRegistry{}).Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.BeEmpty())
	})

	ginkgo.It("reads goals and topics from their own folders", func() {
		writePage(dir, "24 Goals", "Ship It", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\n---\n")
		writePage(dir, "23 Topics", "Fleet Ops", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\n---\n")
		rows, err := source(&fakeFleetRegistry{}).Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		kinds := map[string]bool{}
		for _, row := range rows {
			kinds[row.Kind] = true
		}
		gomega.Expect(kinds).To(gomega.HaveKey("goal"))
		gomega.Expect(kinds).To(gomega.HaveKey("topic"))
	})

	ginkgo.It("reads a row whose session the registry lists as LIVE", func() {
		writePage(dir, "25 Tasks", "Do It", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\nclaude_session_id: s1\n---\n")
		rows, err := source(&fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 42, Name: "Do It"}}}).Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Liveness).To(gomega.Equal("LIVE"))
		gomega.Expect(rows[0].Branch).To(gomega.Equal("skip"))
	})

	ginkgo.It("reads an unreadable registry as UNKNOWN, never ABSENT", func() {
		// ABSENT is a positive claim that nothing is running; a failed probe is not.
		writePage(dir, "25 Tasks", "Do It", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\nclaude_session_id: s1\n---\n")
		rows, err := source(&fakeFleetRegistry{err: os.ErrPermission}).Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Liveness).To(gomega.Equal("UNKNOWN"))
		gomega.Expect(rows[0].Branch).To(gomega.Equal("unknown"))
	})

	ginkgo.It("fails rather than reporting no rows when the vaults cannot be listed", func() {
		s := NativeFlaggedRowsSource{
			Vaults:     nativeVaults{err: os.ErrPermission},
			Supervisor: nativeSupervisor{},
			Registry:   &fakeFleetRegistry{},
		}
		rows, err := s.Rows(context.Background())
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.BeNil())
	})

	ginkgo.It("resolves a goal's owner through its declaring topic", func() {
		// The topic page declares the goal under ## Goals, so an unmanaged goal is
		// lifted to the topic rather than falling to the fleet.
		writePage(dir, "23 Topics", "Fleet Ops", "---\nstatus: in_progress\n---\n\n## Goals\n\n- [[Ship It]]\n")
		writePage(dir, "24 Goals", "Ship It", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\n---\n")
		rows, err := source(&fakeFleetRegistry{}).Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Branch).To(gomega.Equal("open-manager"))
	})
})

// nativeHeartbeats is a fixed heartbeat store.
type nativeHeartbeats struct {
	rows []HeartbeatRow
}

func (n nativeHeartbeats) Rows(context.Context) ([]HeartbeatRow, error) { return n.rows, nil }

// SC2 — the row's liveness follows the VERDICT SOURCE, never a self-derivation.
//
// The criterion asks for a stub of `/api/1.0/liveness`; the in-Go source makes no
// HTTP call, it calls `LivenessVerdicts` in-process — the same function the
// endpoint serves. So the stub is applied at that function's INPUTS (registry,
// child table, heartbeat store), which is the same falsifier: a consumer that
// derived liveness itself from `os.kill(pid, 0)` cannot pass either spec.
var _ = ginkgo.Describe("SC2 — follow the verdict", func() {
	var dir string

	ginkgo.BeforeEach(func() {
		dir = ginkgo.GinkgoT().TempDir()
	})

	ginkgo.It("flips with the verdict for a session whose heartbeat is stale", func() {
		writePage(dir, "25 Tasks", "Do It", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\nclaude_session_id: s1\n---\n")
		stale := []HeartbeatRow{{SessionID: "s1", Live: false}}

		live := NativeFlaggedRowsSource{
			Vaults:     nativeVaults{vaults: []Vault{{Name: "v", Path: dir}}},
			Supervisor: nativeSupervisor{},
			Heartbeats: nativeHeartbeats{rows: stale},
			Registry:   &fakeFleetRegistry{entries: []RegistryEntry{{SessionID: "s1", PID: 42, Name: "Do It"}}},
		}
		rows, err := live.Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		// The registry proves the process alive; the heartbeat is corroboration only
		// and must not drag the verdict down.
		gomega.Expect(rows[0].Liveness).To(gomega.Equal("LIVE"))

		exited := 0
		dead := NativeFlaggedRowsSource{
			Vaults:     nativeVaults{vaults: []Vault{{Name: "v", Path: dir}}},
			Supervisor: nativeSupervisor{children: []Session{{ID: "s1", ExitCode: &exited}}},
			Heartbeats: nativeHeartbeats{rows: stale},
			Registry:   &fakeFleetRegistry{},
		}
		rows, err = dead.Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Liveness).To(gomega.Equal("ABSENT"))
	})

	ginkgo.It("reads a session live whose verdict CONTRADICTS its pid", func() {
		// The child row has no exit code, so the verdict is live — while the registry
		// lists no pid at all for it. A consumer that self-derived from the pid would
		// report ABSENT here, which is the whole point of the second session.
		writePage(dir, "25 Tasks", "Do It", "---\nstatus: in_progress\nflag: true\nflag_set_by: operator\nclaude_session_id: s2\n---\n")
		source := NativeFlaggedRowsSource{
			Vaults:     nativeVaults{vaults: []Vault{{Name: "v", Path: dir}}},
			Supervisor: nativeSupervisor{children: []Session{{ID: "s2"}}},
			Registry:   &fakeFleetRegistry{},
		}
		rows, err := source.Rows(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rows).To(gomega.HaveLen(1))
		gomega.Expect(rows[0].Liveness).To(gomega.Equal("LIVE"))
	})
})
