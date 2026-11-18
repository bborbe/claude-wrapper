// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command hook is the single Claude Code hook entry for the Bash guard chain.
//
// It replaces the per-call script chain: one process per event instead of
// twenty processes and sixty-odd children. Install it and register it in
// ~/.claude/settings.json as `hook || exit 2`, so a missing or crashed binary
// refuses the call rather than allowing it.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bborbe/claude-wrapper/pkg/hook"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := hook.Run(ctx, hook.NewDefaultRegistry(), os.Stdin, os.Stdout); err != nil {
		slog.Error("hook failed", "error", err)
		// Exit 2 blocks the tool call. A broken hook must fail closed: a guard
		// that silently stops guarding is worse than a refused command.
		os.Exit(2)
	}
}
