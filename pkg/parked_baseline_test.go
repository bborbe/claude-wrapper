// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("parked baseline", func() {
	ginkgo.It("resolves the state directories like the session store", func() {
		const sessionID = "123e4567-e89b-12d3-a456-426614174000"

		type stateDirsCase struct {
			name   string
			setEnv bool
		}
		cases := []stateDirsCase{
			{name: "flag value"},
			{name: "env fallback", setEnv: true},
			{name: "agrees with the store", setEnv: true},
		}

		for _, tc := range cases {
			tc := tc
			root := ginkgo.GinkgoT().TempDir()
			if tc.setEnv {
				gomega.Expect(os.Setenv(StateDirEnv, root)).To(gomega.Succeed())
				ginkgo.DeferCleanup(func() { _ = os.Unsetenv(StateDirEnv) })
			}

			arg := root
			wantSessions := root
			if tc.setEnv {
				arg = ""
				wantSessions = filepath.Join(root, "sessions")
			}

			sessions, parked, err := StateDirs(arg)
			fmt.Printf("case=%s sessions=%s parked=%s\n", tc.name, sessions, parked)
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(sessions).To(gomega.Equal(wantSessions))
			gomega.Expect(parked).To(
				gomega.Equal(filepath.Join(filepath.Dir(wantSessions), "parked")),
			)

			if tc.name == "agrees with the store" {
				gomega.Expect(NewSessionStore("").Dir(sessionID)).To(
					gomega.Equal(filepath.Join(sessions, sessionID)),
				)
			}
		}
	})
})
