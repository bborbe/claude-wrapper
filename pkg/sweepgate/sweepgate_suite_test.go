// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sweepgate

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Qualified ginkgo and gomega, not dot-imports: these specs are in-package
// because the guards they drive (Slug, cutSection, ParseTask's helpers) are
// unexported and cannot be reached from an external test package.
func TestSweepgate(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Sweepgate Suite")
}
