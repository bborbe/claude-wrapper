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

var _ = ginkgo.Describe("FileSubjectReader", func() {
	var (
		ctx      context.Context
		subjects string
		state    string
		reader   *FileSubjectReader
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		dir := ginkgo.GinkgoT().TempDir()
		subjects = filepath.Join(dir, "worker-manager")
		state = filepath.Join(dir, "sweep-gate")
		gomega.Expect(os.MkdirAll(subjects, 0o755)).To(gomega.Succeed())
		gomega.Expect(os.MkdirAll(state, 0o755)).To(gomega.Succeed())
		reader = NewFileSubjectReader(subjects, state)
	})

	writeSubject := func(session, body string) {
		gomega.Expect(
			os.WriteFile(filepath.Join(subjects, session+".json"), []byte(body), 0o644),
		).To(gomega.Succeed())
	}

	ginkgo.It("returns no subject for a session that recorded none", func() {
		subject, err := reader.Subject(ctx, "absent")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(subject).To(gomega.BeEmpty())
	})

	ginkgo.It("folds the recorded subject, so Personal and personal compare equal", func() {
		writeSubject("m1", `{"subject": "  Personal   Work "}`)

		subject, err := reader.Subject(ctx, "m1")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(subject).To(gomega.Equal("personal work"))
	})

	ginkgo.It("reads a malformed record as no subject rather than failing the tick", func() {
		writeSubject("m1", `{not json`)

		subject, err := reader.Subject(ctx, "m1")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(subject).To(gomega.BeEmpty())
	})

	ginkgo.It("reports not-stopped when no marker exists on any tier", func() {
		marker, stopped, err := reader.Stopped(ctx, "personal work")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(stopped).To(gomega.BeFalse())
		gomega.Expect(marker).To(gomega.BeEmpty())
	})

	ginkgo.It("finds the marker on every tier, and names the file it found", func() {
		for _, branch := range standdownBranches {
			marker := branch + "--personal-work.stopped"
			gomega.Expect(
				os.WriteFile(filepath.Join(state, marker), []byte("x"), 0o644),
			).To(gomega.Succeed())

			found, stopped, err := reader.Stopped(ctx, "Personal Work")

			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(stopped).To(gomega.BeTrue())
			gomega.Expect(found).To(gomega.Equal(marker))

			gomega.Expect(os.Remove(filepath.Join(state, marker))).To(gomega.Succeed())
		}
	})

	ginkgo.It("fails CLOSED when the state dir cannot be read", func() {
		// A stat error that is not "not found" must not read as "no marker": that
		// would adopt a worker whose subject the operator stood down.
		file := filepath.Join(ginkgo.GinkgoT().TempDir(), "not-a-dir")
		gomega.Expect(os.WriteFile(file, []byte("x"), 0o600)).To(gomega.Succeed())
		broken := NewFileSubjectReader(subjects, file)

		_, stopped, err := broken.Stopped(ctx, "personal work")

		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(stopped).To(gomega.BeFalse())
	})

	ginkgo.It("never reports an empty subject as stopped", func() {
		_, stopped, err := reader.Stopped(ctx, "")

		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(stopped).To(gomega.BeFalse())
	})

	ginkgo.It("slugs a subject the way manager-liveness.py writes it", func() {
		// `base_slug` folds every non-alphanumeric run to one hyphen. A different
		// fold would look for a file nobody writes and read a stopped subject as live.
		gomega.Expect(baseSlug("BRO-21389 MDM via REST")).To(gomega.Equal("bro-21389-mdm-via-rest"))
		gomega.Expect(baseSlug("Personal")).To(gomega.Equal("personal"))
	})
})
