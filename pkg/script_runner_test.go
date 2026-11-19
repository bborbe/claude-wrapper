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

var _ = ginkgo.Describe("ExecScriptRunner", func() {
	var ctx context.Context

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.It("reports a non-zero exit as a result with its output and code", func() {
		result, err := ExecScriptRunner{}.RunScript(
			ctx, "sh", "-c", "echo out; echo err >&2; exit 3",
		)
		gomega.Expect(err).To(gomega.BeNil())
		gomega.Expect(result.ExitCode).To(gomega.Equal(3))
		gomega.Expect(result.Stdout).To(gomega.Equal("out\n"))
		gomega.Expect(result.Stderr).To(gomega.Equal("err\n"))
	})

	ginkgo.It("returns an error when the binary cannot be run", func() {
		_, err := ExecScriptRunner{}.RunScript(ctx, "definitely-not-a-real-binary-xyz")
		gomega.Expect(err).NotTo(gomega.BeNil())
	})

	ginkgo.It("returns an error naming the timeout when the script does not finish", func() {
		runner := ExecScriptRunner{Timeout: 100 * time.Millisecond}
		_, err := runner.RunScript(ctx, "sh", "-c", "sleep 5")
		gomega.Expect(err).NotTo(gomega.BeNil())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("did not finish"))
	})
})
