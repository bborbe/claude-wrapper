// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"bytes"
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// These tests carry the refusals that no earlier spec reached: a topic page with
// no `## Goals` section, an execution row with no mtime, and the three tick write
// failures the reference names on stderr. Each name is cited by docs/sweep-gate.md.

var _ = ginkgo.Describe("refuses a topic page with no Goals section", func() {
	ginkgo.It("refuses a topic page with no Goals section", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "Empty.md")
		gomega.Expect(os.WriteFile(path, []byte(
			"---\npage_type: topic\n---\n# Empty\n\nNo goals here.\n"), 0o600)).
			To(gomega.Succeed())
		members, err := DeclaredMembers(path)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("no `## Goals` section"))
		gomega.Expect(members).To(gomega.BeNil())
	})
})

var _ = ginkgo.Describe("refuses a row with no mtime", func() {
	ginkgo.It("refuses a row with no mtime", func() {
		gomega.Expect(IdleStuck(Task{Phase: "execution", Met: "0/2"}, fixedNow)).
			To(gomega.BeFalse())
	})
})

var _ = ginkgo.Describe("refuses action lines to silent buckets", func() {
	ginkgo.It("refuses action lines to held, deferred, approval, external and declared rows", func() {
		holds := map[string]Hold{"held-session": {
			Reason: "operator parked it",
			HeldAt: fixedNow.Add(-time.Hour).UTC().Format(heldAtFormat),
		}}
		prepared := Prepared{
			Holds: holds,
			Tasks: []Task{
				{Name: "Held", Session: "held-session"},
				{Name: "Deferred", Status: "next", DeferDate: "2099-01-01"},
				{Name: "Approval", Status: "in_progress", Phase: "todo"},
				{Name: "External", Status: "in_progress", ExternalWait: true},
				{Name: "Declared", Status: "in_progress", DeclaredWaitActive: true},
			},
		}
		lines, err := ActionLines(context.Background(), Config{Now: fixedClock()}, prepared)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lines).To(gomega.BeEmpty())
	})
})

// tickFailureConfig is the parity fixture configured for one write-failure case.
func tickFailureConfig(stateDir string) Config {
	cfg := parityRenderConfig("Parity Topic", fixtureVault())
	cfg.StateDir = stateDir
	return cfg
}

var _ = ginkgo.Describe("refuses a tick it cannot write the tick file for", func() {
	ginkgo.It("refuses a tick it cannot write the tick file for", func() {
		parent := ginkgo.GinkgoT().TempDir()
		blocked := filepath.Join(parent, "blocked")
		gomega.Expect(os.WriteFile(blocked, []byte("not a directory"), 0o600)).
			To(gomega.Succeed())

		var stdout, stderr bytes.Buffer
		code := Tick(context.Background(), tickFailureConfig(blocked),
			&stdout, &stderr, nil)

		gomega.Expect(code).To(gomega.Equal(ExitFailed))
		gomega.Expect(stderr.String()).To(gomega.ContainSubstring(
			"fail-open: could not write tick file"))
		gomega.Expect(stdout.String()).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("degrades a card write failure to a named stderr line", func() {
	ginkgo.It("degrades a card write failure to a named stderr line", func() {
		stateDir := ginkgo.GinkgoT().TempDir()
		cfg := tickFailureConfig(stateDir)
		card := func(context.Context, Prepared) ([]byte, error) {
			return nil, stderrors.New("card builder failed")
		}

		var stdout, stderr bytes.Buffer
		code := Tick(context.Background(), cfg, &stdout, &stderr, card)

		gomega.Expect(code).To(gomega.Equal(ExitChange))
		gomega.Expect(stderr.String()).To(gomega.ContainSubstring(
			"fail-open: could not write card file"))
		_, statErr := os.Stat(filepath.Join(stateDir, "parity-topic.tick.txt"))
		gomega.Expect(statErr).NotTo(gomega.HaveOccurred())
	})
})

var _ = ginkgo.Describe("refuses a tick it cannot record its state", func() {
	ginkgo.It("refuses a tick it cannot record its state", func() {
		stateDir := ginkgo.GinkgoT().TempDir()
		// A directory at the atomic writer's temp path makes the rename target's
		// OpenFile fail after the tick file has already been written.
		gomega.Expect(os.MkdirAll(
			filepath.Join(stateDir, "parity-topic.json.tmp"), 0o750)).To(gomega.Succeed())

		var stdout, stderr bytes.Buffer
		code := Tick(context.Background(), tickFailureConfig(stateDir),
			&stdout, &stderr, nil)

		gomega.Expect(code).To(gomega.Equal(ExitFailed))
		gomega.Expect(stderr.String()).To(gomega.ContainSubstring(
			"fail-open: could not record state"))
		_, statErr := os.Stat(filepath.Join(stateDir, "parity-topic.tick.txt"))
		gomega.Expect(statErr).NotTo(gomega.HaveOccurred())
	})
})
