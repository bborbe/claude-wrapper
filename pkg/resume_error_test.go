// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"

	"github.com/bborbe/errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ResumeError", func() {
	ginkgo.It("carries the row-unreachable code in its text", func() {
		err := resumeFailure(
			ResumeCodeRowUnreachable,
			errors.New(context.Background(), "spawn failed"),
		)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(
			gomega.ContainSubstring("code=" + ResumeCodeRowUnreachable),
		)
	})

	ginkgo.It("carries the path-broken code in its text", func() {
		err := resumeFailure(ResumeCodePathBroken, ErrVaultListUnreadable)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(
			gomega.ContainSubstring("code=" + ResumeCodePathBroken),
		)
	})

	// The whole point of the two codes: a manager reading a failed resume must be
	// able to tell them apart from the text alone.
	ginkgo.It("renders the two causes differently for the same underlying fault", func() {
		rowUnreachable := resumeFailure(ResumeCodeRowUnreachable, ErrVaultListUnreadable)
		pathBroken := resumeFailure(ResumeCodePathBroken, ErrVaultListUnreadable)
		gomega.Expect(rowUnreachable.Error()).NotTo(gomega.Equal(pathBroken.Error()))
	})

	ginkgo.It("unwraps to the cause so errors.Is still matches it", func() {
		err := resumeFailure(ResumeCodePathBroken, ErrVaultListUnreadable)
		gomega.Expect(stderrors.Is(err, ErrVaultListUnreadable)).To(gomega.BeTrue())
	})

	ginkgo.It("returns nil for a nil cause, so a caller can wrap unconditionally", func() {
		gomega.Expect(resumeFailure(ResumeCodePathBroken, nil)).To(gomega.Succeed())
	})
})
