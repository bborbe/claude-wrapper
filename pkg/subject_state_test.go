// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("SubjectStateReader", func() {
	var workerDir, stateDir string
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

	ginkgo.BeforeEach(func() {
		root := ginkgo.GinkgoT().TempDir()
		workerDir = filepath.Join(root, "worker-manager")
		stateDir = filepath.Join(root, "sweep-gate")
		gomega.Expect(os.MkdirAll(workerDir, 0o755)).To(gomega.Succeed())
		gomega.Expect(os.MkdirAll(stateDir, 0o755)).To(gomega.Succeed())
	})

	writeRecord := func(sessionID, body string) {
		gomega.Expect(os.WriteFile(
			filepath.Join(workerDir, sessionID+".json"), []byte(body), 0o644,
		)).To(gomega.Succeed())
	}

	ginkgo.It("reports SLEEPING when a future sleep marker names the subject", func() {
		writeRecord("s1", `{"subject":"Foo","branch":"goal"}`)
		gomega.Expect(os.WriteFile(
			filepath.Join(stateDir, "goal--foo.sleeping"),
			[]byte(now.Add(time.Hour).Format(time.RFC3339)), 0o644,
		)).To(gomega.Succeed())
		reader := NewSubjectStateReader(workerDir, stateDir, fixedNow(now))
		state, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeTrue())
		gomega.Expect(state).To(gomega.Equal(SubjectStateSleeping))
	})

	ginkgo.It("reports awake for a past sleep marker", func() {
		writeRecord("s1", `{"subject":"Foo","branch":"goal"}`)
		gomega.Expect(os.WriteFile(
			filepath.Join(stateDir, "goal--foo.sleeping"),
			[]byte(now.Add(-time.Hour).Format(time.RFC3339)), 0o644,
		)).To(gomega.Succeed())
		reader := NewSubjectStateReader(workerDir, stateDir, fixedNow(now))
		state, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeTrue())
		gomega.Expect(state).To(gomega.BeEmpty())
	})

	ginkgo.It("resolves no subject when the session has no record", func() {
		reader := NewSubjectStateReader(workerDir, stateDir, fixedNow(now))
		_, resolved, err := reader.State(context.Background(), "s-none")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeFalse())
	})

	ginkgo.It("resolves no subject when the record carries no subject or branch", func() {
		writeRecord("s1", `{"subject":"","branch":""}`)
		reader := NewSubjectStateReader(workerDir, stateDir, fixedNow(now))
		_, resolved, err := reader.State(context.Background(), "s1")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resolved).To(gomega.BeFalse())
	})
})
