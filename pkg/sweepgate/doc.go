// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sweepgate is the Go port of the vault-side manager sweep gate.
//
// This package owns the half of the gate that does not render: configuration and
// path resolution, task frontmatter parsing, tracked-set resolution, the change
// digest, the state-file readers and writers, and the four exit codes the later
// modes return. Nothing here renders a frame.
//
// The reference implementation is the frozen Python script at
// `testdata/sweepgate/reference/sweep-gate.py`. That file is retained permanently
// and is never imported or wired into the daemon; it exists so the port's tests
// can diff against it and so a Python-generated golden can never be confused with
// one produced by this package.
//
// The per-vault task selection index (`tasks-index-<sha12>.json`) the reference
// keeps is deliberately NOT ported. This package reads every `25 Tasks/` file on
// each resolution, so the cache can never change an answer, and the index version
// constant is absent from the Go tree on purpose.
package sweepgate
