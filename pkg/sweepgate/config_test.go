// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// setEnv sets an environment variable and restores it after the spec.
func setEnv(key, value string) {
	previous, had := os.LookupEnv(key)
	gomega.Expect(os.Setenv(key, value)).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() {
		if had {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	})
}

// clearSweepEnv empties every SWEEP_GATE_* variable the defaults read.
func clearSweepEnv() {
	for _, key := range []string{
		"SWEEP_GATE_STATE_DIR", "SWEEP_GATE_SETTLED_DIR", "SWEEP_GATE_FEED_DIR",
		"SWEEP_GATE_HOLDS", "SWEEP_GATE_PREDISPATCH_DIR", "SWEEP_GATE_WORKER_MANAGER_DIR",
		"SWEEP_GATE_STORE_URL", "ATTENTION_STORE_URL",
	} {
		setEnv(key, "")
	}
}

var _ = ginkgo.Describe("fills configuration defaults", func() {
	ctx := context.Background()

	ginkgo.It("requires a vault and a subject", func() {
		err := Config{}.Validate(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("Vault"))

		err = Config{Vault: "/vault"}.Validate(ctx)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("Subject"))

		gomega.Expect((Config{Vault: "/vault", Subject: "S"}).Validate(ctx)).
			To(gomega.Succeed())
	})

	ginkgo.It("prefers an explicit field over the environment", func() {
		clearSweepEnv()
		setEnv("SWEEP_GATE_STATE_DIR", "/from-env")
		cfg, err := Config{Vault: "/v", Subject: "s", StateDir: "/explicit"}.WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.StateDir).To(gomega.Equal("/explicit"))
	})

	ginkgo.It("uses the environment when the field is empty", func() {
		clearSweepEnv()
		setEnv("SWEEP_GATE_STATE_DIR", "/from-env")
		cfg, err := Config{Vault: "/v", Subject: "s"}.WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.StateDir).To(gomega.Equal("/from-env"))
	})

	ginkgo.It("uses the home-relative defaults", func() {
		clearSweepEnv()
		home := ginkgo.GinkgoT().TempDir()
		setEnv("HOME", home)
		cfg, err := Config{Vault: "/v", Subject: "s"}.WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.StateDir).
			To(gomega.Equal(filepath.Join(home, ".claude", "state", "sweep-gate")))
		gomega.Expect(cfg.SettledDir).
			To(gomega.Equal(filepath.Join(home, ".claude", "state", "sweep-gate-settled")))
		gomega.Expect(cfg.FeedDir).
			To(gomega.Equal(filepath.Join(home, ".claude", "state", "attention")))
		gomega.Expect(cfg.HoldsPath).
			To(gomega.Equal(filepath.Join(home, ".claude", "state", "session-holds.json")))
		gomega.Expect(cfg.PredispatchDir).
			To(gomega.Equal(filepath.Join(home, ".claude", "state", "manager-predispatch")))
		gomega.Expect(cfg.WorkerManagerDir).
			To(gomega.Equal(filepath.Join(home, ".claude", "state", "worker-manager")))
		gomega.Expect(cfg.RegistryDir).
			To(gomega.Equal(filepath.Join(home, ".claude", "sessions")))
		gomega.Expect(cfg.StoreURL).To(gomega.Equal("http://localhost:18080"))
	})

	ginkgo.It("resolves the store URL precedence", func() {
		clearSweepEnv()
		setEnv("ATTENTION_STORE_URL", "http://attention")
		setEnv("SWEEP_GATE_STORE_URL", "http://sweep")
		cfg, err := Config{Vault: "/v", Subject: "s"}.WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.StoreURL).To(gomega.Equal("http://sweep"))

		setEnv("SWEEP_GATE_STORE_URL", "")
		cfg, err = Config{Vault: "/v", Subject: "s"}.WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.StoreURL).To(gomega.Equal("http://attention"))

		cfg, err = Config{Vault: "/v", Subject: "s", StoreURL: "http://explicit"}.
			WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.StoreURL).To(gomega.Equal("http://explicit"))
	})

	ginkgo.It("expands a leading tilde", func() {
		clearSweepEnv()
		home := ginkgo.GinkgoT().TempDir()
		setEnv("HOME", home)
		cfg, err := Config{Vault: "/v", Subject: "s", StateDir: "~/state"}.
			WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.StateDir).To(gomega.Equal(filepath.Join(home, "state")))
	})

	ginkgo.It("fills the clock, interval, timeout and logger", func() {
		clearSweepEnv()
		cfg, err := Config{Vault: "/v", Subject: "s"}.WithDefaults(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(cfg.Now).NotTo(gomega.BeNil())
		gomega.Expect(cfg.Interval).To(gomega.Equal(DefaultInterval))
		gomega.Expect(cfg.ReadTimeout).To(gomega.Equal(DefaultReadTimeout))
		gomega.Expect(cfg.Logf).NotTo(gomega.BeNil())
	})
})

var _ = ginkgo.Describe("exit codes", func() {
	ginkgo.It("pins the four exit codes", func() {
		gomega.Expect(ExitNoChange).To(gomega.Equal(0))
		gomega.Expect(ExitChange).To(gomega.Equal(10))
		gomega.Expect(ExitUsage).To(gomega.Equal(2))
		gomega.Expect(ExitFailed).To(gomega.Equal(ExitUsage))
	})
})
