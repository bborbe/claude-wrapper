// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

func TestMain(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Main Suite")
}

var _ = AfterSuite(func() {
	gexec.CleanupBuildArtifacts()
})

var _ = Describe("Main", func() {
	// The binary's behaviour is covered in pkg/hook: Run, EncodeDecision and the
	// guards are all tested there against a buffer, so this spec only proves the
	// command compiles and links.
	It("compiles", func() {
		_, err := gexec.Build(".", "-mod=mod", "-buildvcs=false")
		Expect(err).NotTo(HaveOccurred())
	})
})
