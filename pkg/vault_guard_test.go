// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("vault family guard", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.Describe("vaultFamily", func() {
		ginkgo.It("reduces a vault name to its family prefix", func() {
			gomega.Expect(vaultFamily("private-personal")).To(gomega.Equal("private"))
			gomega.Expect(vaultFamily("private-trading")).To(gomega.Equal("private"))
			gomega.Expect(vaultFamily("seibert-brogrammers")).To(gomega.Equal("seibert"))
			gomega.Expect(vaultFamily("seibert-personal")).To(gomega.Equal("seibert"))
			gomega.Expect(vaultFamily("assistant-brogrammers")).To(gomega.Equal("assistant"))
			gomega.Expect(vaultFamily("plain")).To(gomega.Equal("plain"))
			gomega.Expect(vaultFamily("")).To(gomega.Equal(""))
		})
	})

	ginkgo.Describe("ResolveDaemonVault", func() {
		ginkgo.It("returns a disabled vault when neither flag names one", func() {
			resolved := ResolveDaemonVault(ctx, &fakeVaults{}, "", "")
			gomega.Expect(resolved.Enabled()).To(gomega.BeFalse())
		})

		ginkgo.It("resolves an explicit name against the configured list", func() {
			vaults := &fakeVaults{vaults: []Vault{
				{Name: "private-personal", Path: "/vaults/private-personal"},
			}}
			resolved := ResolveDaemonVault(ctx, vaults, "private-personal", "")
			gomega.Expect(resolved).To(gomega.Equal(DaemonVault{
				Name: "private-personal", Path: "/vaults/private-personal",
			}))
			gomega.Expect(resolved.Family()).To(gomega.Equal("private"))
		})

		ginkgo.It("falls back to the sweep-gate path when no name is given", func() {
			vaults := &fakeVaults{vaults: []Vault{
				{Name: "seibert-brogrammers", Path: "/vaults/seibert-brogrammers"},
			}}
			resolved := ResolveDaemonVault(ctx, vaults, "", "/vaults/seibert-brogrammers")
			gomega.Expect(resolved.Name).To(gomega.Equal("seibert-brogrammers"))
			gomega.Expect(resolved.Family()).To(gomega.Equal("seibert"))
		})

		ginkgo.It("derives the name from the path basename when the list is unreadable", func() {
			vaults := &fakeVaults{err: stderrors.New("vault-cli failed")}
			resolved := ResolveDaemonVault(ctx, vaults, "", "/vaults/seibert-brogrammers")
			gomega.Expect(resolved.Name).To(gomega.Equal("seibert-brogrammers"))
			gomega.Expect(resolved.Family()).To(gomega.Equal("seibert"))
		})
	})

	ginkgo.Describe("RefuseForeignVault", func() {
		own := DaemonVault{Name: "seibert-brogrammers", Path: "/vaults/seibert-brogrammers"}

		ginkgo.It("is a no-op when the daemon named no vault", func() {
			gomega.Expect(RefuseForeignVault(ctx, DaemonVault{}, "private-personal", "")).To(gomega.Succeed())
		})

		ginkgo.It("refuses a row from another family and names both vaults", func() {
			err := RefuseForeignVault(ctx, own, "private-personal", "")
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(stderrors.Is(err, ErrForeignVault)).To(gomega.BeTrue())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("private-personal"))
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("seibert-brogrammers"))
		})

		ginkgo.It("allows a row from the same family", func() {
			gomega.Expect(RefuseForeignVault(ctx, own, "seibert-personal", "")).To(gomega.Succeed())
		})

		ginkgo.It("allows the daemon's own vault", func() {
			gomega.Expect(RefuseForeignVault(ctx, own, "seibert-brogrammers", "")).To(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("OpenExecutor vault-family guard", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	// foreignFixture wires an executor serving v1 over a foreign v2 row.
	foreignFixture := func() (*openExecutorFixture, string, OpenExecutor) {
		f := newOpenExecutorFixture()
		foreignDir := ginkgo.GinkgoT().TempDir()
		f.vaults.vaults = append(f.vaults.vaults, Vault{
			Name: "v2", Path: foreignDir, ClaudeScript: "/scripts/cc-v2",
		})
		deps := f.deps()
		deps.OwnVault = DaemonVault{Name: "v1", Path: f.dir}
		return f, foreignDir, NewOpenExecutor(deps)
	}

	ginkgo.It("refuses a foreign-vault row on the request path and opens nothing", func() {
		f, foreignDir, executor := foreignFixture()
		writeOpenPage(foreignDir, openTaskTestFolder, "F", "role: agent")

		verdict := executor.OpenRequest(
			ctx, []OpenRow{{Kind: OpenKindTask, Vault: "v2", Name: "F"}}, "caller-1",
		)[0]

		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonVaultMismatch))
		gomega.Expect(verdict.Message).To(gomega.ContainSubstring("v2"))
		gomega.Expect(verdict.Message).To(gomega.ContainSubstring("v1"))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
		gomega.Expect(f.tabOpener.requests).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses a foreign-vault row on the flagged tick path", func() {
		f, foreignDir, executor := foreignFixture()
		writeOpenPage(foreignDir, openTaskTestFolder, "F", "role: agent")

		verdict := executor.OpenTick(
			ctx, []OpenRow{{Kind: OpenKindTask, Vault: "v2", Name: "F"}},
		)[0]

		gomega.Expect(verdict.Branch).To(gomega.Equal(OpenBranchRefused))
		gomega.Expect(verdict.Reason).To(gomega.Equal(OpenReasonVaultMismatch))
		gomega.Expect(f.headless.requests).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("RosterToolServer vault-family guard on the spawn path", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("refuses a headless spawn into a foreign vault family, naming both", func() {
		ownDir := ginkgo.GinkgoT().TempDir()
		foreignDir := ginkgo.GinkgoT().TempDir()
		vaults := &fakeVaults{vaults: []Vault{
			{Name: "private-personal", Path: ownDir, ClaudeScript: "/scripts/cc-private"},
			{Name: "seibert-brogrammers", Path: foreignDir, ClaudeScript: "/scripts/cc-seibert"},
		}}
		supervisor := &headlessSupervisor{}
		server := &RosterToolServer{
			Supervisor: supervisor,
			Vaults:     vaults,
			OwnVault:   DaemonVault{Name: "private-personal", Path: ownDir},
		}

		_, _, err := server.SpawnOpenHeadless(ctx, OpenHeadlessRequest{
			Vault: "seibert-brogrammers", Task: "T",
		})

		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(stderrors.Is(err, ErrForeignVault)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("seibert-brogrammers"))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("private-personal"))
		gomega.Expect(supervisor.spawned).To(gomega.BeEmpty())
	})

	ginkgo.It("allows a headless spawn into a vault of the same family", func() {
		ownDir := ginkgo.GinkgoT().TempDir()
		siblingDir := ginkgo.GinkgoT().TempDir()
		vaults := &fakeVaults{vaults: []Vault{
			{Name: "seibert-brogrammers", Path: ownDir, ClaudeScript: "/scripts/cc-seibert"},
			{Name: "seibert-personal", Path: siblingDir, ClaudeScript: "/scripts/cc-seibert-personal"},
		}}
		supervisor := &headlessSupervisor{}
		server := &RosterToolServer{
			Supervisor: supervisor,
			Vaults:     vaults,
			OwnVault:   DaemonVault{Name: "seibert-brogrammers", Path: ownDir},
		}

		_, _, err := server.SpawnOpenHeadless(ctx, OpenHeadlessRequest{
			Vault: "seibert-personal", Task: "T",
		})

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(supervisor.spawned).To(gomega.HaveLen(1))
	})
})
