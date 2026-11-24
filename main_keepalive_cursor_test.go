// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg"
)

// ⚠️ The defect this file guards: `KeepaliveLister.Advance` existed but was
// called from nowhere in production. The cursor file did not exist on
// 2026-11-24, so every tick loaded an EMPTY map and `RotatePages` started at
// index 0 again — measured 11:23, 11:38 and 11:53 all evaluating the SAME topic
// page, which means the fleet got exactly one manager decision per tick and
// every page past the per-vault cap was never examined at all.
//
// A fixture that only asserts "Select was called" passes identically on a tick
// that never rotates, so this asserts the SAVE.

var _ = ginkgo.Describe("the keep-alive rotation", func() {
	ctx := context.Background()

	vaults := []pkg.Vault{
		{Name: "private-personal", Path: "/Users/x/Obsidian/private-personal"},
	}

	var (
		saved      map[string]string
		saveCalls  int
		scriptName string
	)

	ginkgo.BeforeEach(func() {
		saved = nil
		saveCalls = 0
		scriptName = ""
		original := keepaliveScript
		keepaliveScript = func(name string) pkg.KeepaliveScriptRunner {
			return func(ctx context.Context, args ...string) (string, int, error) {
				scriptName = name
				return "leave\nSTOPPED — a deliberate stand-down is not a lapse", 0, nil
			}
		}
		ginkgo.DeferCleanup(func() { keepaliveScript = original })
	})

	ginkgo.It("saves the evaluated topic page as the cursor, so the next tick starts after it", func() {
		cursor := &recordingCursor{save: func(m map[string]string) {
			saveCalls++
			saved = m
		}}
		lister := &pkg.KeepaliveLister{
			Source: func(context.Context) ([]pkg.KeepalivePage, error) {
				return []pkg.KeepalivePage{{
					Vault:     "private-personal",
					Name:      "Collaborative AI Workspace",
					SessionID: "94c5889a-4287-42b6-8eb5-59e803e93571",
				}}, nil
			},
			Cursor: cursor,
			Kind:   pkg.KeepaliveKindTopic,
			Cap:    20,
		}

		_, err := newManagerDecide(lister, vaults, "topic")(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(scriptName).To(gomega.Equal("topic-keepalive.py"))
		gomega.Expect(saveCalls).To(gomega.Equal(1), "a tick that never advances re-evaluates the same page forever")
		gomega.Expect(saved).To(gomega.Equal(map[string]string{
			"private-personal": "Collaborative AI Workspace",
		}))
	})

	ginkgo.It("saves the last evaluated task page per vault", func() {
		cursor := &recordingCursor{save: func(m map[string]string) {
			saveCalls++
			saved = m
		}}
		lister := &pkg.KeepaliveLister{
			Source: func(context.Context) ([]pkg.KeepalivePage, error) {
				return []pkg.KeepalivePage{
					{Vault: "private-personal", Name: "Task A", SessionID: "aaaaaaaa-1111", Status: "in_progress"},
					{Vault: "private-personal", Name: "Task B", SessionID: "bbbbbbbb-2222", Status: "in_progress"},
				}, nil
			},
			Cursor: cursor,
			Kind:   pkg.KeepaliveKindTask,
			Cap:    20,
		}

		_, err := newWorkerDecisions(lister, vaults)(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(scriptName).To(gomega.Equal("worker-keepalive.py"))
		gomega.Expect(saveCalls).To(gomega.Equal(1))
		gomega.Expect(saved).To(gomega.Equal(map[string]string{"private-personal": "Task B"}))
	})
})

// recordingCursor is an in-memory cursor store that reports its Save.
type recordingCursor struct {
	loaded map[string]string
	save   func(map[string]string)
}

func (c *recordingCursor) Load(context.Context) (map[string]string, error) {
	if c.loaded == nil {
		return map[string]string{}, nil
	}
	return c.loaded, nil
}

func (c *recordingCursor) Save(_ context.Context, m map[string]string) error {
	c.save(m)
	return nil
}
