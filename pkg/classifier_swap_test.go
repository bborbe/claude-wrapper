// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("classifyLiveness", func() {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	sid := "11111111-1111-1111-1111-111111111111"
	cwd := "/work/x"
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)

	newTick := func(dir string) *FlaggedTick {
		tick := &FlaggedTick{ProjectsDir: dir}
		tick.Now = fixedNow(now)
		return tick
	}

	ginkgo.It("F2: a live child reads live", func() {
		tick := newTick(ginkgo.GinkgoT().TempDir())
		row := FlaggedRow{SessionID: sid, Cwd: cwd}
		gomega.Expect(tick.classifyLiveness(context.Background(), row, nil, map[string]bool{sid: true}, nil, now)).
			To(gomega.Equal(livenessLive))
	})

	ginkgo.It("F3: a stale transcript with an ended ledger row reads absent", func() {
		dir := ginkgo.GinkgoT().TempDir()
		gomega.Expect(os.MkdirAll(filepath.Join(dir, slug), 0o755)).To(gomega.Succeed())
		path := filepath.Join(dir, slug, sid+".jsonl")
		gomega.Expect(os.WriteFile(path, []byte("{}"), 0o644)).To(gomega.Succeed())
		old := now.Add(-time.Hour)
		gomega.Expect(os.Chtimes(path, old, old)).To(gomega.Succeed())

		tick := newTick(dir)
		row := FlaggedRow{SessionID: sid, Cwd: cwd}
		gomega.Expect(tick.classifyLiveness(context.Background(), row, map[string]struct{}{sid: {}}, nil, nil, now)).
			To(gomega.Equal(livenessAbsent))
	})

	ginkgo.It("F5: an unreadable liveness source reads indeterminate", func() {
		tick := newTick(ginkgo.GinkgoT().TempDir())
		// The source's registry read failed (UNKNOWN) — never auto-open.
		row := FlaggedRow{SessionID: sid, Liveness: "UNKNOWN"}
		gomega.Expect(tick.classifyLiveness(context.Background(), row, nil, nil, nil, now)).
			To(gomega.Equal(livenessIndeterminate))
	})

	ginkgo.It("F5b: a failed child-store read reads indeterminate, never absent", func() {
		tick := newTick(ginkgo.GinkgoT().TempDir())
		row := FlaggedRow{SessionID: sid}
		gomega.Expect(tick.classifyLiveness(
			context.Background(), row, nil, nil, fmt.Errorf("store down"), now,
		)).To(gomega.Equal(livenessIndeterminate))
	})
})
