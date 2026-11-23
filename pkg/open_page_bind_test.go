// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// These specs pin the TAB BIND — the half that writes an opened goal manager's
// session id onto its own page at open time, so the stamp does not depend on the
// `/vault-cli:work-on-goal` start command landing.
//
// ⚠️ The load-bearing case is the ownership branch: a goal page another session
// already owns must be left intact and the bind must report `appended`. A spec
// that only covered the unowned page would pass on an unconditional overwrite,
// which is exactly the defect this branch exists to prevent. The ordered call
// lists are deliberate: they pin that the owner is READ before the stamp, and
// that the foreign owner is never written back.
var _ = ginkgo.Describe("vaultCLIPageBinder binds an opened goal manager to its page", func() {
	const (
		vault      = "test-vault"
		subject    = "Some Goal"
		pagePath   = "/vault/24 Goals/Some Goal.md"
		sessionID  = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		foreignOwn = "11111111-2222-4333-8444-555555555555"
	)

	var (
		ctx     context.Context
		runner  *goalBindRunner
		binder  OpenPageBinder
		request OpenTabRequest
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
		runner = &goalBindRunner{}
		binder = NewVaultCLIPageBinder(runner)
		request = OpenTabRequest{
			Kind:     OpenKindGoal,
			Role:     "manager",
			Subject:  subject,
			PagePath: pagePath,
			Vault:    Vault{Name: vault},
		}
	})

	ginkgo.Context("an unowned goal page", func() {
		ginkgo.It("stamps the session and reports stamped", func() {
			bind, err := binder.BindOpenedPage(ctx, request, sessionID)
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(bind).NotTo(gomega.BeNil())
			gomega.Expect(bind.SessionID).To(gomega.Equal(sessionID))
			gomega.Expect(bind.Task).To(gomega.Equal(subject))
			gomega.Expect(bind.Action).To(gomega.Equal(OpenBindStamped))
			gomega.Expect(runner.calls).To(gomega.Equal([][]string{
				{"vault-cli", "--vault", vault, "goal", "get", subject, "claude_session_id"},
				{"vault-cli", "--vault", vault, "goal", "set", subject, "claude_session_id", sessionID},
			}))
		})
	})

	ginkgo.Context("a goal page another session already owns", func() {
		ginkgo.BeforeEach(func() { runner.owner = foreignOwn })

		ginkgo.It("leaves the owner intact and reports appended", func() {
			bind, err := binder.BindOpenedPage(ctx, request, sessionID)
			gomega.Expect(err).To(gomega.BeNil())
			gomega.Expect(bind).NotTo(gomega.BeNil())
			gomega.Expect(bind.Action).To(gomega.Equal(OpenBindAppended))
			gomega.Expect(runner.countSub("set")).To(gomega.Equal(0),
				"a foreign owner must never be overwritten")
			gomega.Expect(runner.countSub("get")).To(gomega.Equal(1))
		})
	})

	ginkgo.Context("a row the binder must not write", func() {
		ginkgo.DescribeTable("writes nothing",
			func(mutate func(*OpenTabRequest, *string)) {
				sid := sessionID
				mutate(&request, &sid)
				bind, err := binder.BindOpenedPage(ctx, request, sid)
				gomega.Expect(err).To(gomega.BeNil())
				gomega.Expect(bind).To(gomega.BeNil())
				gomega.Expect(runner.calls).To(gomega.BeEmpty())
			},
			ginkgo.Entry("a task row — the headless spawn binds it", func(r *OpenTabRequest, _ *string) {
				r.Kind = OpenKindTask
			}),
			ginkgo.Entry("a topic row — a separate defect with its own start command",
				func(r *OpenTabRequest, _ *string) { r.Kind = OpenKindTopic }),
			ginkgo.Entry("an empty session id — registration timed out",
				func(_ *OpenTabRequest, sid *string) { *sid = "" }),
			ginkgo.Entry("a row naming no page", func(r *OpenTabRequest, _ *string) {
				r.PagePath = ""
			}),
			ginkgo.Entry("a row naming no vault", func(r *OpenTabRequest, _ *string) {
				r.Vault = Vault{}
			}),
		)
	})

	ginkgo.Context("a failing vault-cli", func() {
		ginkgo.It("returns the read error rather than stamping", func() {
			runner.errOn = map[string]error{"get": errors.New("vault-cli exploded")}
			bind, err := binder.BindOpenedPage(ctx, request, sessionID)
			gomega.Expect(err).NotTo(gomega.BeNil())
			gomega.Expect(bind).To(gomega.BeNil())
			gomega.Expect(runner.countSub("set")).To(gomega.Equal(0))
		})
	})
})

// goalBindRunner fakes `vault-cli --vault <v> goal <sub> …`.
type goalBindRunner struct {
	owner string
	errOn map[string]error
	calls [][]string
}

func (r *goalBindRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	// argv layout: [vault-cli, --vault, <vault>, goal, <sub>, ...]
	if len(call) >= 5 && call[3] == "goal" {
		if err, ok := r.errOn[call[4]]; ok {
			return nil, err
		}
		if call[4] == "get" {
			return []byte(r.owner + "\n"), nil
		}
	}
	return nil, nil
}

// countSub counts calls whose goal subcommand is `sub`.
func (r *goalBindRunner) countSub(sub string) int {
	n := 0
	for _, call := range r.calls {
		if len(call) >= 5 && call[3] == "goal" && call[4] == sub {
			n++
		}
	}
	return n
}
