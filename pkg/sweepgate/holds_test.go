// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("reads the operator hold store", func() {
	ctx := context.Background()

	ginkgo.It("reads the checked-in fixture", func() {
		path := filepath.Join("..", "..", "testdata", "sweepgate", "inputs",
			"session-holds.json")
		holds, err := ReadHolds(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(holds).To(gomega.HaveLen(1))
		reason, ok := HeldReason(holds, "held-session-0001")
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.Equal("operator parked it"))
		gomega.Expect(HeldAge(fixedNow, holds["held-session-0001"].HeldAt)).
			To(gomega.Equal("?"))
	})

	ginkgo.It("degrades an unreadable hold store to nothing held", func() {
		holds, err := ReadHolds(ctx, filepath.Join(ginkgo.GinkgoT().TempDir(), "missing.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(holds).To(gomega.BeEmpty())

		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "holds.json")
		gomega.Expect(os.WriteFile(path, []byte("{half"), 0o600)).To(gomega.Succeed())
		holds, err = ReadHolds(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(holds).To(gomega.BeEmpty())
	})

	ginkgo.It("treats a non-object entry as not held", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "holds.json")
		gomega.Expect(os.WriteFile(path, []byte(`{"holds":{"s":"nope"}}`), 0o600)).
			To(gomega.Succeed())
		holds, err := ReadHolds(ctx, path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(holds).To(gomega.BeEmpty())
		_, ok := HeldReason(holds, "s")
		gomega.Expect(ok).To(gomega.BeFalse())
	})

	ginkgo.It("treats an empty session id as not held", func() {
		_, ok := HeldReason(map[string]Hold{"s": {Reason: "x"}}, "")
		gomega.Expect(ok).To(gomega.BeFalse())
	})

	ginkgo.It("defaults an empty reason to held", func() {
		reason, ok := HeldReason(map[string]Hold{"s": {}}, "s")
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(reason).To(gomega.Equal("held"))
	})

	ginkgo.It("renders each age band", func() {
		base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		gomega.Expect(HeldAge(base, base.Add(-12*time.Minute).Format(heldAtFormat))).
			To(gomega.Equal("12m"))
		gomega.Expect(HeldAge(base, base.Add(-4*time.Hour).Format(heldAtFormat))).
			To(gomega.Equal("4h"))
		gomega.Expect(HeldAge(base, base.Add(-72*time.Hour).Format(heldAtFormat))).
			To(gomega.Equal("3d"))
		gomega.Expect(HeldAge(base, "garbage")).To(gomega.Equal("?"))
	})

	ginkgo.It("clamps a future stamp to zero", func() {
		base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
		gomega.Expect(HeldAge(base, base.Add(time.Hour).Format(heldAtFormat))).
			To(gomega.Equal("0m"))
	})
})
