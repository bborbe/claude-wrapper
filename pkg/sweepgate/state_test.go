// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("state files", func() {
	ctx := context.Background()

	ginkgo.It("reports first run when the state file is missing", func() {
		_, failReason, err := LoadStored(ctx, filepath.Join(ginkgo.GinkgoT().TempDir(), "x.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failReason).To(gomega.Equal("first run — no state file"))
	})

	ginkgo.It("reports unreadable for a directory in place of the file", func() {
		dir := ginkgo.GinkgoT().TempDir()
		_, failReason, err := LoadStored(ctx, dir)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failReason).To(gomega.HavePrefix("state unreadable ("))
	})

	ginkgo.It("reports a parse error for malformed JSON", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "x.json")
		gomega.Expect(os.WriteFile(path, []byte("{not json"), 0o600)).To(gomega.Succeed())
		_, failReason, err := LoadStored(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failReason).To(gomega.HavePrefix("state parse error ("))
	})

	ginkgo.It("reports a parse error when the digest key is absent", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "x.json")
		gomega.Expect(os.WriteFile(path, []byte(`{"topic": "x"}`), 0o600)).To(gomega.Succeed())
		_, failReason, err := LoadStored(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failReason).To(gomega.Equal("state parse error (no digest key)"))
	})

	ginkgo.It("reports no digest key for a non-object", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "x.json")
		gomega.Expect(os.WriteFile(path, []byte(`[]`), 0o600)).To(gomega.Succeed())
		_, failReason, err := LoadStored(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failReason).To(gomega.Equal("state parse error (no digest key)"))
	})

	ginkgo.It("round-trips a saved state and its busy-since map", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "state.json")
		state := State{
			Topic:      "Parity Topic",
			Digest:     "abc123",
			Members:    2,
			BusySince:  map[string]BusyStamp{"Alpha": {Since: 1.5, Progress: "p1"}},
			RecordedAt: "2026-01-01T00:00:00Z",
		}
		gomega.Expect(SaveStored(ctx, path, state)).To(gomega.Succeed())

		digest, failReason, err := LoadStored(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failReason).To(gomega.BeEmpty())
		gomega.Expect(digest).To(gomega.Equal("abc123"))

		loaded, err := LoadState(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded.Members).To(gomega.Equal(2))
		gomega.Expect(loaded.BusySince["Alpha"].Progress).To(gomega.Equal("p1"))
		gomega.Expect(loaded.BusySince["Alpha"].Since).To(gomega.Equal(1.5))
	})

	ginkgo.It("returns the zero state when LoadState cannot read the file", func() {
		loaded, err := LoadState(ctx, filepath.Join(ginkgo.GinkgoT().TempDir(), "missing.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded).To(gomega.Equal(State{}))
	})

	ginkgo.It("round-trips a settled record", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "settled.json")
		settled := Settled{
			Subject: "Parity Topic", RowKey: "row-1", RecordedAt: "2026-01-01T00:00:00Z",
		}
		gomega.Expect(SaveSettled(ctx, path, settled)).To(gomega.Succeed())
		loaded, err := LoadSettled(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded).To(gomega.Equal(settled))
	})

	ginkgo.It("reads a missing settled record as no memory", func() {
		loaded, err := LoadSettled(ctx, filepath.Join(ginkgo.GinkgoT().TempDir(), "missing.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded).To(gomega.Equal(Settled{}))
	})

	ginkgo.It("reads a malformed settled record as no memory", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "settled.json")
		gomega.Expect(os.WriteFile(path, []byte("{half"), 0o600)).To(gomega.Succeed())
		loaded, err := LoadSettled(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(loaded).To(gomega.Equal(Settled{}))
	})
})

var _ = ginkgo.Describe("writes the tick and card files", func() {
	ctx := context.Background()

	ginkgo.It("strips OSC 8 links and normalises trailing newlines", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "tick.txt")
		frame := "a \x1b]8;;https://example/\x1b\\link\x1b]8;;\x1b\\ b\n\n\n"
		gomega.Expect(WriteTickFile(ctx, path, frame)).To(gomega.Succeed())
		raw, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).To(gomega.Equal("a link b\n"))
		gomega.Expect(strings.Contains(string(raw), "\x1b]8;")).To(gomega.BeFalse())
	})

	ginkgo.It("creates the tick file mode 0600", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "tick.txt")
		gomega.Expect(WriteTickFile(ctx, path, "frame\n")).To(gomega.Succeed())
		info, err := os.Stat(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(info.Mode().Perm()).To(gomega.Equal(os.FileMode(0o600)))
	})

	ginkgo.It("removes the card file when the payload is nil", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "card.json")
		gomega.Expect(WriteCardFile(ctx, path, []byte(`{"a":1}`))).To(gomega.Succeed())
		gomega.Expect(WriteCardFile(ctx, path, nil)).To(gomega.Succeed())
		_, err := os.Stat(path)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("tolerates removing a card file that does not exist", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "card.json")
		gomega.Expect(WriteCardFile(ctx, path, nil)).To(gomega.Succeed())
	})

	ginkgo.It("writes a non-nil card payload verbatim", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "card.json")
		gomega.Expect(WriteCardFile(ctx, path, []byte(`{"a":1}`))).To(gomega.Succeed())
		raw, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).To(gomega.Equal(`{"a":1}`))
	})

	ginkgo.It("StripLinks keeps the wrapped text", func() {
		gomega.Expect(StripLinks("\x1b]8;;u\x1b\\visible\x1b]8;;\x1b\\")).
			To(gomega.Equal("visible"))
	})
})
