// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/a2a"
)

// writeConfig writes a supervisor config file into a temp home.
func writeConfig(home, body string) {
	dir := filepath.Join(home, ".config", "claude-supervisor")
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600)).To(Succeed())
}

var _ = Describe("LoadConfig + ResolveController", func() {
	ctx := context.Background()
	var home string

	BeforeEach(func() {
		var err error
		home, err = os.MkdirTemp("", "a2a-config")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(home)
	})

	It("resolves a per-target URL from the file and a token from the environment", func() {
		writeConfig(home, `{"a2a":{"url":{"docker":"http://localhost:8090/a2a"}}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		cfg.EnvTokens["docker"] = "token-from-env"

		resolved, err := a2a.ResolveController(ctx, a2a.Target{Kind: a2a.KindDocker}, cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.URL).To(Equal("http://localhost:8090/a2a"))
		Expect(resolved.Token).To(Equal("token-from-env"))
	})

	It("resolves a bare k8s to the default cluster and records k8s:<cluster>", func() {
		writeConfig(home, `{"a2a":{"url":{"nukedev":"https://dev.example/a2a"}}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		cfg.EnvTokens["nukedev"] = "t"

		resolved, err := a2a.ResolveController(ctx, a2a.Target{Kind: a2a.KindK8s}, cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Target.String()).To(Equal("k8s:nukedev"))
	})

	It("falls back to the legacy k8s URL key for the default cluster only", func() {
		writeConfig(home, `{"a2a":{"url":{"k8s":"https://legacy.example/a2a"}}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		cfg.EnvTokens["nukedev"] = "t"

		resolved, err := a2a.ResolveController(ctx, a2a.Target{Kind: a2a.KindK8s}, cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.URL).To(Equal("https://legacy.example/a2a"))
	})

	It("refuses a bare-string a2a.url", func() {
		writeConfig(home, `{"a2a":{"url":"http://localhost:8090/a2a"}}`)
		_, err := a2a.LoadConfig(ctx, home)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("one controller URL cannot serve more than one target"))
	})

	It("refuses a plaintext token in the file", func() {
		writeConfig(home, `{"a2a":{"token":"secret"}}`)
		_, err := a2a.LoadConfig(ctx, home)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`"a2a.token"`))
	})

	It("refuses a target with no configured URL and names the configured clusters", func() {
		writeConfig(home, `{"a2a":{"url":{"nukedev":"https://dev.example/a2a"}}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())

		_, err = a2a.ResolveController(ctx, a2a.Target{Kind: a2a.KindK8s, Cluster: "sun"}, cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("nukedev"))
	})

	It("refuses a target with no token", func() {
		writeConfig(home, `{"a2a":{"url":{"docker":"http://localhost:8090/a2a"}}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		// The developer's own environment may carry a token; this spec is about
		// the absence of one.
		cfg.EnvTokens = map[string]string{}

		_, err = a2a.ResolveController(ctx, a2a.Target{Kind: a2a.KindDocker}, cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no controller token is configured"))
	})
})

var _ = Describe("ClusterAllowlistRefusal", func() {
	ctx := context.Background()

	It("allows a cluster the vault declares", func() {
		cfg := a2a.Config{VaultClusters: map[string][]string{"private-personal": {"nukedev"}}}
		Expect(a2a.ClusterAllowlistRefusal(ctx, "private-personal",
			a2a.Target{Kind: a2a.KindK8s, Cluster: "nukedev"}, cfg)).To(Succeed())
	})

	It("refuses a cluster the vault does not declare", func() {
		cfg := a2a.Config{VaultClusters: map[string][]string{"private-personal": {"nukedev"}}}
		err := a2a.ClusterAllowlistRefusal(ctx, "private-personal",
			a2a.Target{Kind: a2a.KindK8s, Cluster: "octopus-dev"}, cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("data-boundary breach"))
	})

	It("refuses a vault with no allowlist entry rather than allowing it", func() {
		err := a2a.ClusterAllowlistRefusal(ctx, "unknown-vault",
			a2a.Target{Kind: a2a.KindK8s, Cluster: "nukedev"}, a2a.Config{})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("declares no cluster allowlist"))
	})

	It("allows docker regardless of the allowlist", func() {
		Expect(a2a.ClusterAllowlistRefusal(ctx, "unknown-vault",
			a2a.Target{Kind: a2a.KindDocker}, a2a.Config{})).To(Succeed())
	})
})

// writeVaults writes a vault allowlist file into a temp home.
func writeVaults(home, body string) {
	dir := filepath.Join(home, ".config", "claude-supervisor")
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "vaults.json"), []byte(body), 0o600)).To(Succeed())
}

var _ = Describe("LoadConfig config path", func() {
	ctx := context.Background()

	It("reads SUPERVISOR_CONFIG, the same file the roster's spawn caps read", func() {
		path := filepath.Join(GinkgoT().TempDir(), "config.json")
		Expect(os.WriteFile(path, []byte(`{"a2a":{"url":{"docker":"http://scratch/a2a"}}}`), 0o600)).To(Succeed())
		GinkgoT().Setenv("SUPERVISOR_CONFIG", path)

		cfg, err := a2a.LoadConfig(ctx, "/nonexistent-home")
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ConfigPath).To(Equal(path))
		Expect(cfg.URLs["docker"]).To(Equal("http://scratch/a2a"))
	})
})

var _ = Describe("LoadConfig vault allowlist", func() {
	ctx := context.Background()
	var home string

	BeforeEach(func() {
		var err error
		home, err = os.MkdirTemp("", "a2a-vaults")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(home)
	})

	It("reads the flat array shape the live file uses", func() {
		writeVaults(home, `{"vaults":{"private-personal":["nukedev","nukeprod","sun"]}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.VaultClusters["private-personal"]).
			To(Equal([]string{"nukedev", "nukeprod", "sun"}))
		Expect(a2a.ClusterAllowlistRefusal(ctx, "private-personal",
			a2a.Target{Kind: a2a.KindK8s, Cluster: "nukedev"}, cfg)).To(Succeed())
	})

	It("still reads the nested clusters shape", func() {
		writeVaults(home, `{"vaults":{"private-personal":{"clusters":["nukedev"]}}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.VaultClusters["private-personal"]).To(Equal([]string{"nukedev"}))
	})

	It("keeps an empty array as a declared vault with no clusters", func() {
		writeVaults(home, `{"vaults":{"assistant-brogrammers":[]}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.VaultClusters).To(HaveKey("assistant-brogrammers"))
		Expect(a2a.ClusterAllowlistRefusal(ctx, "assistant-brogrammers",
			a2a.Target{Kind: a2a.KindK8s, Cluster: "nukedev"}, cfg)).NotTo(Succeed())
	})

	It("fails closed on an entry in neither shape", func() {
		writeVaults(home, `{"vaults":{"private-personal":"nukedev"}}`)
		cfg, err := a2a.LoadConfig(ctx, home)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.VaultClusters).To(BeEmpty())
	})
})
