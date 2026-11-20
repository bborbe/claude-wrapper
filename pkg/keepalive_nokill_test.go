// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// SC2's no-kill row, both limbs. The single `KeepaliveStopper` tripwire was ruled
// non-discriminating on 2026-11-20: a `Supervisor.Stop` added anywhere in the
// restart path never touches that seam, so a fixture asserting only "the tripwire
// was not called" passes on an act that stops processes. These two specs are the
// replacement, and BOTH must be red when a stop call is added to the restart path.
var _ = ginkgo.Describe("the restart path stops nothing", func() {
	ctx := context.Background()

	// LIMB 1 — the exact call set. A restart types the restart command into the
	// pane and calls nothing else: the opener is untouched and the pane sender
	// receives exactly one call, with the command the caller supplied.
	ginkgo.It("a restart touches only the pane sender", func() {
		var opened []OpenRow
		var sent []string
		run := NewKeepaliveAct(
			func(_ context.Context, row OpenRow) error { opened = append(opened, row); return nil },
			func(_ context.Context, sessionID, text string) error {
				sent = append(sent, sessionID+"|"+text)
				return nil
			},
			nil, // production's no-kill tripwire: never called
			func(sessionID string) string { return "/supervisor:restart " + sessionID },
		)

		err := run(ctx, "restart", LapsedManagerDecision{SessionID: "s1", Vault: "v", Name: "n"})

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(sent).To(gomega.Equal([]string{"s1|/supervisor:restart s1"}))
		gomega.Expect(opened).To(gomega.BeEmpty())
	})

	// LIMB 2 — the symbol check. Limb 1 only covers the paths this fixture drives;
	// this one covers every path in the act's own source, which is where a stop
	// call would actually be added.
	ginkgo.It("the act's source contains no stop or kill call", func() {
		// The wiring file is included deliberately: a Stop added where the act is
		// CONSTRUCTED bypasses both limbs otherwise, which is exactly the hole the
		// single tripwire left.
		for _, name := range []string{
			"keepalive_act.go",
			"keepalive_lapsed_manager.go",
			filepath.Join("..", "main_keepalive.go"),
		} {
			source, err := os.ReadFile(filepath.Join(".", name))
			gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
			text := string(source)
			for _, forbidden := range []string{".Stop(", ".Kill(", "exec.Command(\"kill\""} {
				gomega.Expect(strings.Contains(text, forbidden)).To(
					gomega.BeFalse(),
					"%s contains %q — the keepalive tick kills nothing (ruling efc771fb holds by ABSENCE)", name, forbidden,
				)
			}
		}
	})
})
