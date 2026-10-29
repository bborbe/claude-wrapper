// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Qualified ginkgo and gomega, not dot-imports: ginkgo exports a `Pending`
// decorator, which collides with this package's own `Pending` type the moment a
// spec is compiled in-package — and these specs must be in-package, because
// buildArgs, childEnv and validateEnv are unexported.
func TestPkg(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Pkg Suite")
}
