// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("KeepaliveEffectForVerdict", func() {
	ctx := context.Background()

	// Every fixture below is a token `topic-keepalive.py` actually prints on its
	// first stdout line, copied from its docstring at claude-supervisor
	// origin/master (2026-11-19). A fixture shaped like a word the script never
	// emits would test a contract no procedure meets.
	ginkgo.DescribeTable("maps the script's verdict to the tick's effect",
		func(verdict string, want KeepaliveEffect) {
			got, err := KeepaliveEffectForVerdict(ctx, verdict)
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			gomega.Expect(got).To(gomega.Equal(want))
		},
		ginkgo.Entry("resume", "resume", KeepaliveEffect{Result: KeepaliveResultResumed}),
		ginkgo.Entry("restart", "restart", KeepaliveEffect{Result: KeepaliveResultRestarted}),
		// ⚠️ The cap's own word. If this ever maps to anything but `capped`, the
		// one probe that counts the cap stops counting it.
		ginkgo.Entry("crash-loop is the cap", "crash-loop", KeepaliveEffect{
			Result: KeepaliveResultCapped,
			Reason: "crash-loop",
		}),
		ginkgo.Entry("claimed", "claimed", KeepaliveEffect{
			Result: KeepaliveResultSkipped,
			Reason: "claimed",
		}),
		ginkgo.Entry("leave", "leave", KeepaliveEffect{
			Result: KeepaliveResultKept,
			Reason: "leave",
		}),
		ginkgo.Entry("jump", "jump", KeepaliveEffect{
			Result: KeepaliveResultKept,
			Reason: "jump",
		}),
		ginkgo.Entry("cannot-tell", "cannot-tell", KeepaliveEffect{
			Result: KeepaliveResultSkipped,
			Reason: "cannot-tell",
		}),
		// ⚠️ The open leg belongs to the sibling task; it is named as skipped so an
		// unopened row never reads as a healthy quiet tick.
		ginkgo.Entry("open", "open", KeepaliveEffect{
			Result: KeepaliveResultSkipped,
			Reason: "open-leg",
		}),
		ginkgo.Entry("open-unflagged", "open-unflagged", KeepaliveEffect{
			Result: KeepaliveResultSkipped,
			Reason: "open-leg",
		}),
	)

	ginkgo.It("refuses an empty verdict rather than reporting a quiet tick", func() {
		_, err := KeepaliveEffectForVerdict(ctx, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.It("refuses a word the script does not emit", func() {
		_, err := KeepaliveEffectForVerdict(ctx, "teleported")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
