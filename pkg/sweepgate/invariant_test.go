// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const invariantSubject = "Blocked Subject"

// newInvariantFixture builds a vault with one goal subject and two operator rows.
func newInvariantFixture() Config {
	dir := ginkgo.GinkgoT().TempDir()
	vault := filepath.Join(dir, "vault")
	mustWrite(filepath.Join(vault, "24 Goals", invariantSubject+".md"),
		"---\npage_type: goal\n---\n\n# "+invariantSubject+"\n")
	writeInvariantTask(vault, "Row One", "in_progress", "human_review")
	writeInvariantTask(vault, "Row Two", "in_progress", "human_review")
	return Config{
		Vault:            vault,
		Subject:          invariantSubject,
		StateDir:         filepath.Join(dir, "state"),
		SettledDir:       filepath.Join(dir, "settled"),
		FeedDir:          filepath.Join(dir, "feed"),
		HoldsPath:        filepath.Join(dir, "holds.json"),
		PredispatchDir:   filepath.Join(dir, "predispatch"),
		WorkerManagerDir: filepath.Join(dir, "worker-manager"),
		RegistryDir:      filepath.Join(dir, "sessions"),
		HeartbeatDir:     filepath.Join(dir, "heartbeat"),
		ToolMarkerDir:    filepath.Join(dir, "tools"),
		TranscriptDir:    filepath.Join(dir, "projects"),
		StoreURL:         "http://127.0.0.1:1",
		Now:              fixedClock(),
		ReadTimeout:      DefaultReadTimeout,
	}
}

// writeInvariantTask writes one task page into the fixture vault.
func writeInvariantTask(vault, name, status, phase string) {
	body := fmt.Sprintf(
		"---\nstatus: %s\nphase: %s\ngoals: ['[[%s]]']\n---\n\n# %s\n",
		status, phase, invariantSubject, name,
	)
	mustWrite(filepath.Join(vault, "25 Tasks", name+".md"), body)
}

// writeManagerRegistration records a producer session as a subject's manager.
func writeManagerRegistration(cfg Config, sessionID, subject string) {
	mustWrite(
		filepath.Join(cfg.WorkerManagerDir, sessionID+".json"),
		`{"subject":`+fmt.Sprintf("%q", subject)+`}`,
	)
}

// writeEventCard writes one open gate into the feed's event log.
func writeEventCard(cfg Config, sessionID, itemID string) {
	body := fmt.Sprintf(
		`{"type":"open","item_id":%q,"session_id":%q,"kind":"permission",`+
			`"detail":"Allow?","ts":1}`+"\n",
		itemID, sessionID,
	)
	mustWrite(filepath.Join(cfg.FeedDir, sessionID+".events.jsonl"), body)
}

// mustWrite writes a fixture file, creating its directory.
func mustWrite(path, body string) {
	gomega.Expect(os.MkdirAll(filepath.Dir(path), 0o750)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(path, []byte(body), 0o600)).To(gomega.Succeed())
}

// mustPrepare runs the pipeline and fails the spec on error.
func mustPrepare(ctx context.Context, cfg Config) Prepared {
	prepared, err := Prepare(ctx, cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return prepared
}

// assertInvariantPayload parses the payload and checks its five keys and options.
func assertInvariantPayload(payload []byte) {
	var card map[string]any
	gomega.Expect(json.Unmarshal(payload, &card)).To(gomega.Succeed())
	for _, key := range []string{"dedup_key", "payload", "context", "options", "recommend"} {
		gomega.Expect(card).To(gomega.HaveKey(key))
	}
	gomega.Expect(card["options"]).To(gomega.HaveLen(2))
}

// reportCase prints the per-case line the spec's verification greps.
func reportCase(name string, stdout []byte, exit int) {
	ginkgo.GinkgoWriter.Printf(
		"case=%s stdout_bytes=%d exit=%d\n", name, len(stdout), exit,
	)
}

var _ = ginkgo.Describe("prints the payload only when the card fires", func() {
	ctx := context.Background()

	run := func(name string, cfg Config) ([]byte, int, string) {
		var stdout, stderr bytes.Buffer
		exit := Invariant(ctx, cfg, &stdout, &stderr)
		reportCase(name, stdout.Bytes(), exit)
		return stdout.Bytes(), exit, stderr.String()
	}

	ginkgo.It("fires for a fully blocked, owned, card-free subject", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		stdout, exit, stderr := run("fires", cfg)
		gomega.Expect(exit).To(gomega.Equal(0))
		gomega.Expect(stdout).NotTo(gomega.BeEmpty())
		gomega.Expect(stderr).To(gomega.ContainSubstring("park: "))
		assertInvariantPayload(stdout)
	})

	ginkgo.It("prints nothing for an unreadable subject", func() {
		cfg := newInvariantFixture()
		cfg.Subject = "Missing Subject"
		stdout, exit, _ := run("unreadable", cfg)
		gomega.Expect(exit).To(gomega.Equal(0))
		gomega.Expect(stdout).To(gomega.BeEmpty())
	})

	ginkgo.It("fires when the board holds a card for another subject", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		writeEventCard(cfg, "other-sess", "card-1")
		stdout, exit, _ := run("owned-by-another", cfg)
		gomega.Expect(exit).To(gomega.Equal(0))
		gomega.Expect(stdout).NotTo(gomega.BeEmpty())
		assertInvariantPayload(stdout)
	})

	ginkgo.It("prints nothing while the subject already has a card", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		writeEventCard(cfg, "manager-sess", "card-1")
		stdout, exit, _ := run("already-open", cfg)
		gomega.Expect(exit).To(gomega.Equal(0))
		gomega.Expect(stdout).To(gomega.BeEmpty())
	})

	ginkgo.It("prints nothing when no manager registration names the subject", func() {
		cfg := newInvariantFixture()
		stdout, exit, stderr := run("no-owner", cfg)
		gomega.Expect(exit).To(gomega.Equal(0))
		gomega.Expect(stdout).To(gomega.BeEmpty())
		gomega.Expect(stderr).To(gomega.ContainSubstring("skip " + invariantSubject))
	})

	ginkgo.It("prints nothing when the settled row set is unchanged", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		prepared := mustPrepare(ctx, cfg)
		settledPath, err := SettledPath(ctx, cfg, invariantSubject)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(SaveSettled(ctx, settledPath, Settled{
			Subject:    invariantSubject,
			RowKey:     RowSetKey(ctx, cfg, prepared, invariantSubject),
			RecordedAt: fixedNow.Format(recordedAtFormat),
		})).To(gomega.Succeed())
		stdout, exit, stderr := run("settled", cfg)
		gomega.Expect(exit).To(gomega.Equal(0))
		gomega.Expect(stdout).To(gomega.BeEmpty())
		gomega.Expect(stderr).To(gomega.ContainSubstring("suppress " + invariantSubject))
	})

	ginkgo.It("fires again when the row set changes", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		prepared := mustPrepare(ctx, cfg)
		settledPath, err := SettledPath(ctx, cfg, invariantSubject)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(SaveSettled(ctx, settledPath, Settled{
			Subject:    invariantSubject,
			RowKey:     RowSetKey(ctx, cfg, prepared, invariantSubject),
			RecordedAt: fixedNow.Format(recordedAtFormat),
		})).To(gomega.Succeed())
		writeInvariantTask(cfg.Vault, "Row Two", "in_progress", "todo")
		stdout, exit, _ := run("row-set-changed", cfg)
		gomega.Expect(exit).To(gomega.Equal(0))
		gomega.Expect(stdout).NotTo(gomega.BeEmpty())
		assertInvariantPayload(stdout)
	})
})

var _ = ginkgo.Describe("refuses the card", func() {
	ctx := context.Background()

	ginkgo.It("refuses a card to a subject with no owner", func() {
		cfg := newInvariantFixture()
		var stderr bytes.Buffer
		payload, fires, err := InvariantPayload(ctx, cfg, mustPrepare(ctx, cfg), &stderr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fires).To(gomega.BeFalse())
		gomega.Expect(payload).To(gomega.BeNil())
		gomega.Expect(stderr.String()).To(gomega.ContainSubstring("resolves no owner"))
	})

	ginkgo.It("refuses a card while the subject already has one", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		writeEventCard(cfg, "manager-sess", "card-1")
		payload, fires, err := InvariantPayload(ctx, cfg, mustPrepare(ctx, cfg),
			&bytes.Buffer{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fires).To(gomega.BeFalse())
		gomega.Expect(payload).To(gomega.BeNil())
	})

	ginkgo.It("refuses a re-post of a settled ask", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		prepared := mustPrepare(ctx, cfg)
		settledPath, err := SettledPath(ctx, cfg, invariantSubject)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(SaveSettled(ctx, settledPath, Settled{
			Subject: invariantSubject,
			RowKey:  RowSetKey(ctx, cfg, prepared, invariantSubject),
		})).To(gomega.Succeed())
		var stderr bytes.Buffer
		payload, fires, err := InvariantPayload(ctx, cfg, prepared, &stderr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fires).To(gomega.BeFalse())
		gomega.Expect(payload).To(gomega.BeNil())
		gomega.Expect(stderr.String()).To(gomega.ContainSubstring("already answered"))
	})

	ginkgo.It("refuses a card to a subject that is not operator-blocked", func() {
		cfg := newInvariantFixture()
		writeManagerRegistration(cfg, "manager-sess", invariantSubject)
		writeInvariantTask(cfg.Vault, "Row One", "in_progress", "execution")
		writeInvariantTask(cfg.Vault, "Row Two", "in_progress", "execution")
		payload, fires, err := InvariantPayload(ctx, cfg, mustPrepare(ctx, cfg),
			&bytes.Buffer{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fires).To(gomega.BeFalse())
		gomega.Expect(payload).To(gomega.BeNil())
	})

	ginkgo.It("degrades an unreadable worker-manager record to no subject", func() {
		dir := ginkgo.GinkgoT().TempDir()
		mustWrite(filepath.Join(dir, "s1.json"), "{half")
		subject, ok := ManagerSubject(ctx, Config{WorkerManagerDir: dir}, "s1")
		gomega.Expect(ok).To(gomega.BeFalse())
		gomega.Expect(subject).To(gomega.BeEmpty())
	})

	ginkgo.It("degrades an unreadable verdict cache to no verdicts", func() {
		dir := ginkgo.GinkgoT().TempDir()
		mustWrite(filepath.Join(dir, "blocked-subject.verdicts.json"), "{half")
		verdicts, err := CachedVerdicts(ctx, Config{PredispatchDir: dir}, invariantSubject)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(verdicts).To(gomega.BeEmpty())
	})
})
