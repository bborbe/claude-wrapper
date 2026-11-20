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
