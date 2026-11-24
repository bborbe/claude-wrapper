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

// ⚠️ The defect this file guards: the page refs carry a vault NAME (the open
// surface validates a row against the configured names), while
// `topic-keepalive.py` and `worker-keepalive.py` open the vault DIRECTORY —
// each does `os.path.abspath(os.path.expanduser(--vault))`. A name there
// resolves against the daemon's cwd, the directory does not exist, and the
// script answers `cannot-tell` with exit 1. Measured 2026-11-24 in the live
// daemon log: 364 `lapsed-topic-manager` and 363 `dead-worker` lines, EVERY one
// `cannot-tell`, so the tick had never acted on a manager or a worker.
//
// The fixture asserts the EXACT argument, because the broken form also exits 0
// and prints a plausible verdict word — only the argument distinguishes them.

var _ = ginkgo.Describe("keepaliveVaultPath", func() {
	vaults := []pkg.Vault{
		{Name: "private-personal", Path: "/Users/x/Obsidian/private-personal"},
	}

	ginkgo.It("resolves a configured vault name to its path", func() {
		gomega.Expect(keepaliveVaultPath(vaults, "private-personal")).
			To(gomega.Equal("/Users/x/Obsidian/private-personal"))
	})

	// An unconfigured name is passed through unchanged rather than blanked: the
	// script then reports its own `cannot-tell`, which is visible, where an empty
	// argument would be a different failure the caller could not read.
	ginkgo.It("passes an unconfigured name through unchanged", func() {
		gomega.Expect(keepaliveVaultPath(vaults, "elsewhere")).To(gomega.Equal("elsewhere"))
	})
})

var _ = ginkgo.Describe("the keep-alive decide steps", func() {
	ctx := context.Background()

	vaults := []pkg.Vault{
		{Name: "private-personal", Path: "/Users/x/Obsidian/private-personal"},
	}

	var (
		scriptName string
		gotArgs    []string
	)

	ginkgo.BeforeEach(func() {
		scriptName = ""
		gotArgs = nil
		original := keepaliveScript
		keepaliveScript = func(name string) pkg.KeepaliveScriptRunner {
			return func(ctx context.Context, args ...string) (string, int, error) {
				scriptName = name
				gotArgs = args
				return "leave\nSTOPPED — a deliberate stand-down is not a lapse", 0, nil
			}
		}
		ginkgo.DeferCleanup(func() { keepaliveScript = original })
	})

	ginkgo.It("hands topic-keepalive.py the vault PATH, never its name", func() {
		lister := &pkg.KeepaliveLister{
			Source: func(context.Context) ([]pkg.KeepalivePage, error) {
				return []pkg.KeepalivePage{{
					Vault:     "private-personal",
					Name:      "Collaborative AI Workspace",
					SessionID: "94c5889a-4287-42b6-8eb5-59e803e93571",
					Status:    "in_progress",
				}}, nil
			},
			Cursor: fakeCursor{},
			Kind:   pkg.KeepaliveKindTopic,
			Cap:    20,
		}

		_, err := newManagerDecide(lister, vaults, "topic")(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(scriptName).To(gomega.Equal("topic-keepalive.py"))
		gomega.Expect(argValue(gotArgs, "--vault")).
			To(gomega.Equal("/Users/x/Obsidian/private-personal"))
		gomega.Expect(argValue(gotArgs, "--vault")).NotTo(gomega.Equal("private-personal"))
	})

	ginkgo.It("hands worker-keepalive.py the vault PATH, never its name", func() {
		lister := &pkg.KeepaliveLister{
			Source: func(context.Context) ([]pkg.KeepalivePage, error) {
				return []pkg.KeepalivePage{{
					Vault:     "private-personal",
					Name:      "Some Task",
					SessionID: "aaaaaaaa-1111-2222-3333-444444444444",
					Status:    "in_progress",
				}}, nil
			},
			Cursor: fakeCursor{},
			Kind:   pkg.KeepaliveKindTask,
			Cap:    20,
		}

		_, err := newWorkerDecisions(lister, vaults)(ctx)

		gomega.Expect(err).To(gomega.Not(gomega.HaveOccurred()))
		gomega.Expect(scriptName).To(gomega.Equal("worker-keepalive.py"))
		gomega.Expect(argValue(gotArgs, "--vault")).
			To(gomega.Equal("/Users/x/Obsidian/private-personal"))
		gomega.Expect(argValue(gotArgs, "--vault")).NotTo(gomega.Equal("private-personal"))
	})
})

// fakeCursor is an empty, in-memory cursor so a fixture never touches the disk.
type fakeCursor struct{}

func (fakeCursor) Load(context.Context) (map[string]string, error) { return map[string]string{}, nil }

func (fakeCursor) Save(context.Context, map[string]string) error { return nil }

// argValue returns the argument following flag, or "" when it is absent.
func argValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
