// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("runScript", func() {
	ginkgo.It("returns the checker's exit code without an error", func() {
		output, code, err := runScript(context.Background(), "sh", "-c", "echo owed; exit 10")
		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(code).To(gomega.Equal(10))
		gomega.Expect(output).To(gomega.ContainSubstring("owed"))
	})

	// A cancelled context SIGKILLs the child, and ExitCode() reports -1 for a
	// signal-terminated process. Returning that code with a nil error made every
	// caller read a daemon shutdown as the checker's own -1 verdict and record a
	// false `check-failed` row.
	ginkgo.It("reports a cancellation as an error rather than a -1 verdict", func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		_, code, err := runScript(ctx, "sleep", "5")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(code).To(gomega.Equal(-1))
	})
})
